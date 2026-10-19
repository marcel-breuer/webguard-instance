package runner

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/config"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

func newTestService(client application.MonitoringGateway, cfg config.Config, logger *log.Logger) *MonitoringService {
	return NewWithTelemetry(client, cfg, logger, application.NewTelemetry())
}

type getMonitoringsCall struct {
	location string
	types    []monitor.Type
}

type fakeCoreClient struct {
	mu sync.Mutex

	responseMonitorings []monitor.Monitoring
	sslMonitorings      []monitor.Monitoring
	domainMonitorings   []monitor.Monitoring

	calls []getMonitoringsCall

	postedResponses []monitor.MonitoringResponsePayload
	postedSSL       []monitor.SSLResultPayload
	postedDomains   []monitor.DomainResultPayload
	claimedJobs     []monitor.ClaimedJob
	claimRequests   []monitor.ClaimMonitoringJobsRequest
	completedJobs   []completedJob
	releasedJobs    []releasedJob
	extend          func(context.Context, string, monitor.ExtendMonitoringJobRequest) (monitor.ExtendMonitoringJobResponse, error)
}

type completedJob struct {
	jobID          string
	idempotencyKey string
	request        monitor.CompleteMonitoringJobRequest
}

type releasedJob struct {
	jobID   string
	request monitor.ReleaseMonitoringJobRequest
}

func (f *fakeCoreClient) GetMonitorings(_ context.Context, location string, types []monitor.Type) ([]monitor.Monitoring, error) {
	f.mu.Lock()
	f.calls = append(f.calls, getMonitoringsCall{
		location: location,
		types:    append([]monitor.Type(nil), types...),
	})
	f.mu.Unlock()

	if slices.Contains(types, monitor.TypePing) || slices.Contains(types, monitor.TypeDNSRecord) {
		return append([]monitor.Monitoring(nil), f.responseMonitorings...), nil
	}
	if slices.Contains(types, monitor.TypeDomainExpiration) {
		return append([]monitor.Monitoring(nil), f.domainMonitorings...), nil
	}

	return append([]monitor.Monitoring(nil), f.sslMonitorings...), nil
}

func (f *fakeCoreClient) PostMonitoringResponse(_ context.Context, payload monitor.MonitoringResponsePayload) error {
	f.mu.Lock()
	f.postedResponses = append(f.postedResponses, payload)
	f.mu.Unlock()
	return nil
}

func (f *fakeCoreClient) PostSSLResult(_ context.Context, payload monitor.SSLResultPayload) error {
	f.mu.Lock()
	f.postedSSL = append(f.postedSSL, payload)
	f.mu.Unlock()
	return nil
}

func (f *fakeCoreClient) PostDomainResult(_ context.Context, payload monitor.DomainResultPayload) error {
	f.mu.Lock()
	f.postedDomains = append(f.postedDomains, payload)
	f.mu.Unlock()
	return nil
}

func (f *fakeCoreClient) ClaimMonitoringJobs(_ context.Context, request monitor.ClaimMonitoringJobsRequest) ([]monitor.ClaimedJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimRequests = append(f.claimRequests, request)
	return append([]monitor.ClaimedJob(nil), f.claimedJobs...), nil
}

func (f *fakeCoreClient) CompleteMonitoringJob(_ context.Context, jobID, idempotencyKey string, request monitor.CompleteMonitoringJobRequest) error {
	f.mu.Lock()
	f.completedJobs = append(f.completedJobs, completedJob{jobID: jobID, idempotencyKey: idempotencyKey, request: request})
	f.mu.Unlock()
	return nil
}

func (f *fakeCoreClient) ReleaseMonitoringJob(_ context.Context, jobID string, request monitor.ReleaseMonitoringJobRequest) error {
	f.mu.Lock()
	f.releasedJobs = append(f.releasedJobs, releasedJob{jobID: jobID, request: request})
	f.mu.Unlock()
	return nil
}

func (f *fakeCoreClient) ExtendMonitoringJob(ctx context.Context, jobID string, request monitor.ExtendMonitoringJobRequest) (monitor.ExtendMonitoringJobResponse, error) {
	if f.extend != nil {
		return f.extend(ctx, jobID, request)
	}
	return monitor.ExtendMonitoringJobResponse{}, nil
}

func (f *fakeCoreClient) snapshotCalls() []getMonitoringsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]getMonitoringsCall(nil), f.calls...)
}

func (f *fakeCoreClient) snapshotPostedResponses() []monitor.MonitoringResponsePayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]monitor.MonitoringResponsePayload(nil), f.postedResponses...)
}

func (f *fakeCoreClient) snapshotPostedSSL() []monitor.SSLResultPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]monitor.SSLResultPayload(nil), f.postedSSL...)
}

