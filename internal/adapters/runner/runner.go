package runner

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/adapters/coreapi"
	"github.com/marcel-breuer/webguard-instance/internal/adapters/domainlookup"
	"github.com/marcel-breuer/webguard-instance/internal/adapters/target"
	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/config"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

const fixedHTTPRetryTimes = 1
const fixedHTTPRetryDelay = 250 * time.Millisecond
const fixedHTTPMaxRedirects = 5
const fixedHTTPTimeoutSeconds = 30
const fixedHTTPMaxResponseBytes = 2 << 20
const fixedHTTPMaxRequestBodyBytes = 1 << 20
const fixedHTTPMaxRequestHeaders = 64
const fixedHTTPMaxRequestHeaderBytes = 32 << 10
const fixedPingTimeoutSeconds = 5

var pingLatencyPattern = regexp.MustCompile(`time[=<]([0-9]+(?:\.[0-9]+)?)\s*ms`)

type DomainLookup interface {
	Lookup(ctx context.Context, target string) (domainlookup.Result, error)
}

type PingCommandExecutor func(context.Context, string, int) ([]byte, error)

type HTTPChecker interface {
	Check(context.Context, monitor.Monitoring) (int, string, error)
}

type HTTPCheckFunc func(context.Context, monitor.Monitoring) (int, string, error)

func (f HTTPCheckFunc) Check(ctx context.Context, monitoring monitor.Monitoring) (int, string, error) {
	return f(ctx, monitoring)
}

type MonitoringService struct {
	client          application.MonitoringGateway
	cfg             config.Config
	logger          *log.Logger
	domainLookup    DomainLookup
	dnsChecker      *DNSRecordChecker
	pingExecutor    PingCommandExecutor
	httpChecker     HTTPChecker
	executors       *executorRegistry
	telemetry       *application.Telemetry
	now             func() time.Time
	responseCadence *responseCadence
	phaseScheduleMu sync.Mutex
	phaseLastRuns   map[ExecutionPhase]time.Time
}

func NewWithTelemetry(client application.MonitoringGateway, cfg config.Config, logger *log.Logger, telemetry *application.Telemetry) *MonitoringService {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	if telemetry == nil {
		telemetry = application.NewTelemetry()
	}
	service := &MonitoringService{
		client:          client,
		cfg:             cfg,
		logger:          logger,
		domainLookup:    domainlookup.New(10 * time.Second),
		dnsChecker:      NewDNSRecordChecker(nil, logger),
		pingExecutor:    runPingCommand,
		telemetry:       telemetry,
		now:             time.Now,
		responseCadence: newResponseCadence(),
		phaseLastRuns:   make(map[ExecutionPhase]time.Time),
	}
	service.httpChecker = HTTPCheckFunc(service.performHTTPRequest)
	service.executors = service.newExecutorRegistry()
	return service
}

// phaseDispatch describes how one phase fetches, filters, and reports its
// monitorings; the worker-pool dispatch itself is shared by every phase.
type phaseDispatch struct {
	phase ExecutionPhase
	// name appears inside progress logs and title starts the summary log.
	name            string
	title           string
	unsupportedName string
	// cadence enforces per-monitoring response intervals; nil dispatches every
	// supported monitoring.
	cadence   *responseCadence
	logResult func(monitor.Monitoring, Execution)
}

func (r *MonitoringService) runResponse(ctx context.Context) error {
	r.responseCadence.prune(r.now())
	return r.dispatchPhase(ctx, phaseDispatch{
		phase:           PhaseResponse,
		name:            "response",
		title:           "Response",
		unsupportedName: "passive/unsupported response",
		cadence:         r.responseCadence,
		logResult: func(monitoring monitor.Monitoring, execution Execution) {
			r.logger.Printf(
				"Response monitoring result computed (monitoring_id=%s type=%s %s)",
				monitoring.ID,
				monitoring.Type,
				execution,
			)
		},
	})
}

func (r *MonitoringService) runSSL(ctx context.Context) error {
	return r.runAuxiliaryPhase(ctx, phaseDispatch{
		phase:           PhaseSSL,
		name:            "SSL",
		title:           "SSL",
		unsupportedName: "passive/unsupported SSL",
	})
}

