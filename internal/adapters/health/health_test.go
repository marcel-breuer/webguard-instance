package health

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/application"
)

func TestNewHandlerGet(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()

	NewHandler(ReadinessFunc(func() bool { return true }), application.NewTelemetry()).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if recorder.Body.String() != "ok" {
		t.Fatalf("expected body ok, got %q", recorder.Body.String())
	}
}

func TestNewHandlerHealthGet(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()

	NewHandler(ReadinessFunc(func() bool { return true }), application.NewTelemetry()).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if recorder.Body.String() != "ok" {
		t.Fatalf("expected body ok, got %q", recorder.Body.String())
	}
}

func TestNewHandlerMethodNotAllowed(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	recorder := httptest.NewRecorder()

	NewHandler(ReadinessFunc(func() bool { return true }), application.NewTelemetry()).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", recorder.Code)
	}
}

func TestOperationsHandlerSeparatesLivenessReadinessAndMetrics(t *testing.T) {
	t.Parallel()
	telemetry := application.NewTelemetry()
	telemetry.RecordRun(time.Second, "success")
	telemetry.QueueJob()
	telemetry.StartJob()
	telemetry.FinishJobWithDuration("response", "up", 250*time.Millisecond)
	telemetry.RecordJobTransition("running", "completed")
	telemetry.RecordSchedulerRun(75*time.Millisecond, 2*time.Second, "success")
	telemetry.RecordLeaseWait(40 * time.Millisecond)
	telemetry.RecordCoreRequest("claim_monitoring_jobs", 50*time.Millisecond, nil)
	handler := NewHandler(ReadinessFunc(func() bool { return false }), telemetry)

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusOK || live.Body.String() != "ok" {
		t.Fatalf("unexpected liveness response: %d %q", live.Code, live.Body.String())
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable || ready.Body.String() != "not ready" {
		t.Fatalf("unexpected readiness response: %d %q", ready.Code, ready.Body.String())
	}
	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), `webguard_instance_runs_total{outcome="success"} 1`) || !strings.Contains(metrics.Body.String(), `operation="claim_monitoring_jobs"`) {
		t.Fatalf("unexpected metrics: %q", metrics.Body.String())
	}
	for _, metric := range []string{
		"webguard_instance_job_duration_seconds_sum{phase=\"response\"} 0.250000",
		"webguard_instance_job_transitions_total{from=\"running\",to=\"completed\"} 1",
		"webguard_instance_scheduler_delay_seconds_sum 0.075000",
		"webguard_instance_lease_wait_seconds_sum 0.040000",
		"webguard_instance_process_goroutines",
		"webguard_instance_process_heap_alloc_bytes",
	} {
		if !strings.Contains(metrics.Body.String(), metric) {
			t.Fatalf("expected metric %q, got %q", metric, metrics.Body.String())
		}
	}
	if strings.Contains(metrics.Body.String(), "target") || strings.Contains(metrics.Body.String(), "monitoring_id") {
		t.Fatalf("metrics must not expose high-cardinality target labels: %q", metrics.Body.String())
	}
}

func TestStartShutsDownOnContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- StartWithHandler(ctx, "127.0.0.1:0", log.New(io.Discard, "", 0), NewHandler(ReadinessFunc(func() bool { return true }), application.NewTelemetry()))
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected graceful shutdown, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not shutdown in time")
	}
}
