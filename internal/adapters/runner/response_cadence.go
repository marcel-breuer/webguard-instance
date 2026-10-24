package runner

import (
	"sync"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

const (
	legacyResponseCheckInterval  = 5 * time.Minute
	minimumResponseCheckInterval = time.Minute
)

type responseCadence struct {
	mu     sync.Mutex
	starts map[string]time.Time
}

func newResponseCadence() *responseCadence {
	return &responseCadence{starts: make(map[string]time.Time)}
}

func (c *responseCadence) tryStart(monitoring monitor.Monitoring, location string, now time.Time) bool {
	interval := responseCheckInterval(monitoring)
	if !responseDue(monitoring, now) {
		return false
	}

	key := monitoring.ID + ":" + location
	c.mu.Lock()
	defer c.mu.Unlock()

	if previousStart, ok := c.starts[key]; ok && now.Sub(previousStart) < interval {
		return false
	}

	c.starts[key] = now
	return true
}

func responseDue(monitoring monitor.Monitoring, now time.Time) bool {
	if monitoring.LastResponseAt == nil {
		return true
	}

	return !now.Before(monitoring.LastResponseAt.Add(responseCheckInterval(monitoring)))
}

func responseCheckInterval(monitoring monitor.Monitoring) time.Duration {
	if monitoring.CheckIntervalSeconds < int(minimumResponseCheckInterval/time.Second) {
		return legacyResponseCheckInterval
	}

	return time.Duration(monitoring.CheckIntervalSeconds) * time.Second
}