func (r *MonitoringService) runDomainExpiration(ctx context.Context) error {
	return r.runAuxiliaryPhase(ctx, phaseDispatch{
		phase:           PhaseDomainExpiration,
		name:            "domain expiration",
		title:           "Domain expiration",
		unsupportedName: "unsupported domain expiration",
		logResult: func(monitoring monitor.Monitoring, execution Execution) {
			r.logger.Printf(
				"Domain expiration monitoring result computed (monitoring_id=%s %s)",
				monitoring.ID,
				execution,
			)
		},
	})
}

func (r *MonitoringService) runAuxiliaryPhase(ctx context.Context, dispatch phaseDispatch) error {
	if !r.allowAuxiliaryPhase(dispatch.phase) {
		return nil
	}
	if err := r.dispatchPhase(ctx, dispatch); err != nil {
		// A failed fetch dispatched nothing, so retry on the next tick instead
		// of waiting for the full auxiliary interval.
		r.resetAuxiliaryPhase(dispatch.phase)
		return err
	}
	return nil
}

func (r *MonitoringService) dispatchPhase(ctx context.Context, dispatch phaseDispatch) error {
	r.logger.Printf("Dispatching %s monitoring jobs...", dispatch.name)

	monitorings, err := r.client.GetMonitorings(ctx, r.cfg.WebGuardLocation, r.executors.Types(dispatch.phase))
	if err != nil {
		r.logFetchError(err)
		return err
	}

	if len(monitorings) == 0 {
		r.logger.Printf("No active %s monitoring found.", dispatch.name)
		return nil
	}

	dispatched := 0
	skippedCadence := 0
	skippedMaintenance := 0
	skippedUnsupported := 0

	jobs := make(chan monitor.Monitoring)
	var workers sync.WaitGroup

	workerCount := r.workerLimit()
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for monitoring := range jobs {
				r.runDispatchedMonitoring(ctx, dispatch, monitoring)
			}
		}()
	}

	for _, monitoring := range monitorings {
		if !r.executors.Supports(dispatch.phase, monitoring.Type) {
			skippedUnsupported++
			r.logger.Printf(
				"Skipping %s monitoring (monitoring_id=%s type=%s)",
				dispatch.unsupportedName,
				monitoring.ID,
				monitoring.Type,
			)
			continue
		}

		if dispatch.cadence != nil && !responseDue(monitoring, r.now()) {
			skippedCadence++
			continue
		}

		if monitoring.MaintenanceActive {
			skippedMaintenance++
			// SSL maintenance yields an empty execution, which publishes nothing.
			r.publishExecution(ctx, maintenanceExecution(dispatch.phase, monitoring.ID))
			continue
		}

		dispatched++
		r.telemetry.QueueJob()
		jobs <- monitoring
	}
	close(jobs)
	workers.Wait()

	summary := fmt.Sprintf("%s monitoring dispatch done. total=%d dispatched=%d", dispatch.title, len(monitorings), dispatched)
	if dispatch.cadence != nil {
		summary += fmt.Sprintf(" skipped_cadence=%d", skippedCadence)
	}
	r.logger.Printf("%s skipped_maintenance=%d skipped_unsupported=%d", summary, skippedMaintenance, skippedUnsupported)

	return nil
}

func (r *MonitoringService) runDispatchedMonitoring(ctx context.Context, dispatch phaseDispatch, monitoring monitor.Monitoring) {
	if dispatch.cadence != nil && !dispatch.cadence.tryStart(monitoring, r.cfg.WebGuardLocation, r.now()) {
		r.telemetry.DropQueuedJob(string(dispatch.phase), "cadence")
		r.logger.Printf("Skipping %s monitoring until its selected interval is due (monitoring_id=%s interval_seconds=%d)", dispatch.name, monitoring.ID, monitoring.CheckIntervalSeconds)
		return
	}

	execution, _ := r.executeMonitoring(ctx, dispatch.phase, monitoring)
	r.logger.Printf("run_id=%s monitoring_id=%s phase=%s outcome=%s", application.RunID(ctx), monitoring.ID, dispatch.phase, executionOutcome(execution))
	if dispatch.logResult != nil {
		dispatch.logResult(monitoring, execution)
	}
	r.publishExecution(ctx, execution)
}

