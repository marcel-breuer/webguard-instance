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
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/config"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

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

func (f *fakeCoreClient) ExtendMonitoringJob(_ context.Context, _ string, _ monitor.ExtendMonitoringJobRequest) (monitor.ExtendMonitoringJobResponse, error) {
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

func TestRunMonitoringClaimsMixedJobsAndCompletesWithIdempotencyKey(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{claimedJobs: []monitor.ClaimedJob{
		{
			ID:             "job-response",
			Phase:          string(PhaseResponse),
			Attempt:        2,
			IdempotencyKey: "response-key",
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
			IdempotencyKey: "expired-key",
			LeaseExpiresAt: time.Now().Add(-time.Minute),
			Monitoring: monitor.Monitoring{
				ID: "http-expired", Type: monitor.TypeHTTP, Target: "https://example.com",
			},
		},
	}}
	runner := New(client, config.Config{
		WebGuardLocation:    "de-1",
		WebGuardInstanceID:  "worker-de-1-a",
		JobLeasesEnabled:    true,
		JobLeaseMaxBatch:    4,
		QueueDefaultWorkers: 2,
		AllowPrivateTargets: true,
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
	if claims[0].Capacity != 2 || claims[0].MaxBatchSize != 4 {
		t.Fatalf("unexpected claim capacity: %#v", claims[0])
	}
	if !slices.Equal(claims[0].Capabilities, []string{"response", "ssl", "domain_expiration"}) {
		t.Fatalf("unexpected claim capabilities: %#v", claims[0].Capabilities)
	}

	completed := client.snapshotCompletedJobs()
	if len(completed) != 1 {
		t.Fatalf("expected one completed job, got %d", len(completed))
	}
	if completed[0].jobID != "job-response" || completed[0].idempotencyKey != "response-key" || completed[0].request.Attempt != 2 {
		t.Fatalf("unexpected completion: %#v", completed[0])
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
	runner := New(client, cfg, log.New(io.Discard, "", 0))

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
	runner := New(client, cfg, log.New(io.Discard, "", 0))

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
	runner := New(client, cfg, log.New(&logs, "", 0))

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
	runner := New(client, cfg, log.New(io.Discard, "", 0))

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
	runner := New(client, cfg, log.New(io.Discard, "", 0))
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
	runner := New(client, cfg, log.New(io.Discard, "", 0))

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
