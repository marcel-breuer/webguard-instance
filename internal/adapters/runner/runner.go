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
	client       application.MonitoringGateway
	cfg          config.Config
	logger       *log.Logger
	domainLookup DomainLookup
	dnsChecker   *DNSRecordChecker
	pingExecutor PingCommandExecutor
	httpChecker  HTTPChecker
	executors    *executorRegistry
	telemetry    *application.Telemetry
}

func New(client application.MonitoringGateway, cfg config.Config, logger *log.Logger) *MonitoringService {
	return NewWithTelemetry(client, cfg, logger, application.NewTelemetry())
}

func NewWithTelemetry(client application.MonitoringGateway, cfg config.Config, logger *log.Logger, telemetry *application.Telemetry) *MonitoringService {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	if telemetry == nil {
		telemetry = application.NewTelemetry()
	}
	service := &MonitoringService{
		client:       client,
		cfg:          cfg,
		logger:       logger,
		domainLookup: domainlookup.New(10 * time.Second),
		dnsChecker:   NewDNSRecordChecker(nil, logger),
		pingExecutor: runPingCommand,
		telemetry:    telemetry,
	}
	service.httpChecker = HTTPCheckFunc(service.performHTTPRequest)
	service.executors = service.newExecutorRegistry()
	return service
}

func (r *MonitoringService) runResponse(ctx context.Context) error {
	r.logger.Println("Dispatching response monitoring jobs...")

	monitorings, err := r.client.GetMonitorings(ctx, r.cfg.WebGuardLocation, r.executors.Types(PhaseResponse))
	if err != nil {
		r.logFetchError(err)
		return err
	}

	if len(monitorings) == 0 {
		r.logger.Println("No active response monitoring found.")
		return nil
	}

	dispatched := 0
	skippedMaintenance := 0
	skippedUnsupported := 0

	jobs := make(chan monitor.Monitoring)
	var workers sync.WaitGroup

	workerCount := max(1, r.cfg.QueueDefaultWorkers)
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for monitoring := range jobs {
				release, err := application.AcquireExecutionSlot(ctx)
				if err != nil {
					r.logger.Printf("Response monitoring canceled before execution (monitoring_id=%s): %v", monitoring.ID, err)
					continue
				}
				execution, _ := r.executors.Execute(ctx, PhaseResponse, monitoring)
				release()
				r.logger.Printf("run_id=%s monitoring_id=%s phase=%s outcome=%s", application.RunID(ctx), monitoring.ID, PhaseResponse, executionOutcome(execution))
				r.logger.Printf(
					"Response monitoring result computed (monitoring_id=%s type=%s %s)",
					monitoring.ID,
					monitoring.Type,
					execution,
				)
				r.publishExecution(ctx, execution)
			}
		}()
	}

	for _, monitoring := range monitorings {
		if !r.executors.Supports(PhaseResponse, monitoring.Type) {
			skippedUnsupported++
			r.logger.Printf(
				"Skipping passive/unsupported response monitoring (monitoring_id=%s type=%s)",
				monitoring.ID,
				monitoring.Type,
			)
			continue
		}

		if monitoring.MaintenanceActive {
			skippedMaintenance++
			r.publishExecution(ctx, maintenanceExecution(PhaseResponse, monitoring.ID))
			continue
		}

		dispatched++
		jobs <- monitoring
	}
	close(jobs)
	workers.Wait()

	r.logger.Printf(
		"Response monitoring dispatch done. total=%d dispatched=%d skipped_maintenance=%d skipped_unsupported=%d",
		len(monitorings),
		dispatched,
		skippedMaintenance,
		skippedUnsupported,
	)

	return nil
}