// RunMonitoring runs every phase once and waits for all of them.
func (r *MonitoringService) RunMonitoring(ctx context.Context) error {
	r.logger.Println("Dispatching all monitoring jobs...")
	if r.cfg.JobLeasesEnabled {
		return r.runClaimedJobs(ctx)
	}

	return application.NewCoordinator(r.logger, r.responsePhase(), r.sslPhase(), r.domainExpirationPhase()).Run(ctx)
}

// RunResponse runs the response phase only, or the claimed job batch when job
// leases are enabled because Core then schedules every phase. Scheduling it
// independently from RunAuxiliary keeps slow SSL and domain lookups from
// delaying or skipping response checks.
func (r *MonitoringService) RunResponse(ctx context.Context) error {
	if r.cfg.JobLeasesEnabled {
		return r.runClaimedJobs(ctx)
	}

	return application.NewCoordinator(r.logger, r.responsePhase()).Run(ctx)
}

// RunAuxiliary runs the SSL and domain expiration phases on their own cadence.
// It does nothing when job leases are enabled because claimed jobs already
// cover those phases.
func (r *MonitoringService) RunAuxiliary(ctx context.Context) error {
	if r.cfg.JobLeasesEnabled {
		return nil
	}

	return application.NewCoordinator(r.logger, r.sslPhase(), r.domainExpirationPhase()).Run(ctx)
}

func (r *MonitoringService) responsePhase() monitoringPhase {
	return monitoringPhase{name: "response", run: r.runResponse}
}

func (r *MonitoringService) sslPhase() monitoringPhase {
	return monitoringPhase{name: "SSL", run: r.runSSL}
}

func (r *MonitoringService) domainExpirationPhase() monitoringPhase {
	return monitoringPhase{name: "domain expiration", run: r.runDomainExpiration}
}

type monitoringPhase struct {
	name string
	run  func(context.Context) error
}

func (p monitoringPhase) Name() string {
	return p.name
}

func (p monitoringPhase) Run(ctx context.Context) error {
	return p.run(ctx)
}

func (r *MonitoringService) executeMonitoring(ctx context.Context, phase ExecutionPhase, monitoring monitor.Monitoring) (Execution, bool) {
	r.telemetry.StartJob()
	startedAt := time.Now()
	release, err := application.AcquireExecutionSlot(ctx)
	if err != nil {
		r.logger.Printf("Monitoring canceled before execution (monitoring_id=%s phase=%s)", monitoring.ID, phase)
		r.telemetry.FinishJobWithDuration(string(phase), "canceled", time.Since(startedAt))
		return Execution{}, false
	}
	execution, supported := r.executors.Execute(ctx, phase, monitoring)
	release()
	outcome := executionOutcome(execution)
	if !supported {
		outcome = "unsupported"
	}
	r.telemetry.FinishJobWithDuration(string(phase), outcome, time.Since(startedAt))
	return execution, supported
}

func (r *MonitoringService) workerLimit() int {
	workers := max(1, r.cfg.QueueDefaultWorkers)
	if r.cfg.RunMaxConcurrency > 0 {
		workers = min(workers, r.cfg.RunMaxConcurrency)
	}
	return workers
}

func (r *MonitoringService) allowAuxiliaryPhase(phase ExecutionPhase) bool {
	const interval = 5 * time.Minute
	now := r.now()

	r.phaseScheduleMu.Lock()
	defer r.phaseScheduleMu.Unlock()

	if previousRun, ok := r.phaseLastRuns[phase]; ok && now.Sub(previousRun) < interval {
		return false
	}

	r.phaseLastRuns[phase] = now
	return true
}

