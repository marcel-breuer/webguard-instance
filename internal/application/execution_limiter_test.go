package application

import (
	"context"
	"errors"
	"testing"
)

func TestControllersSharingLimiterStayWithinOneConcurrencyBudget(t *testing.T) {
	t.Parallel()

	limiter := NewExecutionLimiter(1)
	holding := make(chan struct{})
	release := make(chan struct{})
	first := NewExecutionControllerWithTelemetry(MonitoringRunnerFunc(func(ctx context.Context) error {
		releaseSlot, err := AcquireExecutionSlot(ctx)
		if err != nil {
			return err
		}
		defer releaseSlot()
		close(holding)
		<-release
		return nil
	}), nil, 5, NewTelemetry(), WithExecutionLimiter(limiter))

	var secondErr error
	second := NewExecutionControllerWithTelemetry(MonitoringRunnerFunc(func(ctx context.Context) error {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, secondErr = AcquireExecutionSlot(canceled)
		return nil
	}), nil, 5, NewTelemetry(), WithExecutionLimiter(limiter))

	done := make(chan error, 1)
	go func() { done <- first.RunMonitoring(context.Background()) }()
	<-holding

	if err := second.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("unexpected second run error: %v", err)
	}
	if !errors.Is(secondErr, context.Canceled) {
		t.Fatalf("expected the shared budget to be exhausted for the second controller, got %v", secondErr)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("unexpected first run error: %v", err)
	}
	if len(limiter.slots) != 0 {
		t.Fatalf("expected all shared slots to be released, got %d in use", len(limiter.slots))
	}
}

func TestExecutionControllerRunNamePrefixesRunID(t *testing.T) {
	t.Parallel()

	var runIDs []string
	controller := NewExecutionControllerWithTelemetry(MonitoringRunnerFunc(func(ctx context.Context) error {
		runIDs = append(runIDs, RunID(ctx))
		return nil
	}), nil, 1, NewTelemetry(), WithRunName("auxiliary"))

	for range 2 {
		if err := controller.RunMonitoring(context.Background()); err != nil {
			t.Fatalf("unexpected run error: %v", err)
		}
	}
	if len(runIDs) != 2 || runIDs[0] != "auxiliary-run-1" || runIDs[1] != "auxiliary-run-2" {
		t.Fatalf("unexpected run IDs: %v", runIDs)
	}
}

func TestExecutionLimiterHasAtLeastOneSlot(t *testing.T) {
	t.Parallel()

	if got := cap(NewExecutionLimiter(0).slots); got != 1 {
		t.Fatalf("expected one slot for non-positive concurrency, got %d", got)
	}
}