func (f *fakeCoreClient) snapshotPostedDomains() []monitor.DomainResultPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]monitor.DomainResultPayload(nil), f.postedDomains...)
}

func (f *fakeCoreClient) snapshotClaimRequests() []monitor.ClaimMonitoringJobsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]monitor.ClaimMonitoringJobsRequest(nil), f.claimRequests...)
}

func (f *fakeCoreClient) snapshotCompletedJobs() []completedJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]completedJob(nil), f.completedJobs...)
}

func (f *fakeCoreClient) snapshotReleasedJobs() []releasedJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]releasedJob(nil), f.releasedJobs...)
}

func TestPublishExecutionUsesOneUUIDv4ForOneExecution(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{}
	runner := newTestService(client, config.Config{}, log.New(io.Discard, "", 0))
	runner.publishExecution(context.Background(), Execution{
		Response: &monitor.MonitoringResponsePayload{MonitoringID: "response-1", Status: monitor.StatusUp},
		Domain:   &monitor.DomainResultPayload{MonitoringID: "domain-1", IsValid: true},
	})

	responses := client.snapshotPostedResponses()
	domains := client.snapshotPostedDomains()
	if len(responses) != 1 || len(domains) != 1 {
		t.Fatalf("expected one response and domain callback, got responses=%d domains=%d", len(responses), len(domains))
	}
	if !isUUIDv4(responses[0].IdempotencyKey) || responses[0].IdempotencyKey != domains[0].IdempotencyKey {
		t.Fatalf("expected one shared UUIDv4, got response=%q domain=%q", responses[0].IdempotencyKey, domains[0].IdempotencyKey)
	}

	runner.publishExecution(context.Background(), Execution{
		Response: &monitor.MonitoringResponsePayload{MonitoringID: "response-2", Status: monitor.StatusUp},
	})
	secondResponses := client.snapshotPostedResponses()
	if len(secondResponses) != 2 || secondResponses[0].IdempotencyKey == secondResponses[1].IdempotencyKey {
		t.Fatalf("expected independent executions to use different keys: %#v", secondResponses)
	}
}

func TestRunMonitoringClaimsMixedJobsAndCompletesWithIdempotencyKey(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{claimedJobs: []monitor.ClaimedJob{
		{
			ID:             "job-response",
			Phase:          string(PhaseResponse),
			LeaseExpiresAt: time.Now().Add(time.Minute),
			Attempt:        2,
			IdempotencyKey: "6f3a8bb2-8a66-4c84-91db-5166c9c291c5",
			Monitoring: monitor.Monitoring{
				ID: "http-1", Type: monitor.TypeHTTP, Target: "https://example.com",
			},
		},
		{
			ID:             "job-invalid",
			Phase:          string(PhaseSSL),
			Attempt:        1,
			IdempotencyKey: "invalid-key",
			Monitoring: monitor.Monitoring{
				ID: "domain-1", Type: monitor.TypeDomainExpiration,
			},
		},
		{
			ID:             "job-expired",
			Phase:          string(PhaseResponse),
			Attempt:        4,
			IdempotencyKey: "39d4e4b3-2b2e-446c-9f79-58f6c7e35099",
			LeaseExpiresAt: time.Now().Add(-time.Minute),
			Monitoring: monitor.Monitoring{
				ID: "http-expired", Type: monitor.TypeHTTP, Target: "https://example.com",
			},
		},
	}}
	runner := newTestService(client, config.Config{
		WebGuardLocation:    "de-1",
		WebGuardInstanceID:  "worker-de-1-a",
		JobLeasesEnabled:    true,
		JobLeaseMaxBatch:    4,
		QueueDefaultWorkers: 2,
		AllowPrivateTargets: true,
		JobLeasesDualWrite:  true,
	}, log.New(io.Discard, "", 0))
	runner.httpChecker = HTTPCheckFunc(func(context.Context, monitor.Monitoring) (int, string, error) {
		return http.StatusOK, "", nil
	})

	if err := runner.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("RunMonitoring failed: %v", err)
	}

	claims := client.snapshotClaimRequests()
	if len(claims) != 1 {
		t.Fatalf("expected one claim request, got %d", len(claims))
	}
	if claims[0].Location != "de-1" || claims[0].InstanceID != "worker-de-1-a" {
		t.Fatalf("unexpected claim identity: %#v", claims[0])
	}
	if claims[0].Capacity != 2 || claims[0].MaxBatchSize != 2 {
		t.Fatalf("unexpected claim capacity: %#v", claims[0])
	}
	if !slices.Equal(claims[0].Capabilities, []string{"response", "ssl", "domain_expiration"}) {
		t.Fatalf("unexpected claim capabilities: %#v", claims[0].Capabilities)
	}

	completed := client.snapshotCompletedJobs()
	if len(completed) != 1 {
		t.Fatalf("expected one completed job, got %d", len(completed))
	}
	if completed[0].jobID != "job-response" || completed[0].idempotencyKey != "6f3a8bb2-8a66-4c84-91db-5166c9c291c5" || completed[0].request.Attempt != 2 {
		t.Fatalf("unexpected completion: %#v", completed[0])
	}
	postedResponses := client.snapshotPostedResponses()
	if len(postedResponses) != 1 || postedResponses[0].IdempotencyKey != "6f3a8bb2-8a66-4c84-91db-5166c9c291c5" {
		t.Fatalf("expected callback to reuse the claimed execution key, got %#v", postedResponses)
	}
	if completed[0].request.Result.Response == nil || completed[0].request.Result.Response.MonitoringID != "http-1" {
		t.Fatalf("expected response result, got %#v", completed[0].request.Result)
	}

	released := client.snapshotReleasedJobs()
	if len(released) != 2 {
		t.Fatalf("unexpected release: %#v", released)
	}
	releasedReasons := make(map[string]string, len(released))
	for _, job := range released {
		releasedReasons[job.jobID] = job.request.Reason
	}
	if releasedReasons["job-invalid"] != "unsupported monitoring job" || releasedReasons["job-expired"] != "lease expired before execution" {
		t.Fatalf("unexpected release reasons: %#v", releasedReasons)
	}
	if len(client.snapshotCalls()) != 0 {
		t.Fatalf("expected no legacy monitoring fetches when job leases are enabled")
	}
}

