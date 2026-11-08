package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/adapters/domainlookup"
	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/config"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

var errFetchUnavailable = errors.New("core unavailable")

// failingFetchClient fails monitoring fetches for one exact type set while
// failures are enabled and delegates everything else.
type failingFetchClient struct {
	*fakeCoreClient
	failMu    sync.Mutex
	failTypes []monitor.Type
	failing   bool
}

func (c *failingFetchClient) setFailing(failing bool) {
	c.failMu.Lock()
	c.failing = failing
	c.failMu.Unlock()
}

func (c *failingFetchClient) GetMonitorings(ctx context.Context, location string, types []monitor.Type) ([]monitor.Monitoring, error) {
	c.failMu.Lock()
	fail := c.failing && slices.Equal(types, c.failTypes)
	c.failMu.Unlock()
	if fail {
		c.mu.Lock()
		c.calls = append(c.calls, getMonitoringsCall{location: location, types: append([]monitor.Type(nil), types...)})
		c.mu.Unlock()
		return nil, errFetchUnavailable
	}
	return c.fakeCoreClient.GetMonitorings(ctx, location, types)
}

type blockingDomainLookup struct {
	started chan struct{}
	release chan struct{}
}

func (l blockingDomainLookup) Lookup(ctx context.Context, target string) (domainlookup.Result, error) {
	l.started <- struct{}{}
	select {
	case <-l.release:
		return domainlookup.Result{Domain: target, Registered: true}, nil
	case <-ctx.Done():
		return domainlookup.Result{}, ctx.Err()
	}
}

func countCallsForPhase(calls []getMonitoringsCall, types []monitor.Type) int {
	count := 0
	for _, call := range calls {
		if slices.Equal(call.types, types) {
			count++
		}
	}
	return count
}

func TestAuxiliaryPhaseRetriesNextTickAfterFetchFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	core := &fakeCoreClient{}
	service := newTestService(core, config.Config{WebGuardLocation: "de-1"}, log.New(io.Discard, "", 0))
	service.now = func() time.Time { return now }
	sslTypes := service.executors.Types(PhaseSSL)
	client := &failingFetchClient{fakeCoreClient: core, failTypes: sslTypes, failing: true}
	service.client = client

	if err := service.runSSL(context.Background()); !errors.Is(err, errFetchUnavailable) {
		t.Fatalf("expected fetch failure, got %v", err)
	}

	client.setFailing(false)
	now = now.Add(time.Minute)
	if err := service.runSSL(context.Background()); err != nil {
		t.Fatalf("expected the SSL phase to retry on the next tick, got %v", err)
	}
	now = now.Add(time.Minute)
	if err := service.runSSL(context.Background()); err != nil {
		t.Fatalf("unexpected SSL error: %v", err)
	}

	if got := countCallsForPhase(core.snapshotCalls(), sslTypes); got != 2 {
		t.Fatalf("expected a failed fetch and one successful retry within the interval, got %d SSL fetches", got)
	}
}

func TestRunResponseAndRunAuxiliaryFetchOnlyTheirPhases(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{}
	service := newTestService(client, config.Config{WebGuardLocation: "de-1"}, log.New(io.Discard, "", 0))
	responseTypes := service.executors.Types(PhaseResponse)
	sslTypes := service.executors.Types(PhaseSSL)
	domainTypes := service.executors.Types(PhaseDomainExpiration)

	if err := service.RunResponse(context.Background()); err != nil {
		t.Fatalf("RunResponse failed: %v", err)
	}
	calls := client.snapshotCalls()
	if len(calls) != 1 || !slices.Equal(calls[0].types, responseTypes) {
		t.Fatalf("expected RunResponse to fetch only response types, got %#v", calls)
	}

	if err := service.RunAuxiliary(context.Background()); err != nil {
		t.Fatalf("RunAuxiliary failed: %v", err)
	}
	calls = client.snapshotCalls()
	if len(calls) != 3 || countCallsForPhase(calls, sslTypes) != 1 || countCallsForPhase(calls, domainTypes) != 1 {
		t.Fatalf("expected RunAuxiliary to fetch SSL and domain types once each, got %#v", calls)
	}
}

