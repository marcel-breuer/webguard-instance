package scheduler

import (
	"context"
	"log"
	"time"
)

func RunEveryFiveMinutes(ctx context.Context, logger *log.Logger, task func(context.Context) error) {
	timer := time.NewTimer(time.Until(nextFiveMinuteBoundary(time.Now())))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := task(ctx); err != nil && logger != nil {
				logger.Printf("Scheduled run failed: %v", err)
			}
			timer.Reset(5 * time.Minute)
		}
	}
}

func nextFiveMinuteBoundary(now time.Time) time.Time {
	boundary := now.Truncate(5 * time.Minute)
	if !boundary.After(now) {
		boundary = boundary.Add(5 * time.Minute)
	}
	return boundary
}
