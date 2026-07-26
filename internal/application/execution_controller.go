package application

import (
	"context"
	"log"
	"sync"
	"time"
)

type MonitoringRunner interface {
	RunMonitoring(context.Context) error
}

// ExecutionController prevents overlapping runs and provides one shared
// concurrency budget to every phase of a monitoring run.
type ExecutionController struct {
	runner         MonitoringRunner
	logger         *log.Logger
	maxConcurrency int
	mu             sync.Mutex
	summaryMu      sync.RWMutex
	lastSummary    RunSummary
}

type RunSummary struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Skipped    bool
	Err        error
}

func NewExecutionController(runner MonitoringRunner, logger *log.Logger, maxConcurrency int) *ExecutionController {
	if maxConcurrency < 1 {
		maxConcurrency = 1
	}
	return &ExecutionController{runner: runner, logger: logger, maxConcurrency: maxConcurrency}
}

func (c *ExecutionController) RunMonitoring(ctx context.Context) error {
	if !c.mu.TryLock() {
		if c.logger != nil {
			c.logger.Println("Monitoring run skipped because another run is active.")
		}
		c.storeSummary(RunSummary{StartedAt: time.Now(), FinishedAt: time.Now(), Skipped: true})
		return nil
	}
	defer c.mu.Unlock()

	startedAt := time.Now()
	err := c.runner.RunMonitoring(withExecutionLimiter(ctx, make(chan struct{}, c.maxConcurrency)))
	c.storeSummary(RunSummary{StartedAt: startedAt, FinishedAt: time.Now(), Err: err})
	return err
}

func (c *ExecutionController) LastSummary() RunSummary {
	c.summaryMu.RLock()
	defer c.summaryMu.RUnlock()
	return c.lastSummary
}

func (c *ExecutionController) storeSummary(summary RunSummary) {
	c.summaryMu.Lock()
	defer c.summaryMu.Unlock()
	c.lastSummary = summary
}

type executionLimiterKey struct{}

func withExecutionLimiter(ctx context.Context, limiter chan struct{}) context.Context {
	return context.WithValue(ctx, executionLimiterKey{}, limiter)
}

func AcquireExecutionSlot(ctx context.Context) (func(), error) {
	limiter, _ := ctx.Value(executionLimiterKey{}).(chan struct{})
	if limiter == nil {
		return func() {}, nil
	}
	select {
	case limiter <- struct{}{}:
		return func() { <-limiter }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
