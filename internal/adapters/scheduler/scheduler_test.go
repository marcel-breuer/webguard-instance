package scheduler

import (
	"context"
	"io"
	"log"
	"testing"
	"time"
)

func TestNextFiveMinuteBoundary(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 20, 11, 2, 31, 0, time.UTC)
	next := nextFiveMinuteBoundary(now)

	expected := time.Date(2026, 2, 20, 11, 5, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Fatalf("expected %s, got %s", expected, next)
	}
}

func TestRunEveryFiveMinutesReturnsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	taskCalled := make(chan struct{}, 1)
	go func() {
		RunEveryFiveMinutes(ctx, log.New(io.Discard, "", 0), func(context.Context) error {
			taskCalled <- struct{}{}
			return nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("scheduler did not return after context cancellation")
	}

	select {
	case <-taskCalled:
		t.Fatalf("task should not run on already canceled context")
	default:
	}
}
