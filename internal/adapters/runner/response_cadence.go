package runner

import (
	"hash/fnv"
	"sync"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

const legacyResponseCheckInterval = 5 * time.Minute

type responseCadence struct {
	mu     sync.Mutex
	starts map[string]time.Time
}

func newResponseCadence() *responseCadence {
	return &responseCadence{starts: make(map[string]time.Time)}
}

func (c *responseCadence) tryStart(monitoring monitor.Monitoring, location string, now time.Time) bool {
	interval := responseCheckInterval(monitoring)
	if !isScheduledResponseWindow(monitoring.ID, location, interval, now) {
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

func responseCheckInterval(monitoring monitor.Monitoring) time.Duration {
	if monitoring.CheckIntervalSeconds <= 0 {
		return legacyResponseCheckInterval
	}

	return time.Duration(monitoring.CheckIntervalSeconds) * time.Second
}

func isScheduledResponseWindow(monitoringID, location string, interval time.Duration, now time.Time) bool {
	if interval <= legacyResponseCheckInterval {
		return true
	}

	slots := int64((interval + legacyResponseCheckInterval - 1) / legacyResponseCheckInterval)
	if slots < 1 {
		return true
	}

	tick := now.UTC().Unix() / int64(legacyResponseCheckInterval/time.Second)

	return tick%slots == responseScheduleSlot(monitoringID, location, slots)
}

func responseScheduleSlot(monitoringID, location string, slots int64) int64 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(monitoringID + ":" + location))

	return int64(hash.Sum32()) % slots
}