func (r *MonitoringService) resetAuxiliaryPhase(phase ExecutionPhase) {
	r.phaseScheduleMu.Lock()
	defer r.phaseScheduleMu.Unlock()
	delete(r.phaseLastRuns, phase)
}

func (r *MonitoringService) logFetchError(err error) {
	var statusError *coreapi.HTTPStatusError
	if errors.As(err, &statusError) {
		r.logger.Printf("Failed to fetch monitorings from the Core API (status=%d).", statusError.StatusCode)
		return
	}
	r.logger.Println("Failed to fetch monitorings from the Core API.")
}

func (r *MonitoringService) observeHTTPMonitoring(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64, *int, monitor.RawObservation) {
	start := time.Now()
	observation := monitor.RawObservation{Type: monitoring.Type, ObservedAt: start.UTC()}
	statusCode, _, err := r.httpCheck(ctx, monitoring)
	if err != nil {
		observation.TransportError = stringPointer("http_transport_error")
		return monitor.StatusDown, nil, nil, observation
	}
	httpStatusCode := intPointer(statusCode)
	responseTime := roundMilliseconds(time.Since(start))
	observation.HTTPStatusCode = httpStatusCode
	observation.ResponseTime = &responseTime
	if statusCode >= http.StatusOK && statusCode < http.StatusBadRequest {
		return monitor.StatusUp, &responseTime, httpStatusCode, observation
	}
	return monitor.StatusDown, nil, httpStatusCode, observation
}

func (r *MonitoringService) observeKeywordMonitoring(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64, *int, monitor.RawObservation) {
	start := time.Now()
	observation := monitor.RawObservation{Type: monitoring.Type, ObservedAt: start.UTC()}
	statusCode, body, err := r.httpCheck(ctx, monitoring)
	if err != nil {
		observation.TransportError = stringPointer("http_transport_error")
		observation.KeywordMatched = boolPointer(false)
		return monitor.StatusDown, nil, nil, observation
	}
	httpStatusCode := intPointer(statusCode)
	responseTime := roundMilliseconds(time.Since(start))
	matched := strings.Contains(body, monitoring.Keyword)
	observation.HTTPStatusCode = httpStatusCode
	observation.ResponseTime = &responseTime
	observation.KeywordMatched = boolPointer(matched)
	if matched {
		return monitor.StatusUp, &responseTime, httpStatusCode, observation
	}
	return monitor.StatusDown, nil, httpStatusCode, observation
}

func (r *MonitoringService) httpCheck(ctx context.Context, monitoring monitor.Monitoring) (int, string, error) {
	checker := r.httpChecker
	if checker == nil {
		checker = HTTPCheckFunc(r.performHTTPRequest)
	}
	return checker.Check(ctx, monitoring)
}

func (r *MonitoringService) egressPolicy() target.EgressPolicy {
	return target.EgressPolicy{AllowPrivate: r.cfg.AllowPrivateTargets}
}

func observePingMonitoring(ctx context.Context, monitoring monitor.Monitoring, policy target.EgressPolicy, executor PingCommandExecutor) (monitor.Status, *float64, monitor.RawObservation) {
	start := time.Now()
	observation := monitor.RawObservation{Type: monitoring.Type, ObservedAt: start.UTC(), Connected: boolPointer(false)}
	host, err := target.Host(monitoring.Target)
	if err != nil {
		observation.TransportError = stringPointer("invalid_target")
		return monitor.StatusDown, nil, observation
	}
	if err := target.ValidateHost(ctx, host, policy); err != nil {
		observation.TransportError = stringPointer("target_rejected")
		return monitor.StatusDown, nil, observation
	}

	timeoutSeconds := fixedPingTimeoutSeconds
	if monitoring.Timeout > 0 {
		timeoutSeconds = monitoring.Timeout
	}

	if executor == nil {
		executor = runPingCommand
	}
	output, err := executor(ctx, host, timeoutSeconds)
	responseTime := parsePingLatency(output)
	if err != nil {
		observation.TransportError = stringPointer("ping_failed")
		return monitor.StatusDown, nil, observation
	}

	if responseTime == nil {
		elapsed := roundMilliseconds(time.Since(start))
		responseTime = &elapsed
	}
	observation.Connected = boolPointer(true)
	observation.ResponseTime = responseTime
	return monitor.StatusUp, responseTime, observation
}

