package application

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type MonitoringRunner interface {
	RunMonitoring(context.Context) error
}

// MonitoringRunnerFunc adapts a plain function to MonitoringRunner.
type MonitoringRunnerFunc func(context.Context) error

func (f MonitoringRunnerFunc) RunMonitoring(ctx context.Context) error {
	return f(ctx)
}

// ExecutionLimiter bounds concurrently executing checks. Sharing one limiter
// between controllers keeps independently scheduled runs within a single
// process-wide concurrency budget.
type ExecutionLimiter struct {
	slots chan struct{}
}

func NewExecutionLimiter(maxConcurrency int) *ExecutionLimiter {
	return &ExecutionLimiter{slots: make(chan struct{}, max(1, maxConcurrency))}
}

type ExecutionControllerOption func(*ExecutionController)

// WithExecutionLimiter replaces the controller's own limiter with a shared one.
func WithExecutionLimiter(limiter *ExecutionLimiter) ExecutionControllerOption {
	return func(c *ExecutionController) {
		if limiter != nil {
			c.limiter = limiter
		}
	}
}

// WithRunName prefixes run IDs so logs of concurrently scheduled controllers
// stay distinguishable.
func WithRunName(name string) ExecutionControllerOption {
	return func(c *ExecutionController) {
		c.name = name
	}
}

// ExecutionController prevents overlapping runs and provides one concurrency
// budget to every phase of a monitoring run.
type ExecutionController struct {
	runner      MonitoringRunner
	logger      *log.Logger
	limiter     *ExecutionLimiter
	name        string
	telemetry   *Telemetry
	mu          sync.Mutex
	summaryMu   sync.RWMutex
	lastSummary RunSummary
	stateMu     sync.RWMutex
	draining    bool
	active      bool
	idle        chan struct{}
	runSequence atomic.Uint64
}

type RunSummary struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Skipped    bool
	Err        error
}

func NewExecutionControllerWithTelemetry(runner MonitoringRunner, logger *log.Logger, maxConcurrency int, telemetry *Telemetry, options ...ExecutionControllerOption) *ExecutionController {
	if telemetry == nil {
		telemetry = NewTelemetry()
	}
	idle := make(chan struct{})
	close(idle)
	controller := &ExecutionController{runner: runner, logger: logger, limiter: NewExecutionLimiter(maxConcurrency), telemetry: telemetry, idle: idle}
	for _, option := range options {
		option(controller)
	}
	return controller
}

func (c *ExecutionController) RunMonitoring(ctx context.Context) error {
	c.stateMu.RLock()
	draining := c.draining
	c.stateMu.RUnlock()
	if draining {
		c.recordSkippedRun()
		return nil
	}
	if !c.mu.TryLock() {
		c.recordSkippedRun()
		return nil
	}
	defer c.mu.Unlock()
	c.stateMu.Lock()
	if c.draining {
		c.stateMu.Unlock()
		c.recordSkippedRun()
		return nil
	}
	c.active = true
	c.idle = make(chan struct{})
	c.stateMu.Unlock()
	defer func() {
		c.stateMu.Lock()
		c.active = false
		close(c.idle)
		c.stateMu.Unlock()
	}()

	startedAt := time.Now()
	runID := fmt.Sprintf("run-%d", c.runSequence.Add(1))
	if c.name != "" {
		runID = c.name + "-" + runID
	}
	if c.logger != nil {
		c.logger.Printf("run_id=%s monitoring run started", runID)
	}
	err := c.runner.RunMonitoring(withExecutionLimiter(withRunID(ctx, runID), c.limiter.slots))
	finishedAt := time.Now()
	c.storeSummary(RunSummary{StartedAt: startedAt, FinishedAt: finishedAt, Err: err})
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	c.telemetry.RecordRun(finishedAt.Sub(startedAt), outcome)
	if c.logger != nil {
		c.logger.Printf("run_id=%s monitoring run finished outcome=%s", runID, outcome)
	}
	return err
}

func (c *ExecutionController) recordSkippedRun() {
	if c.logger != nil {
		if c.name != "" {
			c.logger.Printf("Monitoring run skipped because another %s run is active or the instance is draining.", c.name)
		} else {
			c.logger.Println("Monitoring run skipped because another run is active or the instance is draining.")
		}
	}
	now := time.Now()
	c.storeSummary(RunSummary{StartedAt: now, FinishedAt: now, Skipped: true})
	c.telemetry.RecordRun(0, "skipped")
}

func (c *ExecutionController) LastSummary() RunSummary {
	c.summaryMu.RLock()
	defer c.summaryMu.RUnlock()
	return c.lastSummary
}

func (c *ExecutionController) BeginDrain() {
	c.stateMu.Lock()
	c.draining = true
	c.stateMu.Unlock()
}

func (c *ExecutionController) IsDraining() bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.draining
}

func (c *ExecutionController) IsActive() bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.active
}

func (c *ExecutionController) WaitForIdle(ctx context.Context) bool {
	c.stateMu.RLock()
	idle := c.idle
	c.stateMu.RUnlock()
	select {
	case <-idle:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *ExecutionController) Telemetry() *Telemetry {
	return c.telemetry
}

func (c *ExecutionController) storeSummary(summary RunSummary) {
	c.summaryMu.Lock()
	defer c.summaryMu.Unlock()
	c.lastSummary = summary
}

type executionLimiterKey struct{}
type runIDKey struct{}

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

func withRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDKey{}, runID)
}

func RunID(ctx context.Context) string {
	runID, _ := ctx.Value(runIDKey{}).(string)
	return runID
}
