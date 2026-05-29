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

	"github.com/marcel-breuer/webguard-instance/internal/config"
	"github.com/marcel-breuer/webguard-instance/internal/core"
	"github.com/marcel-breuer/webguard-instance/internal/domainlookup"
	"github.com/marcel-breuer/webguard-instance/internal/monitor"
	"github.com/marcel-breuer/webguard-instance/internal/target"
)

const fixedHTTPRetryTimes = 1
const fixedHTTPRetryDelay = 250 * time.Millisecond
const fixedHTTPMaxRedirects = 5
const fixedPingTimeoutSeconds = 5

var pingLatencyPattern = regexp.MustCompile(`time[=<]([0-9]+(?:\.[0-9]+)?)\s*ms`)

var pingExecutor = runPingCommand

var responseMonitoringTypes = []monitor.Type{
	monitor.TypeHTTP,
	monitor.TypePing,
	monitor.TypeKeyword,
	monitor.TypePort,
	monitor.TypeDNSRecord,
}

var sslMonitoringTypes = []monitor.Type{
	monitor.TypeHTTP,
	monitor.TypeKeyword,
	monitor.TypePort,
}

var domainExpirationMonitoringTypes = []monitor.Type{
	monitor.TypeDomainExpiration,
}

type CoreClient interface {
	GetMonitorings(ctx context.Context, location string, types []monitor.Type) ([]monitor.Monitoring, error)
	PostMonitoringResponse(ctx context.Context, payload monitor.MonitoringResponsePayload) error
	PostSSLResult(ctx context.Context, payload monitor.SSLResultPayload) error
	PostDomainResult(ctx context.Context, payload monitor.DomainResultPayload) error
}

type DomainLookup interface {
	Lookup(ctx context.Context, target string) (domainlookup.Result, error)
}

type Runner struct {
	client       CoreClient
	cfg          config.Config
	logger       *log.Logger
	domainLookup DomainLookup
	dnsChecker   *DNSRecordChecker
}

func New(client CoreClient, cfg config.Config, logger *log.Logger) *Runner {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Runner{
		client:       client,
		cfg:          cfg,
		logger:       logger,
		domainLookup: domainlookup.New(10 * time.Second),
		dnsChecker:   NewDNSRecordChecker(nil, logger),
	}
}