func runPingCommand(ctx context.Context, host string, timeoutSeconds int) ([]byte, error) {
	command, args := buildPingCommand(host, timeoutSeconds)
	cmd := exec.CommandContext(ctx, command, args...)
	return cmd.CombinedOutput()
}

func buildPingCommand(host string, timeoutSeconds int) (string, []string) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = fixedPingTimeoutSeconds
	}

	args := []string{
		"-c", "1",
		"-W", strconv.Itoa(timeoutSeconds),
	}

	if parsedIP := net.ParseIP(host); parsedIP != nil {
		if parsedIP.To4() == nil {
			args = append(args, "-6")
		} else {
			args = append(args, "-4")
		}
	}

	args = append(args, host)
	return "ping", args
}

func parsePingLatency(output []byte) *float64 {
	matches := pingLatencyPattern.FindSubmatch(bytes.ToLower(output))
	if len(matches) < 2 {
		return nil
	}

	parsed, err := strconv.ParseFloat(string(matches[1]), 64)
	if err != nil {
		return nil
	}

	rounded := math.Round(parsed*1000) / 1000
	return &rounded
}

func observePortMonitoring(ctx context.Context, monitoring monitor.Monitoring, policy target.EgressPolicy) (monitor.Status, *float64, monitor.RawObservation) {
	start := time.Now()
	observation := monitor.RawObservation{
		Type:       monitoring.Type,
		ObservedAt: start.UTC(),
		Connected:  boolPointer(false),
		Metadata:   map[string]string{"port": strconv.Itoa(monitoring.Port)},
	}
	if monitoring.Port <= 0 {
		observation.TransportError = stringPointer("invalid_port")
		return monitor.StatusDown, nil, observation
	}

	address, err := target.TCPAddress(monitoring.Target, monitoring.Port)
	if err != nil {
		observation.TransportError = stringPointer("invalid_target")
		return monitor.StatusDown, nil, observation
	}

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := target.SafeDialContext(policy)(dialCtx, "tcp", address)
	if err != nil {
		observation.TransportError = stringPointer("connection_failed")
		return monitor.StatusDown, nil, observation
	}
	_ = conn.Close()

	responseTime := roundMilliseconds(time.Since(start))
	observation.Connected = boolPointer(true)
	observation.ResponseTime = &responseTime
	return monitor.StatusUp, &responseTime, observation
}

func (r *MonitoringService) performHTTPRequest(ctx context.Context, monitoring monitor.Monitoring) (int, string, error) {
	targetURL, err := target.HTTPURL(ctx, monitoring.Target, r.egressPolicy())
	if err != nil {
		return 0, "", err
	}

	method := strings.ToLower(strings.TrimSpace(string(monitoring.HTTPMethod)))
	if method == "" || !slices.Contains([]string{"get", "post", "put", "patch", "delete"}, method) {
		method = string(monitor.HTTPMethodGet)
	}

	headers := normalizeHeaders(monitoring.HTTPHeaders)
	body := normalizeBody(monitoring.HTTPBody)
	if len(body) > fixedHTTPMaxRequestBodyBytes {
		return 0, "", fmt.Errorf("monitoring request body exceeds %d bytes", fixedHTTPMaxRequestBodyBytes)
	}
	if len(headers) > fixedHTTPMaxRequestHeaders || headerBytes(headers) > fixedHTTPMaxRequestHeaderBytes {
		return 0, "", fmt.Errorf("monitoring request headers exceed configured limits")
	}
	if method == "get" || method == "delete" {
		body = nil
	}
	if len(body) > 0 && headers["Content-Type"] == "" && headers["content-type"] == "" {
		headers["Content-Type"] = "application/json"
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: target.SafeDialContext(r.egressPolicy()),
		},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= fixedHTTPMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", fixedHTTPMaxRedirects)
			}
			if err := target.ValidateHost(request.Context(), request.URL.Hostname(), r.egressPolicy()); err != nil {
				return err
			}
			return nil
		},
	}
	if monitoring.Timeout > 0 {
		httpClient.Timeout = time.Duration(monitoring.Timeout) * time.Second
	} else {
		httpClient.Timeout = fixedHTTPTimeoutSeconds * time.Second
	}

	retryTimes := fixedHTTPRetryTimes
	attempts := retryTimes + 1
	delay := fixedHTTPRetryDelay

	var firstResponse *httpCheckResult
	var lastErr error
	for _, candidateURL := range httpTargetVariants(targetURL) {
		for attempt := 0; attempt < attempts; attempt++ {
			result, err := performHTTPCheck(ctx, httpClient, method, candidateURL, headers, body, monitoring)
			if err != nil {
				lastErr = err
				if attempt < attempts-1 {
					timer := time.NewTimer(delay)
					select {
					case <-ctx.Done():
						timer.Stop()
						return 0, "", ctx.Err()
					case <-timer.C:
					}
					continue
				}
				break
			}

			if firstResponse == nil {
				firstResponse = &result
			}
			if result.statusCode >= http.StatusOK && result.statusCode < http.StatusBadRequest {
				return result.statusCode, result.body, nil
			}
			break
		}
	}

	if firstResponse != nil {
		return firstResponse.statusCode, firstResponse.body, nil
	}
	return 0, "", lastErr
}