func (r *MonitoringService) runSSL(ctx context.Context) error {
	r.logger.Println("Dispatching SSL monitoring jobs...")

	monitorings, err := r.client.GetMonitorings(ctx, r.cfg.WebGuardLocation, r.executors.Types(PhaseSSL))
	if err != nil {
		r.logFetchError(err)
		return err
	}

	if len(monitorings) == 0 {
		r.logger.Println("No active SSL monitoring found.")
		return nil
	}

	dispatched := 0
	skippedMaintenance := 0
	skippedUnsupported := 0

	jobs := make(chan monitor.Monitoring)
	var workers sync.WaitGroup

	workerCount := max(1, r.cfg.QueueDefaultWorkers)
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for monitoring := range jobs {
				release, err := application.AcquireExecutionSlot(ctx)
				if err != nil {
					r.logger.Printf("SSL monitoring canceled before execution (monitoring_id=%s): %v", monitoring.ID, err)
					continue
				}
				execution, _ := r.executors.Execute(ctx, PhaseSSL, monitoring)
				release()
				r.logger.Printf("run_id=%s monitoring_id=%s phase=%s outcome=%s", application.RunID(ctx), monitoring.ID, PhaseSSL, executionOutcome(execution))
				r.publishExecution(ctx, execution)
			}
		}()
	}

	for _, monitoring := range monitorings {
		if !r.executors.Supports(PhaseSSL, monitoring.Type) {
			skippedUnsupported++
			r.logger.Printf(
				"Skipping passive/unsupported SSL monitoring (monitoring_id=%s type=%s)",
				monitoring.ID,
				monitoring.Type,
			)
			continue
		}

		if monitoring.MaintenanceActive {
			skippedMaintenance++
			continue
		}
		dispatched++
		jobs <- monitoring
	}
	close(jobs)
	workers.Wait()

	r.logger.Printf(
		"SSL monitoring dispatch done. total=%d dispatched=%d skipped_maintenance=%d skipped_unsupported=%d",
		len(monitorings),
		dispatched,
		skippedMaintenance,
		skippedUnsupported,
	)

	return nil
}

func (r *MonitoringService) runDomainExpiration(ctx context.Context) error {
	r.logger.Println("Dispatching domain expiration monitoring jobs...")

	monitorings, err := r.client.GetMonitorings(ctx, r.cfg.WebGuardLocation, r.executors.Types(PhaseDomainExpiration))
	if err != nil {
		r.logFetchError(err)
		return err
	}

	if len(monitorings) == 0 {
		r.logger.Println("No active domain expiration monitoring found.")
		return nil
	}

	dispatched := 0
	skippedMaintenance := 0
	skippedUnsupported := 0

	jobs := make(chan monitor.Monitoring)
	var workers sync.WaitGroup

	workerCount := max(1, r.cfg.QueueDefaultWorkers)
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for monitoring := range jobs {
				release, err := application.AcquireExecutionSlot(ctx)
				if err != nil {
					r.logger.Printf("Domain expiration monitoring canceled before execution (monitoring_id=%s): %v", monitoring.ID, err)
					continue
				}
				execution, _ := r.executors.Execute(ctx, PhaseDomainExpiration, monitoring)
				release()
				r.logger.Printf("run_id=%s monitoring_id=%s phase=%s outcome=%s", application.RunID(ctx), monitoring.ID, PhaseDomainExpiration, executionOutcome(execution))
				r.logger.Printf(
					"Domain expiration monitoring result computed (monitoring_id=%s %s)",
					monitoring.ID,
					execution,
				)
				r.publishExecution(ctx, execution)
			}
		}()
	}

	for _, monitoring := range monitorings {
		if !r.executors.Supports(PhaseDomainExpiration, monitoring.Type) {
			skippedUnsupported++
			r.logger.Printf(
				"Skipping unsupported domain expiration monitoring (monitoring_id=%s type=%s)",
				monitoring.ID,
				monitoring.Type,
			)
			continue
		}

		if monitoring.MaintenanceActive {
			skippedMaintenance++
			r.publishExecution(ctx, maintenanceExecution(PhaseDomainExpiration, monitoring.ID))
			continue
		}

		dispatched++
		jobs <- monitoring
	}
	close(jobs)
	workers.Wait()

	r.logger.Printf(
		"Domain expiration monitoring dispatch done. total=%d dispatched=%d skipped_maintenance=%d skipped_unsupported=%d",
		len(monitorings),
		dispatched,
		skippedMaintenance,
		skippedUnsupported,
	)

	return nil
}