func (r *Runner) runResponse(ctx context.Context) error {
	r.logger.Println("Dispatching response monitoring jobs...")

	monitorings, err := r.client.GetMonitorings(ctx, r.cfg.WebGuardLocation, responseMonitoringTypes)
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
				status, responseTime, httpStatusCode := r.crawlResponseMonitoring(ctx, monitoring)
				r.logger.Printf(
					"Response monitoring result computed (monitoring_id=%s type=%s status=%s response_time=%v http_status_code=%v)",
					monitoring.ID,
					monitoring.Type,
					status,
					pointerFloat64Value(responseTime),
					pointerIntValue(httpStatusCode),
				)
				if err := r.client.PostMonitoringResponse(ctx, monitor.MonitoringResponsePayload{
					MonitoringID:   monitoring.ID,
					Status:         status,
					ResponseTime:   responseTime,
					HTTPStatusCode: httpStatusCode,
				}); err != nil {
					r.logger.Printf("Failed to post response result (monitoring_id=%s): %v", monitoring.ID, err)
				}
			}
		}()
	}

	for _, monitoring := range monitorings {
		if !supportsResponseChecks(monitoring.Type) {
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
			if err := r.client.PostMonitoringResponse(ctx, monitor.MonitoringResponsePayload{
				MonitoringID:   monitoring.ID,
				Status:         monitor.StatusUnknown,
				ResponseTime:   nil,
				HTTPStatusCode: nil,
			}); err != nil {
				r.logger.Printf("Failed to post maintenance response result (monitoring_id=%s): %v", monitoring.ID, err)
			}
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

func (r *Runner) runSSL(ctx context.Context) error {
	r.logger.Println("Dispatching SSL monitoring jobs...")

	monitorings, err := r.client.GetMonitorings(ctx, r.cfg.WebGuardLocation, sslMonitoringTypes)
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
				payload := r.crawlMonitoringSSL(monitoring)
				if err := r.client.PostSSLResult(ctx, payload); err != nil {
					r.logger.Printf("Failed to post SSL result (monitoring_id=%s): %v", monitoring.ID, err)
				}
			}
		}()
	}

	for _, monitoring := range monitorings {
		if !supportsSSLChecks(monitoring.Type) {
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

func (r *Runner) runDomainExpiration(ctx context.Context) error {
	r.logger.Println("Dispatching domain expiration monitoring jobs...")

	monitorings, err := r.client.GetMonitorings(ctx, r.cfg.WebGuardLocation, domainExpirationMonitoringTypes)
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
				status, domainPayload, hasDomainPayload := r.crawlDomainExpiration(ctx, monitoring)
				r.logger.Printf(
					"Domain expiration monitoring result computed (monitoring_id=%s status=%s)",
					monitoring.ID,
					status,
				)
				if err := r.client.PostMonitoringResponse(ctx, monitor.MonitoringResponsePayload{
					MonitoringID:   monitoring.ID,
					Status:         status,
					ResponseTime:   nil,
					HTTPStatusCode: nil,
				}); err != nil {
					r.logger.Printf("Failed to post domain expiration response result (monitoring_id=%s): %v", monitoring.ID, err)
				}
				if hasDomainPayload {
					if err := r.client.PostDomainResult(ctx, domainPayload); err != nil {
						r.logger.Printf("Failed to post domain expiration result (monitoring_id=%s): %v", monitoring.ID, err)
					}
				}
			}
		}()
	}

	for _, monitoring := range monitorings {
		if monitoring.Type != monitor.TypeDomainExpiration {
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
			if err := r.client.PostMonitoringResponse(ctx, monitor.MonitoringResponsePayload{
				MonitoringID:   monitoring.ID,
				Status:         monitor.StatusUnknown,
				ResponseTime:   nil,
				HTTPStatusCode: nil,
			}); err != nil {
				r.logger.Printf("Failed to post maintenance domain expiration response result (monitoring_id=%s): %v", monitoring.ID, err)
			}
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

func (r *Runner) RunMonitoring(ctx context.Context) error {
	r.logger.Println("Dispatching all monitoring jobs...")

	type phaseResult struct {
		name string
		err  error
	}

	results := make(chan phaseResult, 3)
	var phases sync.WaitGroup
	phases.Add(3)

	go func() {
		defer phases.Done()
		results <- phaseResult{name: "response", err: r.runResponse(ctx)}
	}()

	go func() {
		defer phases.Done()
		results <- phaseResult{name: "SSL", err: r.runSSL(ctx)}
	}()

	go func() {
		defer phases.Done()
		results <- phaseResult{name: "domain expiration", err: r.runDomainExpiration(ctx)}
	}()

	phases.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			r.logger.Printf("%s monitoring phase failed: %v", result.name, result.err)
		}
	}

	r.logger.Println("All monitoring jobs have been dispatched successfully.")
	return nil
}

func (r *Runner) logFetchError(err error) {
	r.logger.Println("Failed to fetch monitorings from the Core API.")

	var statusError *core.HTTPStatusError
	if errors.As(err, &statusError) && strings.TrimSpace(statusError.Body) != "" {
		r.logger.Println(statusError.Body)
	}
}