func TestRunMonitoringClaimCapacityRespectsGlobalConcurrency(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{}
	runner := newTestService(client, config.Config{
		WebGuardLocation: "de-1", WebGuardInstanceID: "worker-a", JobLeasesEnabled: true,
		QueueDefaultWorkers: 8, RunMaxConcurrency: 2, JobLeaseMaxBatch: 8,
	}, log.New(io.Discard, "", 0))
	if err := runner.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("RunMonitoring failed: %v", err)
	}
	claims := client.snapshotClaimRequests()
	if len(claims) != 1 || claims[0].Capacity != 2 || claims[0].MaxBatchSize != 2 {
		t.Fatalf("expected claim pressure to respect the global concurrency limit, got %#v", claims)
	}
}

func TestRunMonitoringDeduplicatesClaimedJobsAndUsesCoreIdempotencyKey(t *testing.T) {
	t.Parallel()

	job := monitor.ClaimedJob{
		ID:             "job-stable-idempotency",
		Phase:          string(PhaseResponse),
		LeaseExpiresAt: time.Now().Add(time.Minute),
		Attempt:        1,
		IdempotencyKey: "6f3a8bb2-8a66-4c84-91db-5166c9c291c5",
		Monitoring: monitor.Monitoring{
			ID: "monitor-stable-idempotency", Type: monitor.TypeHTTP, Target: "https://example.com",
		},
	}
	client := &fakeCoreClient{claimedJobs: []monitor.ClaimedJob{job, job}}
	runner := newTestService(client, config.Config{
		WebGuardLocation: "de-1", WebGuardInstanceID: "worker-a", JobLeasesEnabled: true,
		JobLeaseMaxBatch: 2, QueueDefaultWorkers: 2, JobLeasesDualWrite: true,
	}, log.New(io.Discard, "", 0))
	runner.httpChecker = HTTPCheckFunc(func(context.Context, monitor.Monitoring) (int, string, error) {
		return http.StatusOK, "", nil
	})

	if err := runner.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("RunMonitoring failed: %v", err)
	}
	completed := client.snapshotCompletedJobs()
	posted := client.snapshotPostedResponses()
	if len(completed) != 1 || len(posted) != 1 {
		t.Fatalf("duplicate lease delivery should execute and report once, got completed=%d posted=%d", len(completed), len(posted))
	}
	if completed[0].idempotencyKey != job.IdempotencyKey || posted[0].IdempotencyKey != job.IdempotencyKey {
		t.Fatalf("expected callback and completion to reuse the Core-issued key %q, got completion=%q callback=%q", job.IdempotencyKey, completed[0].idempotencyKey, posted[0].IdempotencyKey)
	}
	if runner.telemetry.Snapshot().Leases["duplicate"] != 1 {
		t.Fatalf("expected duplicate delivery to be observable, got %#v", runner.telemetry.Snapshot().Leases)
	}
}

