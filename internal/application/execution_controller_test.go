package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (r *blockingRunner) RunMonitoring(ctx context.Context) error {
	r.calls.Add(1)
	r.started <- struct{}{}
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestExecutionControllerSkipsOverlappingRun(t *testing.T) {
	t.Parallel()
	runner := &blockingRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	controller := NewExecutionControllerWithTelemetry(runner, nil, 1, NewTelemetry())
	done := make(chan error, 1)
	go func() { done <- controller.RunMonitoring(context.Background()) }()
	<-runner.started
	if err := controller.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("unexpected overlap error: %v", err)
	}
	if !controller.LastSummary().Skipped {
		t.Fatal("expected skipped summary")
	}
	close(runner.release)
	if err := <-done; err != nil {
		t.Fatalf("unexpected first run error: %v", err)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("expected one runner call, got %d", runner.calls.Load())
	}
}

func TestAcquireExecutionSlotHonorsCancellation(t *testing.T) {
	t.Parallel()
	limiter := make(chan struct{}, 1)
	limiter <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := AcquireExecutionSlot(withExecutionLimiter(ctx, limiter))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestExecutionControllerDrainStopsNewRunsAndWaitsForActiveRun(t *testing.T) {
	t.Parallel()
	runner := &blockingRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	controller := NewExecutionControllerWithTelemetry(runner, nil, 1, NewTelemetry())
	done := make(chan error, 1)
	go func() { done <- controller.RunMonitoring(context.Background()) }()
	<-runner.started

	controller.BeginDrain()
	if !controller.IsDraining() || !controller.IsActive() {
		t.Fatal("expected an active draining controller")
	}
	notIdleContext, cancelNotIdle := context.WithCancel(context.Background())
	cancelNotIdle()
	if controller.WaitForIdle(notIdleContext) {
		t.Fatal("expected active run not to be idle")
	}
	if err := controller.RunMonitoring(context.Background()); err != nil {
		t.Fatalf("unexpected draining run error: %v", err)
	}
	if !controller.LastSummary().Skipped {
		t.Fatal("expected run to be skipped during drain")
	}
	close(runner.release)
	if err := <-done; err != nil {
		t.Fatalf("unexpected active run error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !controller.WaitForIdle(ctx) || controller.IsActive() {
		t.Fatal("expected controller to become idle after active run completes")
	}
}