func headerBytes(headers map[string]string) int {
	size := 0
	for name, value := range headers {
		size += len(name) + len(value)
	}
	return size
}

type httpCheckResult struct {
	statusCode int
	body       string
}

func performHTTPCheck(ctx context.Context, client *http.Client, method string, targetURL *url.URL, headers map[string]string, body []byte, monitoring monitor.Monitoring) (httpCheckResult, error) {
	var requestBody io.Reader
	if len(body) > 0 {
		requestBody = bytes.NewReader(body)
	}

	request, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), targetURL.String(), requestBody)
	if err != nil {
		return httpCheckResult{}, err
	}

	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if monitoring.AuthUsername != "" && monitoring.AuthPassword != "" {
		request.SetBasicAuth(monitoring.AuthUsername, monitoring.AuthPassword)
	}

	response, err := client.Do(request)
	if err != nil {
		return httpCheckResult{}, err
	}
	defer response.Body.Close()

	payload, err := readLimitedBody(response.Body, fixedHTTPMaxResponseBytes)
	if err != nil {
		return httpCheckResult{}, err
	}

	return httpCheckResult{statusCode: response.StatusCode, body: string(payload)}, nil
}

func httpTargetVariants(targetURL *url.URL) []*url.URL {
	variants := make([]*url.URL, 0, 4)
	seen := make(map[string]struct{}, 4)

	hostnames := []string{targetURL.Hostname()}
	if alternateHostname := alternateWWWHostname(targetURL.Hostname()); alternateHostname != "" {
		hostnames = append(hostnames, alternateHostname)
	}

	paths := []string{targetURL.Path}
	if targetURL.Path == "" {
		paths = append(paths, "/")
	} else if targetURL.Path == "/" {
		paths = append(paths, "")
	}

	for _, path := range paths {
		for _, hostname := range hostnames {
			variant := *targetURL
			if hostname != targetURL.Hostname() {
				variant.Host = hostname
				if port := targetURL.Port(); port != "" {
					variant.Host = net.JoinHostPort(hostname, port)
				}
			}
			variant.Path = path

			if _, exists := seen[variant.String()]; exists {
				continue
			}
			seen[variant.String()] = struct{}{}
			variants = append(variants, &variant)
		}
	}

	return variants
}

func alternateWWWHostname(hostname string) string {
	if hostname == "" || net.ParseIP(hostname) != nil || !strings.Contains(strings.TrimSuffix(hostname, "."), ".") {
		return ""
	}

	if len(hostname) > len("www.") && strings.EqualFold(hostname[:len("www.")], "www.") {
		return hostname[len("www."):]
	}
	return "www." + hostname
}