func TestRunMonitoringRenewsLeaseAndReleasesWhenCanceled(t *testing.T) {
	job := monitor.ClaimedJob{
		ID: "job-renewal", Phase: string(PhaseResponse), LeaseExpiresAt: time.Now().Add(time.Second), Attempt: 1,
		IdempotencyKey: "39d4e4b3-2b2e-446c-9f79-58f6c7e35099",
		Monitoring:     monitor.Monitoring{ID: "monitor-renewal", Type: monitor.TypeHTTP, Target: "https://example.com"},
	}
	extended := make(chan struct{}, 1)
	client := &fakeCoreClient{claimedJobs: []monitor.ClaimedJob{job}}
	client.extend = func(ctx context.Context, jobID string, request monitor.ExtendMonitoringJobRequest) (monitor.ExtendMonitoringJobResponse, error) {
		if jobID != job.ID || request.Attempt != job.Attempt {
			t.Errorf("unexpected lease extension request: job=%q attempt=%d", jobID, request.Attempt)
		}
		select {
		case extended <- struct{}{}:
		default:
		}
		return monitor.ExtendMonitoringJobResponse{LeaseExpiresAt: time.Now().Add(time.Minute)}, nil
	}
	runner := newTestService(client, config.Config{
		WebGuardLocation: "de-1", WebGuardInstanceID: "worker-a", JobLeasesEnabled: true,
		JobLeaseMaxBatch: 1, QueueDefaultWorkers: 1,
	}, log.New(io.Discard, "", 0))
	checkStarted := make(chan struct{}, 1)
	runner.httpChecker = HTTPCheckFunc(func(ctx context.Context, _ monitor.Monitoring) (int, string, error) {
		checkStarted <- struct{}{}
		<-ctx.Done()
		return 0, "", ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.RunMonitoring(ctx) }()
	select {
	case <-checkStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("monitoring check did not start")
	}
	select {
	case <-extended:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("lease was not renewed before its deadline")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunMonitoring failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("monitoring did not stop after cancellation")
	}
	if len(client.snapshotCompletedJobs()) != 0 {
		t.Fatal("canceled check must not report a completion")
	}
	released := client.snapshotReleasedJobs()
	if len(released) != 1 || released[0].jobID != job.ID {
		t.Fatalf("expected canceled lease to be released for retry, got %#v", released)
	}
	if runner.telemetry.Snapshot().Leases["extended"] == 0 {
		t.Fatal("expected lease renewal to be observable")
	}
}

func TestRunMonitoringReleasesInvalidClaimedPayloads(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{claimedJobs: []monitor.ClaimedJob{{
		ID: "job-invalid-target", Phase: string(PhaseResponse), LeaseExpiresAt: time.Now().Add(time.Minute), Attempt: 1,
		IdempotencyKey: "39d4e4b3-2b2e-446c-9f79-58f6c7e35099",
		Monitoring:     monitor.Monitoring{ID: "monitor-invalid-target", Type: monitor.TypeHTTP},
	}, {
		ID: "job-invalid-idempotency", Phase: string(PhaseResponse), LeaseExpiresAt: time.Now().Add(time.Minute), Attempt: 1,
		IdempotencyKey: "not-a-uuid",
		Monitoring:     monitor.Monitoring{ID: "monitor-invalid-idempotency", Type: monitor.TypeHTTP, Target: "https://example.com"},
	}}}
	runner := newTestService(client, config.Config{
		WebGuardLocation: "de-1", WebGuardInstanceID: "worker-a", JobLeasesEnabled: true,
		JobLeaseMaxBatch: 2, QueueDefaultWorkers: 2,
	}, log.New(io.Discard, "", 0))

	if err := runner.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("RunMonitoring failed: %v", err)
	}
	released := client.snapshotReleasedJobs()
	reasons := make(map[string]string, len(released))
	for _, job := range released {
		reasons[job.jobID] = job.request.Reason
	}
	if len(reasons) != 2 || reasons["job-invalid-target"] != "monitoring target is empty" || reasons["job-invalid-idempotency"] != "job idempotency key is invalid" {
		t.Fatalf("expected invalid payload to be released, got %#v", released)
	}
	if len(client.snapshotCompletedJobs()) != 0 {
		t.Fatal("invalid payload must not be executed or completed")
	}
}

