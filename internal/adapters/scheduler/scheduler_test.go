package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/application"
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

// The scheduler tests run inside a synctest bubble, whose fake clock starts at
// 2000-01-01T00:00:00Z and only advances when every goroutine is blocked, so
// minute boundaries are reached deterministically without real waiting.

func TestRunEveryMinuteInvokesTaskOnMinuteBoundaries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		time.Sleep(25 * time.Second)

		var mu sync.Mutex
		var runs []time.Time
		done := make(chan struct{})
		go func() {
			defer close(done)
			RunEveryMinute(ctx, log.New(io.Discard, "", 0), func(context.Context) error {
				mu.Lock()
				runs = append(runs, time.Now())
				mu.Unlock()
				return nil
			})
		}()

		time.Sleep(3*time.Minute - 25*time.Second + time.Second)
		synctest.Wait()
		cancel()
		<-done

		mu.Lock()
		defer mu.Unlock()
		start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		expected := []time.Time{start.Add(time.Minute), start.Add(2 * time.Minute), start.Add(3 * time.Minute)}
		if len(runs) != len(expected) {
			t.Fatalf("expected %d runs, got %d (%v)", len(expected), len(runs), runs)
		}
		for i := range expected {
			if !runs[i].Equal(expected[i]) {
				t.Fatalf("run %d: expected %s, got %s", i, expected[i], runs[i])
			}
		}
	})
}

func TestRunEveryMinuteStopsAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		var mu sync.Mutex
		calls := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			RunEveryMinute(ctx, nil, func(context.Context) error {
				mu.Lock()
				calls++
				mu.Unlock()
				return nil
			})
		}()

		time.Sleep(time.Minute + time.Second)
		cancel()
		<-done

		time.Sleep(5 * time.Minute)
		synctest.Wait()

		mu.Lock()
		defer mu.Unlock()
		if calls != 1 {
			t.Fatalf("expected exactly one run before cancellation, got %d", calls)
		}
	})
}

func TestRunEveryMinuteSkipsMissedBoundariesAfterSlowTask(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var mu sync.Mutex
		var runs []time.Time
		done := make(chan struct{})
		go func() {
			defer close(done)
			RunEveryMinute(ctx, nil, func(context.Context) error {
				mu.Lock()
				runs = append(runs, time.Now())
				first := len(runs) == 1
				mu.Unlock()
				if first {
					time.Sleep(90 * time.Second)
				}
				return nil
			})
		}()

		time.Sleep(3*time.Minute + time.Second)
		cancel()
		<-done

		mu.Lock()
		defer mu.Unlock()
		start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		expected := []time.Time{start.Add(time.Minute), start.Add(3 * time.Minute)}
		if len(runs) != len(expected) || !runs[0].Equal(expected[0]) || !runs[1].Equal(expected[1]) {
			t.Fatalf("expected runs %v, got %v", expected, runs)
		}
	})
}

func TestRunEveryMinuteWithTelemetryRecordsOutcomesAndLogsErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var logs bytes.Buffer
		logger := log.New(&logs, "", 0)
		telemetry := application.NewTelemetry()

		var mu sync.Mutex
		calls := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			RunEveryMinuteWithTelemetry(ctx, logger, telemetry, func(context.Context) error {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if calls == 1 {
					time.Sleep(2 * time.Second)
					return errors.New("core unavailable")
				}
				return nil
			})
		}()

		time.Sleep(2*time.Minute + 30*time.Second)
		cancel()
		<-done

		snapshot := telemetry.Snapshot()
		if snapshot.SchedulerRuns["error"] != 1 || snapshot.SchedulerRuns["success"] != 1 {
			t.Fatalf("expected one error and one success run, got %v", snapshot.SchedulerRuns)
		}
		if snapshot.SchedulerDuration.Count != 2 || snapshot.SchedulerDuration.Sum != 2*time.Second {
			t.Fatalf("expected two runs totalling 2s, got %+v", snapshot.SchedulerDuration)
		}
		if snapshot.SchedulerDelay.Count != 2 || snapshot.SchedulerDelay.Sum != 0 {
			t.Fatalf("expected two on-time runs, got %+v", snapshot.SchedulerDelay)
		}
		output := logs.String()
		if strings.Count(output, "Scheduled run failed: core unavailable") != 1 {
			t.Fatalf("expected one failure log line, got %q", output)
		}
	})
}