func (r *MonitoringService) crawlMonitoringSSL(ctx context.Context, monitoring monitor.Monitoring) monitor.SSLResultPayload {
	payload := monitor.SSLResultPayload{
		MonitoringID: monitoring.ID,
		IsValid:      false,
	}

	address, serverName, err := target.SSLAddressAndServerName(monitoring.Target)
	if err != nil {
		return payload
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rawConn, err := target.SafeDialContext(r.egressPolicy())(dialCtx, "tcp", address)
	if err != nil {
		return payload
	}
	connection := tls.Client(rawConn, &tls.Config{ServerName: serverName})
	err = connection.HandshakeContext(dialCtx)
	if err != nil {
		_ = rawConn.Close()
		return payload
	}
	defer connection.Close()

	peerCertificates := connection.ConnectionState().PeerCertificates
	if len(peerCertificates) == 0 {
		return payload
	}

	certificate := peerCertificates[0]
	now := time.Now()
	if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
		return payload
	}
	if err := certificate.VerifyHostname(serverName); err != nil {
		return payload
	}

	payload.IsValid = true
	expiresAt := certificate.NotAfter.UTC()
	issuedAt := certificate.NotBefore.UTC()
	payload.ExpiresAt = &expiresAt
	payload.IssuedAt = &issuedAt

	issuer := certificate.Issuer.CommonName
	if issuer == "" {
		issuer = certificate.Issuer.String()
	}
	if issuer != "" {
		payload.Issuer = &issuer
	}

	return payload
}

func (r *MonitoringService) crawlDomainExpiration(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, monitor.DomainResultPayload, bool) {
	lookup := r.domainLookup
	if lookup == nil {
		lookup = domainlookup.New(10 * time.Second)
	}

	result, err := lookup.Lookup(ctx, monitoring.Target)
	if err != nil {
		if domainlookup.IsTemporary(err) {
			return monitor.StatusUnknown, monitor.DomainResultPayload{}, false
		}
		now := time.Now().UTC()
		return monitor.StatusDown, monitor.DomainResultPayload{
			MonitoringID: monitoring.ID,
			IsValid:      false,
			CheckedAt:    now,
		}, true
	}

	checkedAt := result.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}

	isValid := result.Registered && result.ExpiresAt != nil && result.ExpiresAt.After(checkedAt.Add(30*24*time.Hour))
	status := monitor.StatusDown
	if isValid {
		status = monitor.StatusUp
	}

	return status, monitor.DomainResultPayload{
		MonitoringID: monitoring.ID,
		IsValid:      isValid,
		ExpiresAt:    result.ExpiresAt,
		Registrar:    result.Registrar,
		CheckedAt:    checkedAt,
	}, true
}

func normalizeHeaders(rawHeaders any) map[string]string {
	result := make(map[string]string)

	switch value := rawHeaders.(type) {
	case nil:
		return result
	case string:
		if strings.TrimSpace(value) == "" {
			return result
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			return result
		}
		for key, raw := range parsed {
			result[key] = fmt.Sprintf("%v", raw)
		}
	case map[string]any:
		for key, raw := range value {
			result[key] = fmt.Sprintf("%v", raw)
		}
	case map[string]string:
		for key, raw := range value {
			result[key] = raw
		}
	}

	return result
}

func readLimitedBody(reader io.Reader, maxBytes int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxBytes)
	}
	return payload, nil
}

func normalizeBody(rawBody any) []byte {
	if rawBody == nil {
		return []byte("[]")
	}

	switch value := rawBody.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return []byte("[]")
		}
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return []byte("[]")
		}
		payload, err := json.Marshal(parsed)
		if err != nil {
			return []byte("[]")
		}
		return payload
	default:
		payload, err := json.Marshal(value)
		if err != nil {
			return []byte("[]")
		}
		return payload
	}
}

func roundMilliseconds(duration time.Duration) float64 {
	value := float64(duration.Microseconds()) / 1000
	return math.Round(value*100) / 100
}

func intPointer(value int) *int {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}

func stringPointer(value string) *string {
	return &value
}

func pointerFloat64Value(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func pointerIntValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
