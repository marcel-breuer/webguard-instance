package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/config"
)

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type fakeController struct {
	name    string
	runErr  error
	events  *eventLog
	idle    chan struct{}
	waiting chan struct{}

	mu       sync.Mutex
	runs     int
	draining bool
}

func newFakeController(name string, events *eventLog) *fakeController {
	idle := make(chan struct{})
	close(idle)
	return &fakeController{name: name, events: events, idle: idle}
}

func (f *fakeController) RunMonitoring(context.Context) error {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	return f.runErr
}

func (f *fakeController) BeginDrain() {
	f.mu.Lock()
	f.draining = true
	f.mu.Unlock()
	f.events.add(f.name + ":begin-drain")
}

func (f *fakeController) IsDraining() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.draining
}

func (f *fakeController) WaitForIdle(ctx context.Context) bool {
	f.events.add(f.name + ":wait-idle")
	if f.waiting != nil {
		f.waiting <- struct{}{}
	}
	select {
	case <-f.idle:
		f.events.add(f.name + ":idle")
		return true
	case <-ctx.Done():
		return false
	}
}

func (f *fakeController) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

func newTestControllers(controllers ...*fakeController) monitoringControllers {
	result := monitoringControllers{telemetry: application.NewTelemetry()}
	for _, controller := range controllers {
		result.controllers = append(result.controllers, controller)
	}
	return result
}

func failOnServe(t *testing.T) serveFunc {
	return func(*log.Logger, monitoringControllers, config.Config) int {
		t.Fatalf("serve should not be called")
		return 1
	}
}

func TestRunMonitoringCommandReturnsFailureWhenRunFails(t *testing.T) {
	t.Parallel()

	response := newFakeController("response", nil)
	auxiliary := newFakeController("auxiliary", nil)
	auxiliary.runErr = errors.New("Core unavailable")
	var logs bytes.Buffer

	exitCode := run([]string{"monitoring"}, log.New(&logs, "", 0), config.Config{}, newTestControllers(response, auxiliary), failOnServe(t), io.Discard)

	if exitCode != 1 || !bytes.Contains(logs.Bytes(), []byte("Monitoring command failed: Core unavailable")) {
		t.Fatalf("expected failed monitoring command to be visible, got exit=%d logs=%q", exitCode, logs.String())
	}
	if response.runCount() != 1 || auxiliary.runCount() != 1 {
		t.Fatalf("expected every controller to run once, got response=%d auxiliary=%d", response.runCount(), auxiliary.runCount())
	}
}

func TestRunMonitoringCommandJoinsAllControllerErrors(t *testing.T) {
	t.Parallel()

	responseErr := errors.New("response failed")
	auxiliaryErr := errors.New("auxiliary failed")
	response := newFakeController("response", nil)
	response.runErr = responseErr
	auxiliary := newFakeController("auxiliary", nil)
	auxiliary.runErr = auxiliaryErr

	err := newTestControllers(response, auxiliary).RunMonitoring(context.Background())
	if !errors.Is(err, responseErr) || !errors.Is(err, auxiliaryErr) {
		t.Fatalf("expected joined controller errors, got %v", err)
	}
}

func TestRunDefaultsToServe(t *testing.T) {
	t.Parallel()

	response := newFakeController("response", nil)
	var serveCalls int

	exitCode := run(nil, log.New(io.Discard, "", 0), config.Config{}, newTestControllers(response), func(*log.Logger, monitoringControllers, config.Config) int {
		serveCalls++
		return 0
	}, io.Discard)

	if exitCode != 0 || serveCalls != 1 || response.runCount() != 0 {
		t.Fatalf("expected serve only, got exit=%d serve=%d runs=%d", exitCode, serveCalls, response.runCount())
	}
}

func TestRunMonitoringCommand(t *testing.T) {
	t.Parallel()

	response := newFakeController("response", nil)
	auxiliary := newFakeController("auxiliary", nil)

	exitCode := run([]string{"monitoring"}, log.New(io.Discard, "", 0), config.Config{}, newTestControllers(response, auxiliary), failOnServe(t), io.Discard)

	if exitCode != 0 || response.runCount() != 1 || auxiliary.runCount() != 1 {
		t.Fatalf("expected exit 0 and one run per controller, got exit=%d response=%d auxiliary=%d", exitCode, response.runCount(), auxiliary.runCount())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	response := newFakeController("response", nil)

	exitCode := run([]string{"unknown-command"}, log.New(io.Discard, "", 0), config.Config{}, newTestControllers(response), failOnServe(t), &stderr)

	if exitCode != 1 || response.runCount() != 0 || stderr.Len() == 0 {
		t.Fatalf("expected usage failure, got exit=%d runs=%d stderr=%q", exitCode, response.runCount(), stderr.String())
	}
}

func readinessStatus(handler http.Handler) int {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return recorder.Code
}

func TestServeDrainsEveryControllerBeforeStoppingHealth(t *testing.T) {
	t.Parallel()

	events := &eventLog{}
	response := newFakeController("response", events)
	auxiliary := newFakeController("auxiliary", events)
	auxiliary.idle = make(chan struct{})
	auxiliary.waiting = make(chan struct{}, 1)

	shutdown, triggerShutdown := context.WithCancel(context.Background())
	defer triggerShutdown()
	handlers := make(chan http.Handler, 1)
	startHealth := func(ctx context.Context, _ string, _ *log.Logger, handler http.Handler) error {
		handlers <- handler
		<-ctx.Done()
		events.add("health-stopped")
		return nil
	}
	cfg := config.Config{
		WebGuardCoreAPIKey:          "key",
		WebGuardCoreAPIURL:          "https://core.example",
		WebGuardLocation:            "de-1",
		WebGuardInstanceAPIBasePath: "/api/instances",
		ShutdownDrainTimeout:        time.Minute,
	}

	done := make(chan int, 1)
	go func() {
		done <- serveUntil(shutdown, log.New(io.Discard, "", 0), newTestControllers(response, auxiliary), cfg, startHealth)
	}()

	handler := <-handlers
	if status := readinessStatus(handler); status != http.StatusOK {
		t.Fatalf("expected ready before shutdown, got %d", status)
	}

	triggerShutdown()
	<-auxiliary.waiting
	if status := readinessStatus(handler); status != http.StatusServiceUnavailable {
		t.Fatalf("expected not ready while draining, got %d", status)
	}
	close(auxiliary.idle)

	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	want := []string{
		"response:begin-drain", "auxiliary:begin-drain",
		"response:wait-idle", "response:idle",
		"auxiliary:wait-idle", "auxiliary:idle",
		"health-stopped",
	}
	if got := events.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("unexpected shutdown order:\n got %v\nwant %v", got, want)
	}
}

func TestServeReportsHealthServerFailure(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	startHealth := func(context.Context, string, *log.Logger, http.Handler) error {
		return errors.New("address in use")
	}

	shutdown, cancel := context.WithCancel(context.Background())
	defer cancel()
	exitCode := serveUntil(shutdown, log.New(&logs, "", 0), newTestControllers(newFakeController("response", nil)), config.Config{}, startHealth)
	if exitCode != 1 || !bytes.Contains(logs.Bytes(), []byte("Health server exited with error: address in use")) {
		t.Fatalf("expected health failure to exit 1, got exit=%d logs=%q", exitCode, logs.String())
	}
}
