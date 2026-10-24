package scheduler

import (
	"context"
	"io"
	"log"
	"testing"
	"time"
)

func TestNextMinuteBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "mid-minute",
			now:  time.Date(2026, 2, 20, 11, 2, 31, 0, time.UTC),
			want: time.Date(2026, 2, 20, 11, 3, 0, 0, time.UTC),
		},
		{
			name: "exact-minute-boundary",
			now:  time.Date(2026, 2, 20, 11, 2, 0, 0, time.UTC),
			want: time.Date(2026, 2, 20, 11, 3, 0, 0, time.UTC),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := nextMinuteBoundary(test.now); !got.Equal(test.want) {
				t.Fatalf("expected %s, got %s", test.want, got)
			}
		})
	}
}

func TestRunEveryMinuteReturnsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	taskCalled := make(chan struct{}, 1)
	go func() {
		RunEveryMinute(ctx, log.New(io.Discard, "", 0), func(context.Context) error {
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