func TestPerformHTTPRequestRejectsOversizedConfiguredRequests(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount++
	}))
	defer server.Close()

	runner := newTestService(nil, config.Config{AllowPrivateTargets: true}, log.New(io.Discard, "", 0))
	tests := []struct {
		name       string
		body       any
		headers    any
		wantReason string
	}{
		{
			name:       "body limit",
			body:       `"` + strings.Repeat("x", fixedHTTPMaxRequestBodyBytes+1) + `"`,
			wantReason: "request body exceeds",
		},
		{
			name:       "header count limit",
			headers:    oversizedHeaderSet(),
			wantReason: "request headers exceed",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := runner.performHTTPRequest(context.Background(), monitor.Monitoring{
				ID: "request-limit", Type: monitor.TypeHTTP, Target: server.URL,
				HTTPMethod: monitor.HTTPMethodPost, HTTPBody: testCase.body, HTTPHeaders: testCase.headers,
			})
			if err == nil || !strings.Contains(err.Error(), testCase.wantReason) {
				t.Fatalf("expected %q error, got %v", testCase.wantReason, err)
			}
		})
	}
	if requestCount != 0 {
		t.Fatalf("oversized configured requests must be rejected before dialing, got %d requests", requestCount)
	}
}

func TestRunMonitoringBoundsConcurrentChecksAcrossConfiguredWorkers(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{responseMonitorings: []monitor.Monitoring{
		{ID: "http-1", Type: monitor.TypeHTTP, Target: "https://example.com"},
		{ID: "http-2", Type: monitor.TypeHTTP, Target: "https://example.com"},
		{ID: "http-3", Type: monitor.TypeHTTP, Target: "https://example.com"},
		{ID: "http-4", Type: monitor.TypeHTTP, Target: "https://example.com"},
	}}
	telemetry := application.NewTelemetry()
	runner := NewWithTelemetry(client, config.Config{
		WebGuardLocation: "de-1", QueueDefaultWorkers: 4, RunMaxConcurrency: 2,
	}, log.New(io.Discard, "", 0), telemetry)
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	runner.httpChecker = HTTPCheckFunc(func(ctx context.Context, monitoring monitor.Monitoring) (int, string, error) {
		current := active.Add(1)
		for previous := maximum.Load(); current > previous; previous = maximum.Load() {
			if maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-ctx.Done():
			active.Add(-1)
			return 0, "", ctx.Err()
		case <-release:
			active.Add(-1)
			return http.StatusOK, monitoring.ID, nil
		}
	})
	controller := application.NewExecutionControllerWithTelemetry(runner, log.New(io.Discard, "", 0), 2, telemetry)
	done := make(chan error, 1)
	go func() { done <- controller.RunMonitoring(context.Background()) }()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("expected two checks to start under the configured concurrency limit")
		}
	}
	if active.Load() != 2 {
		close(release)
		t.Fatalf("expected two active checks, got %d", active.Load())
	}
	select {
	case <-started:
		close(release)
		t.Fatal("a third check started while the shared execution budget was full")
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("monitoring run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("monitoring run did not finish after releasing checks")
	}
	if maximum.Load() > 2 {
		t.Fatalf("expected maximum concurrency 2, got %d", maximum.Load())
	}
}

func oversizedHeaderSet() map[string]string {
	headers := make(map[string]string, fixedHTTPMaxRequestHeaders+1)
	for i := 0; i <= fixedHTTPMaxRequestHeaders; i++ {
		headers[strings.Repeat("X", i+1)] = "value"
	}
	return headers
}

func TestRunMonitoringMaintenancePostsUnknown(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{
		responseMonitorings: []monitor.Monitoring{
			{
				ID:                "7",
				Type:              monitor.TypeHTTP,
				MaintenanceActive: true,
			},
		},
		sslMonitorings: []monitor.Monitoring{},
	}

	cfg := config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
		AllowPrivateTargets: true,
	}
	runner := newTestService(client, cfg, log.New(io.Discard, "", 0))

	if err := runner.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("RunMonitoring failed: %v", err)
	}

	calls := client.snapshotCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 monitoring fetch calls, got %d", len(calls))
	}

	var foundResponseFetch bool
	var foundSSLFetch bool
	var foundDomainFetch bool
	for _, call := range calls {
		if call.location != "de-1" {
			t.Fatalf("expected location de-1, got %q", call.location)
		}

		if len(call.types) == 5 &&
			call.types[0] == monitor.TypeHTTP &&
			call.types[1] == monitor.TypePing &&
			call.types[2] == monitor.TypeKeyword &&
			call.types[3] == monitor.TypePort &&
			call.types[4] == monitor.TypeDNSRecord {
			foundResponseFetch = true
			continue
		}

		if len(call.types) == 3 &&
			call.types[0] == monitor.TypeHTTP &&
			call.types[1] == monitor.TypeKeyword &&
			call.types[2] == monitor.TypePort {
			foundSSLFetch = true
			continue
		}

		if len(call.types) == 1 && call.types[0] == monitor.TypeDomainExpiration {
			foundDomainFetch = true
			continue
		}

		t.Fatalf("unexpected type filter: %#v", call.types)
	}

	if !foundResponseFetch {
		t.Fatalf("response fetch call missing")
	}
	if !foundSSLFetch {
		t.Fatalf("ssl fetch call missing")
	}
	if !foundDomainFetch {
		t.Fatalf("domain expiration fetch call missing")
	}

	postedResponses := client.snapshotPostedResponses()
	if len(postedResponses) != 1 {
		t.Fatalf("expected 1 posted response, got %d", len(postedResponses))
	}
	payload := postedResponses[0]
	if payload.MonitoringID != "7" {
		t.Fatalf("expected monitoring_id 7, got %s", payload.MonitoringID)
	}
	if payload.Status != monitor.StatusUnknown {
		t.Fatalf("expected unknown status, got %s", payload.Status)
	}
	if payload.ResponseTime != nil {
		t.Fatalf("expected nil response_time, got %v", *payload.ResponseTime)
	}
	if payload.HTTPStatusCode != nil {
		t.Fatalf("expected nil http_status_code, got %v", *payload.HTTPStatusCode)
	}
}

