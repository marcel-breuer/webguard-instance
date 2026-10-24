package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/application"
)

func RunEveryMinute(ctx context.Context, logger *log.Logger, task func(context.Context) error) {
	RunEveryMinuteWithTelemetry(ctx, logger, nil, task)
}

func RunEveryMinuteWithTelemetry(ctx context.Context, logger *log.Logger, telemetry *application.Telemetry, task func(context.Context) error) {
	nextRun := nextMinuteBoundary(time.Now())
	timer := time.NewTimer(time.Until(nextRun))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			startedAt := time.Now()
			delay := max(time.Duration(0), startedAt.Sub(nextRun))
			err := task(ctx)
			duration := time.Since(startedAt)
			outcome := "success"
			if err != nil {
				outcome = "error"
			}
			if telemetry != nil {
				telemetry.RecordSchedulerRun(delay, duration, outcome)
			}
			if err != nil && logger != nil {
				logger.Printf("Scheduled run failed: %v", err)
			}
			nextRun = nextMinuteBoundary(time.Now())
			timer.Reset(time.Until(nextRun))
		}
	}
}

func nextMinuteBoundary(now time.Time) time.Time {
	boundary := now.Truncate(time.Minute)
	if !boundary.After(now) {
		boundary = boundary.Add(time.Minute)
	}
	return boundary
}