func TestRunAuxiliaryIsNoopWhenJobLeasesAreEnabled(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{}
	service := newTestService(client, config.Config{WebGuardLocation: "de-1", JobLeasesEnabled: true, QueueDefaultWorkers: 1}, log.New(io.Discard, "", 0))

	if err := service.RunAuxiliary(context.Background()); err != nil {
		t.Fatalf("RunAuxiliary failed: %v", err)
	}
	if err := service.RunResponse(context.Background()); err != nil {
		t.Fatalf("RunResponse failed: %v", err)
	}
	if calls := client.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("expected no monitoring fetches in lease mode, got %#v", calls)
	}
	if requests := client.snapshotClaimRequests(); len(requests) != 1 {
		t.Fatalf("expected RunResponse to claim jobs once, got %d", len(requests))
	}
}

func TestResponseRunCompletesWhileAuxiliaryRunIsStillActive(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{
		responseMonitorings: []monitor.Monitoring{{ID: "http-1", Type: monitor.TypeHTTP, Target: "https://example.com"}},
		domainMonitorings:   []monitor.Monitoring{{ID: "domain-1", Type: monitor.TypeDomainExpiration, Target: "example.com"}},
	}
	telemetry := application.NewTelemetry()
	cfg := config.Config{WebGuardLocation: "de-1", QueueDefaultWorkers: 2, RunMaxConcurrency: 2}
	service := NewWithTelemetry(client, cfg, log.New(io.Discard, "", 0), telemetry)
	service.httpChecker = HTTPCheckFunc(func(context.Context, monitor.Monitoring) (int, string, time.Duration, error) {
		return http.StatusOK, "", 0, nil
	})
	lookup := blockingDomainLookup{started: make(chan struct{}, 1), release: make(chan struct{})}
	service.domainLookup = lookup

	limiter := application.NewExecutionLimiter(cfg.RunMaxConcurrency)
	response := application.NewExecutionControllerWithTelemetry(application.MonitoringRunnerFunc(service.RunResponse), nil, cfg.RunMaxConcurrency, telemetry, application.WithExecutionLimiter(limiter))
	auxiliary := application.NewExecutionControllerWithTelemetry(application.MonitoringRunnerFunc(service.RunAuxiliary), nil, cfg.RunMaxConcurrency, telemetry, application.WithExecutionLimiter(limiter))

	auxiliaryDone := make(chan error, 1)
	go func() { auxiliaryDone <- auxiliary.RunMonitoring(context.Background()) }()
	<-lookup.started

	for range 2 {
		if err := response.RunMonitoring(context.Background()); err != nil {
			t.Fatalf("response run failed: %v", err)
		}
		if response.LastSummary().Skipped {
			t.Fatal("response run was skipped while the auxiliary run was active")
		}
	}
	if !auxiliary.IsActive() {
		t.Fatal("expected the auxiliary run to still be active")
	}
	if posted := client.snapshotPostedResponses(); len(posted) != 1 || posted[0].MonitoringID != "http-1" {
		t.Fatalf("expected one response result while auxiliary work was blocked, got %#v", posted)
	}

	close(lookup.release)
	if err := <-auxiliaryDone; err != nil {
		t.Fatalf("auxiliary run failed: %v", err)
	}
}

func TestDispatchSummaryLogsKeepPhaseSpecificFields(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{
		responseMonitorings: []monitor.Monitoring{{ID: "http-1", Type: monitor.TypeHTTP, MaintenanceActive: true}},
		sslMonitorings:      []monitor.Monitoring{{ID: "ssl-1", Type: monitor.TypeHTTP, MaintenanceActive: true}},
		domainMonitorings:   []monitor.Monitoring{{ID: "domain-1", Type: monitor.TypeDomainExpiration, MaintenanceActive: true}},
	}
	var logs bytes.Buffer
	service := newTestService(client, config.Config{WebGuardLocation: "de-1", QueueDefaultWorkers: 1}, log.New(&logs, "", 0))

	if err := service.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("RunMonitoring failed: %v", err)
	}

	for _, want := range []string{
		"Response monitoring dispatch done. total=1 dispatched=0 skipped_cadence=0 skipped_maintenance=1 skipped_unsupported=0",
		"SSL monitoring dispatch done. total=1 dispatched=0 skipped_maintenance=1 skipped_unsupported=0",
		"Domain expiration monitoring dispatch done. total=1 dispatched=0 skipped_maintenance=1 skipped_unsupported=0",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing log line %q in:\n%s", want, logs.String())
		}
	}
	if len(client.snapshotPostedSSL()) != 0 {
		t.Fatal("SSL maintenance must not publish results")
	}
	if responses := client.snapshotPostedResponses(); len(responses) != 2 {
		t.Fatalf("expected response and domain maintenance to publish unknown results, got %#v", responses)
	}
}
