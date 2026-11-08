package runner

import (
	"sync"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

const (
	legacyResponseCheckInterval  = 5 * time.Minute
	minimumResponseCheckInterval = time.Minute
	// responseCadenceRetention keeps start entries for a multiple of their
	// interval so a monitoring whose interval changed is not restarted early,
	// while entries of deleted monitorings are eventually dropped.
	responseCadenceRetention = 2
)

type responseCadence struct {
	mu     sync.Mutex
	starts map[string]responseStart
}

type responseStart struct {
	at       time.Time
	interval time.Duration
}

func newResponseCadence() *responseCadence {
	return &responseCadence{starts: make(map[string]responseStart)}
}

func (c *responseCadence) tryStart(monitoring monitor.Monitoring, location string, now time.Time) bool {
	interval := responseCheckInterval(monitoring)
	if !responseDue(monitoring, now) {
		return false
	}

	key := monitoring.ID + ":" + location
	c.mu.Lock()
	defer c.mu.Unlock()

	if previousStart, ok := c.starts[key]; ok && now.Sub(previousStart.at) < interval {
		return false
	}

	c.starts[key] = responseStart{at: now, interval: interval}
	return true
}

// prune drops start entries that can no longer reject a check, which keeps the
// map bounded once monitorings are deleted or moved away from this location.
func (c *responseCadence) prune(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, start := range c.starts {
		if now.Sub(start.at) >= responseCadenceRetention*start.interval {
			delete(c.starts, key)
		}
	}
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