func TestRunMonitoringRequestsNonPingTypesForSSL(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{
		responseMonitorings: []monitor.Monitoring{},
		sslMonitorings:      []monitor.Monitoring{},
	}
	cfg := config.Config{
		WebGuardLocation:    "us-1",
		QueueDefaultWorkers: 1,
	}
	runner := newTestService(client, cfg, log.New(io.Discard, "", 0))

	if err := runner.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("RunMonitoring failed: %v", err)
	}

	calls := client.snapshotCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 monitoring fetch calls, got %d", len(calls))
	}

	var foundSSLFetch bool
	for _, call := range calls {
		if call.location != "us-1" {
			t.Fatalf("expected location us-1, got %q", call.location)
		}
		if len(call.types) == 5 &&
			call.types[0] == monitor.TypeHTTP &&
			call.types[1] == monitor.TypePing &&
			call.types[2] == monitor.TypeKeyword &&
			call.types[3] == monitor.TypePort &&
			call.types[4] == monitor.TypeDNSRecord {
			continue
		}
		if len(call.types) == 3 &&
			call.types[0] == monitor.TypeHTTP &&
			call.types[1] == monitor.TypeKeyword &&
			call.types[2] == monitor.TypePort {
			foundSSLFetch = true
			continue
		}
		if len(call.types) == 1 && call.types[0] == monitor.TypeDomainExpiration {
			continue
		}

		t.Fatalf("unexpected type filter: %#v", call.types)
	}

	if !foundSSLFetch {
		t.Fatalf("ssl types fetch missing")
	}
}

func TestRunMonitoringSkipsHeartbeatMonitoringsWithoutPostingResults(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{
		responseMonitorings: []monitor.Monitoring{
			{
				ID:   "hb-response",
				Type: monitor.TypeHeartbeat,
			},
		},
		sslMonitorings: []monitor.Monitoring{
			{
				ID:   "hb-ssl",
				Type: monitor.TypeHeartbeat,
			},
		},
	}

	var logs bytes.Buffer
	cfg := config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
		AllowPrivateTargets: true,
	}
	runner := newTestService(client, cfg, log.New(&logs, "", 0))

	if err := runner.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("RunMonitoring failed: %v", err)
	}

	if postedResponses := client.snapshotPostedResponses(); len(postedResponses) != 0 {
		t.Fatalf("expected no posted response payloads, got %d", len(postedResponses))
	}
	if postedSSL := client.snapshotPostedSSL(); len(postedSSL) != 0 {
		t.Fatalf("expected no posted ssl payloads, got %d", len(postedSSL))
	}

	logOutput := logs.String()
	if !strings.Contains(logOutput, "Skipping passive/unsupported response monitoring (monitoring_id=hb-response type=heartbeat)") {
		t.Fatalf("expected response skip log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "Skipping passive/unsupported SSL monitoring (monitoring_id=hb-ssl type=heartbeat)") {
		t.Fatalf("expected ssl skip log, got %q", logOutput)
	}
}