func (r *MonitoringService) RunMonitoring(ctx context.Context) error {
	r.logger.Println("Dispatching all monitoring jobs...")
	if r.cfg.JobLeasesEnabled {
		return r.runClaimedJobs(ctx)
	}

	return application.NewCoordinator(r.logger,
		monitoringPhase{name: "response", run: r.runResponse},
		monitoringPhase{name: "SSL", run: r.runSSL},
		monitoringPhase{name: "domain expiration", run: r.runDomainExpiration},
	).Run(ctx)
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

func (r *MonitoringService) logFetchError(err error) {
	r.logger.Println("Failed to fetch monitorings from the Core API.")

	var statusError *coreapi.HTTPStatusError
	if errors.As(err, &statusError) && strings.TrimSpace(statusError.Body) != "" {
		r.logger.Println(statusError.Body)
	}
}

func (r *MonitoringService) crawlResponseMonitoring(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64, *int) {
	execution, ok := r.executors.Execute(ctx, PhaseResponse, monitoring)
	if !ok || execution.Response == nil {
		return monitor.StatusUnknown, nil, nil
	}
	return execution.Response.Status, execution.Response.ResponseTime, execution.Response.HTTPStatusCode
}

func (r *MonitoringService) handleHTTPMonitoring(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64, *int) {
	start := time.Now()
	statusCode, _, err := r.httpCheck(ctx, monitoring)
	if err != nil {
		return monitor.StatusDown, nil, nil
	}
	httpStatusCode := intPointer(statusCode)
	if statusCode >= http.StatusOK && statusCode < http.StatusBadRequest {
		responseTime := roundMilliseconds(time.Since(start))
		return monitor.StatusUp, &responseTime, httpStatusCode
	}
	return monitor.StatusDown, nil, httpStatusCode
}

func (r *MonitoringService) handleKeywordMonitoring(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64, *int) {
	start := time.Now()
	statusCode, body, err := r.httpCheck(ctx, monitoring)
	if err != nil {
		return monitor.StatusDown, nil, nil
	}
	httpStatusCode := intPointer(statusCode)
	if strings.Contains(body, monitoring.Keyword) {
		responseTime := roundMilliseconds(time.Since(start))
		return monitor.StatusUp, &responseTime, httpStatusCode
	}
	return monitor.StatusDown, nil, httpStatusCode
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

func handlePingMonitoring(ctx context.Context, monitoring monitor.Monitoring, policy target.EgressPolicy, executor PingCommandExecutor) (monitor.Status, *float64) {
	host, err := target.Host(monitoring.Target)
	if err != nil {
		return monitor.StatusDown, nil
	}
	if err := target.ValidateHost(ctx, host, policy); err != nil {
		return monitor.StatusDown, nil
	}

	timeoutSeconds := fixedPingTimeoutSeconds
	if monitoring.Timeout > 0 {
		timeoutSeconds = monitoring.Timeout
	}

	start := time.Now()
	if executor == nil {
		executor = runPingCommand
	}
	output, err := executor(ctx, host, timeoutSeconds)
	responseTime := parsePingLatency(output)
	if responseTime == nil {
		elapsed := roundMilliseconds(time.Since(start))
		responseTime = &elapsed
	}
	if err != nil {
		return monitor.StatusDown, responseTime
	}

	return monitor.StatusUp, responseTime
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

func handlePortMonitoring(ctx context.Context, monitoring monitor.Monitoring, policy target.EgressPolicy) (monitor.Status, *float64) {
	if monitoring.Port <= 0 {
		return monitor.StatusDown, nil
	}

	address, err := target.TCPAddress(monitoring.Target, monitoring.Port)
	if err != nil {
		return monitor.StatusDown, nil
	}

	start := time.Now()
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := target.SafeDialContext(policy)(dialCtx, "tcp", address)
	if err != nil {
		return monitor.StatusDown, nil
	}
	_ = conn.Close()

	responseTime := roundMilliseconds(time.Since(start))
	return monitor.StatusUp, &responseTime
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

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		var requestBody io.Reader
		if len(body) > 0 {
			requestBody = bytes.NewReader(body)
		}

		request, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), targetURL.String(), requestBody)
		if err != nil {
			return 0, "", err
		}

		for key, value := range headers {
			request.Header.Set(key, value)
		}
		if monitoring.AuthUsername != "" && monitoring.AuthPassword != "" {
			request.SetBasicAuth(monitoring.AuthUsername, monitoring.AuthPassword)
		}

		response, err := httpClient.Do(request)
		if err != nil {
			lastErr = err
			if attempt < attempts-1 {
				time.Sleep(delay)
				continue
			}
			return 0, "", lastErr
		}

		payload, err := readLimitedBody(response.Body, fixedHTTPMaxResponseBytes)
		_ = response.Body.Close()
		if err != nil {
			return 0, "", err
		}

		return response.StatusCode, string(payload), nil
	}

	return 0, "", lastErr
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