func (r *Runner) crawlResponseMonitoring(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64, *int) {
	switch monitoring.Type {
	case monitor.TypeHTTP:
		return r.handleHTTPMonitoring(ctx, monitoring)
	case monitor.TypePing:
		status, responseTime := handlePingMonitoring(monitoring)
		return status, responseTime, nil
	case monitor.TypeKeyword:
		return r.handleKeywordMonitoring(ctx, monitoring)
	case monitor.TypePort:
		status, responseTime := handlePortMonitoring(monitoring)
		return status, responseTime, nil
	case monitor.TypeDNSRecord:
		checker := r.dnsChecker
		if checker == nil {
			checker = NewDNSRecordChecker(nil, r.logger)
		}
		status, responseTime := checker.Check(ctx, monitoring)
		return status, responseTime, nil
	case monitor.TypeHeartbeat:
		return monitor.StatusUnknown, nil, nil
	default:
		return monitor.StatusUnknown, nil, nil
	}
}

func supportsResponseChecks(monitoringType monitor.Type) bool {
	switch monitoringType {
	case monitor.TypeHTTP, monitor.TypePing, monitor.TypeKeyword, monitor.TypePort, monitor.TypeDNSRecord:
		return true
	default:
		return false
	}
}

func supportsSSLChecks(monitoringType monitor.Type) bool {
	switch monitoringType {
	case monitor.TypeHTTP, monitor.TypeKeyword, monitor.TypePort:
		return true
	default:
		return false
	}
}

func (r *Runner) handleHTTPMonitoring(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64, *int) {
	start := time.Now()
	statusCode, _, err := r.performHTTPRequest(ctx, monitoring)
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

func (r *Runner) handleKeywordMonitoring(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64, *int) {
	start := time.Now()
	statusCode, body, err := r.performHTTPRequest(ctx, monitoring)
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

func handlePingMonitoring(monitoring monitor.Monitoring) (monitor.Status, *float64) {
	host, err := target.Host(monitoring.Target)
	if err != nil {
		return monitor.StatusDown, nil
	}

	timeoutSeconds := fixedPingTimeoutSeconds
	if monitoring.Timeout > 0 {
		timeoutSeconds = monitoring.Timeout
	}

	start := time.Now()
	output, err := pingExecutor(context.Background(), host, timeoutSeconds)
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

func handlePortMonitoring(monitoring monitor.Monitoring) (monitor.Status, *float64) {
	if monitoring.Port <= 0 {
		return monitor.StatusDown, nil
	}

	address, err := target.TCPAddress(monitoring.Target, monitoring.Port)
	if err != nil {
		return monitor.StatusDown, nil
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return monitor.StatusDown, nil
	}
	_ = conn.Close()

	responseTime := roundMilliseconds(time.Since(start))
	return monitor.StatusUp, &responseTime
}

func (r *Runner) performHTTPRequest(ctx context.Context, monitoring monitor.Monitoring) (int, string, error) {
	targetURL := strings.TrimSpace(monitoring.Target)
	if targetURL == "" {
		return 0, "", fmt.Errorf("monitoring target is empty")
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
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // Keep PHP compatibility (withoutVerifying)
			},
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= fixedHTTPMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", fixedHTTPMaxRedirects)
			}
			return nil
		},
	}
	if monitoring.Timeout > 0 {
		httpClient.Timeout = time.Duration(monitoring.Timeout) * time.Second
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

		request, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), targetURL, requestBody)
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

		payload, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			return 0, "", err
		}

		return response.StatusCode, string(payload), nil
	}

	return 0, "", lastErr
}

func (r *Runner) crawlMonitoringSSL(monitoring monitor.Monitoring) monitor.SSLResultPayload {
	payload := monitor.SSLResultPayload{
		MonitoringID: monitoring.ID,
		IsValid:      false,
	}

	address, serverName, err := target.SSLAddressAndServerName(monitoring.Target)
	if err != nil {
		return payload
	}

	connection, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", address, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true, //nolint:gosec // Needed to inspect certificate even when invalid.
	})
	if err != nil {
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

func (r *Runner) crawlDomainExpiration(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, monitor.DomainResultPayload, bool) {
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