func TestRunResponsePostsHTTPStatusCodeForHTTPAndKeywordMonitoring(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("needle"))
	}))
	defer server.Close()

	client := &fakeCoreClient{
		responseMonitorings: []monitor.Monitoring{
			{
				ID:         "http-monitoring",
				Type:       monitor.TypeHTTP,
				Target:     server.URL,
				Timeout:    2,
				HTTPMethod: monitor.HTTPMethodGet,
			},
			{
				ID:         "keyword-monitoring",
				Type:       monitor.TypeKeyword,
				Target:     server.URL,
				Timeout:    2,
				HTTPMethod: monitor.HTTPMethodGet,
				Keyword:    "needle",
			},
		},
	}

	cfg := config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
		AllowPrivateTargets: true,
	}
	runner := newTestService(client, cfg, log.New(io.Discard, "", 0))

	if err := runner.runResponse(context.Background()); err != nil {
		t.Fatalf("runResponse failed: %v", err)
	}

	postedResponses := client.snapshotPostedResponses()
	if len(postedResponses) != 2 {
		t.Fatalf("expected 2 posted responses, got %d", len(postedResponses))
	}

	payloadByID := make(map[string]monitor.MonitoringResponsePayload, len(postedResponses))
	for _, payload := range postedResponses {
		payloadByID[payload.MonitoringID] = payload
	}

	for _, monitoringID := range []string{"http-monitoring", "keyword-monitoring"} {
		payload, ok := payloadByID[monitoringID]
		if !ok {
			t.Fatalf("expected payload for %s", monitoringID)
		}
		if payload.HTTPStatusCode == nil {
			t.Fatalf("expected http_status_code for %s", monitoringID)
		}
		if *payload.HTTPStatusCode != http.StatusCreated {
			t.Fatalf("expected http_status_code=%d for %s, got %d", http.StatusCreated, monitoringID, *payload.HTTPStatusCode)
		}
		if payload.Observation == nil {
			t.Fatalf("expected raw observation for %s", monitoringID)
		}
		if payload.Observation.Type != payloadType(monitoringID) {
			t.Fatalf("expected observation type %s for %s, got %s", payloadType(monitoringID), monitoringID, payload.Observation.Type)
		}
		if payload.Observation.HTTPStatusCode == nil || *payload.Observation.HTTPStatusCode != http.StatusCreated {
			t.Fatalf("expected raw HTTP status code for %s", monitoringID)
		}
		if payload.Observation.ResponseTime == nil {
			t.Fatalf("expected raw response time for %s", monitoringID)
		}
	}
}

func TestRunResponseHonorsWebsiteCheckCadenceAndReportsTheExecutedInterval(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{responseMonitorings: []monitor.Monitoring{
		{ID: "website-monitoring", Type: monitor.TypeHTTP, Target: "https://example.com", CheckIntervalSeconds: 900},
		{ID: "ping-monitoring", Type: monitor.TypePing, Target: "127.0.0.1", CheckIntervalSeconds: 300},
	}}
	runner := newTestService(client, config.Config{WebGuardLocation: "de-1", QueueDefaultWorkers: 1, AllowPrivateTargets: true}, log.New(io.Discard, "", 0))
	runner.httpChecker = HTTPCheckFunc(func(context.Context, monitor.Monitoring) (int, string, error) {
		return http.StatusOK, "", nil
	})
	runner.pingExecutor = func(context.Context, string, int) ([]byte, error) {
		return []byte("time=1 ms"), nil
	}

	website := client.responseMonitorings[0]
	firstRun := nextDueResponseWindow(website, "de-1", time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
	runner.now = func() time.Time { return firstRun }
	if err := runner.runResponse(context.Background()); err != nil {
		t.Fatalf("first run failed: %v", err)
	}

	runner.now = func() time.Time { return firstRun.Add(5 * time.Minute) }
	if err := runner.runResponse(context.Background()); err != nil {
		t.Fatalf("second run failed: %v", err)
	}

	runner.now = func() time.Time { return firstRun.Add(15 * time.Minute) }
	if err := runner.runResponse(context.Background()); err != nil {
		t.Fatalf("third run failed: %v", err)
	}

	responses := client.snapshotPostedResponses()
	if len(responses) != 5 {
		t.Fatalf("expected 5 responses (website twice, ping three times), got %d", len(responses))
	}

	websiteChecks := 0
	pingChecks := 0
	for _, response := range responses {
		switch response.MonitoringID {
		case "website-monitoring":
			websiteChecks++
			if response.CheckIntervalSeconds == nil || *response.CheckIntervalSeconds != 900 {
				t.Fatalf("expected website interval 900, got %#v", response.CheckIntervalSeconds)
			}
		case "ping-monitoring":
			pingChecks++
			if response.CheckIntervalSeconds == nil || *response.CheckIntervalSeconds != 300 {
				t.Fatalf("expected ping interval 300, got %#v", response.CheckIntervalSeconds)
			}
		}
	}

	if websiteChecks != 2 || pingChecks != 3 {
		t.Fatalf("unexpected cadence counts: website=%d ping=%d", websiteChecks, pingChecks)
	}
}

func payloadType(monitoringID string) monitor.Type {
	if monitoringID == "keyword-monitoring" {
		return monitor.TypeKeyword
	}
	return monitor.TypeHTTP
}

func TestRunResponsePostsDNSRecordResultWithNilHTTPStatusCode(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{
		responseMonitorings: []monitor.Monitoring{
			{
				ID:                "dns-monitoring",
				Type:              monitor.TypeDNSRecord,
				Target:            "example.com",
				Timeout:           5,
				DNSRecordType:     "A",
				DNSExpectedValues: []string{"192.0.2.10"},
			},
		},
	}

	cfg := config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
	}
	runner := newTestService(client, cfg, log.New(io.Discard, "", 0))
	runner.dnsChecker = NewDNSRecordChecker(&staticDNSRecordResolver{
		values: []string{"192.0.2.10"},
	}, log.New(io.Discard, "", 0))

	if err := runner.runResponse(context.Background()); err != nil {
		t.Fatalf("runResponse failed: %v", err)
	}

	postedResponses := client.snapshotPostedResponses()
	if len(postedResponses) != 1 {
		t.Fatalf("expected 1 posted response, got %d", len(postedResponses))
	}
	payload := postedResponses[0]
	if payload.MonitoringID != "dns-monitoring" {
		t.Fatalf("expected monitoring_id dns-monitoring, got %s", payload.MonitoringID)
	}
	if payload.Status != monitor.StatusUp {
		t.Fatalf("expected up status, got %s", payload.Status)
	}
	if payload.ResponseTime == nil {
		t.Fatalf("expected response_time")
	}
	if payload.HTTPStatusCode != nil {
		t.Fatalf("expected nil http_status_code for dns record response")
	}
	if payload.Observation == nil || payload.Observation.DNSMatched == nil || !*payload.Observation.DNSMatched {
		t.Fatalf("expected matched DNS observation, got %#v", payload.Observation)
	}
	if !slices.Equal(payload.Observation.DNSObservedValues, []string{"192.0.2.10"}) {
		t.Fatalf("unexpected observed DNS values: %#v", payload.Observation.DNSObservedValues)
	}
}

