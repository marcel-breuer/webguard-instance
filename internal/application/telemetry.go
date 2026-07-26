package application

import (
	"sync"
	"time"
)

// Telemetry keeps bounded, process-local operational counters. It deliberately
// accepts only low-cardinality dimensions such as phase and operation.
type Telemetry struct {
	mu sync.RWMutex

	runs        map[string]uint64
	runDuration DurationMetric
	jobsActive  int64
	jobsQueued  int64
	executors   map[string]uint64
	core        map[string]RequestMetric
	leases      map[string]uint64
}

type DurationMetric struct {
	Count uint64
	Sum   time.Duration
}

type RequestMetric struct {
	Count    uint64
	Errors   uint64
	Duration time.Duration
}

type TelemetrySnapshot struct {
	Runs        map[string]uint64
	RunDuration DurationMetric
	JobsActive  int64
	JobsQueued  int64
	Executors   map[string]uint64
	Core        map[string]RequestMetric
	Leases      map[string]uint64
}

func NewTelemetry() *Telemetry {
	return &Telemetry{
		runs:      make(map[string]uint64),
		executors: make(map[string]uint64),
		core:      make(map[string]RequestMetric),
		leases:    make(map[string]uint64),
	}
}

func (t *Telemetry) RecordRun(duration time.Duration, outcome string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.runs[outcome]++
	t.runDuration.Count++
	t.runDuration.Sum += duration
}

func (t *Telemetry) QueueJob() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.jobsQueued++
	t.mu.Unlock()
}

func (t *Telemetry) StartJob() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.jobsQueued > 0 {
		t.jobsQueued--
	}
	t.jobsActive++
	t.mu.Unlock()
}

func (t *Telemetry) FinishJob(phase, outcome string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.jobsActive > 0 {
		t.jobsActive--
	}
	t.executors[phase+"/"+outcome]++
	t.mu.Unlock()
}

func (t *Telemetry) DropQueuedJob(phase, outcome string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.jobsQueued > 0 {
		t.jobsQueued--
	}
	t.executors[phase+"/"+outcome]++
	t.mu.Unlock()
}

func (t *Telemetry) RecordCoreRequest(operation string, duration time.Duration, err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	metric := t.core[operation]
	metric.Count++
	metric.Duration += duration
	if err != nil {
		metric.Errors++
	}
	t.core[operation] = metric
}

func (t *Telemetry) RecordLease(event string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.leases[event]++
	t.mu.Unlock()
}

func (t *Telemetry) Snapshot() TelemetrySnapshot {
	if t == nil {
		return TelemetrySnapshot{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TelemetrySnapshot{
		Runs:        cloneMap(t.runs),
		RunDuration: t.runDuration,
		JobsActive:  t.jobsActive,
		JobsQueued:  t.jobsQueued,
		Executors:   cloneMap(t.executors),
		Core:        cloneRequests(t.core),
		Leases:      cloneMap(t.leases),
	}
}

func cloneMap(source map[string]uint64) map[string]uint64 {
	copy := make(map[string]uint64, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func cloneRequests(source map[string]RequestMetric) map[string]RequestMetric {
	copy := make(map[string]RequestMetric, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}