type parallelPhasesClient struct {
	started chan string
	release chan struct{}
}

func (p *parallelPhasesClient) GetMonitorings(_ context.Context, _ string, types []monitor.Type) ([]monitor.Monitoring, error) {
	phase := "ssl"
	if slices.Contains(types, monitor.TypePing) || slices.Contains(types, monitor.TypeDNSRecord) {
		phase = "response"
	}
	if slices.Contains(types, monitor.TypeDomainExpiration) {
		phase = "domain"
	}
	p.started <- phase
	<-p.release
	return []monitor.Monitoring{}, nil
}

func (p *parallelPhasesClient) PostMonitoringResponse(_ context.Context, _ monitor.MonitoringResponsePayload) error {
	return nil
}

func (p *parallelPhasesClient) PostSSLResult(_ context.Context, _ monitor.SSLResultPayload) error {
	return nil
}

func (p *parallelPhasesClient) PostDomainResult(_ context.Context, _ monitor.DomainResultPayload) error {
	return nil
}

func (p *parallelPhasesClient) ClaimMonitoringJobs(_ context.Context, _ monitor.ClaimMonitoringJobsRequest) ([]monitor.ClaimedJob, error) {
	return nil, nil
}

func (p *parallelPhasesClient) CompleteMonitoringJob(_ context.Context, _ string, _ string, _ monitor.CompleteMonitoringJobRequest) error {
	return nil
}

func (p *parallelPhasesClient) ReleaseMonitoringJob(_ context.Context, _ string, _ monitor.ReleaseMonitoringJobRequest) error {
	return nil
}

func (p *parallelPhasesClient) ExtendMonitoringJob(_ context.Context, _ string, _ monitor.ExtendMonitoringJobRequest) (monitor.ExtendMonitoringJobResponse, error) {
	return monitor.ExtendMonitoringJobResponse{}, nil
}

func TestRunMonitoringRunsPhasesInParallel(t *testing.T) {
	t.Parallel()

	client := &parallelPhasesClient{
		started: make(chan string, 3),
		release: make(chan struct{}),
	}

	cfg := config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
	}
	runner := newTestService(client, cfg, log.New(io.Discard, "", 0))

	done := make(chan struct{})
	go func() {
		_ = runner.RunMonitoring(context.Background())
		close(done)
	}()

	timeout := time.After(500 * time.Millisecond)
	startedPhases := map[string]bool{}
	for len(startedPhases) < 3 {
		select {
		case phase := <-client.started:
			startedPhases[phase] = true
		case <-timeout:
			t.Fatalf("expected all phases to start in parallel, got: %#v", startedPhases)
		}
	}

	close(client.release)

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("RunMonitoring did not finish after releasing blocked phases")
	}
}
