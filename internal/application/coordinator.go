package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
)

// Phase represents one independent part of a monitoring run. Implementations
// belong to adapters; the coordinator only owns their lifecycle.
type Phase interface {
	Name() string
	Run(context.Context) error
}

// Coordinator executes independent monitoring phases concurrently and reports
// their individual failures without coupling application policy to transports.
type Coordinator struct {
	logger *log.Logger
	phases []Phase
}

func NewCoordinator(logger *log.Logger, phases ...Phase) *Coordinator {
	return &Coordinator{logger: logger, phases: phases}
}

func (c *Coordinator) Run(ctx context.Context) error {
	if len(c.phases) == 0 {
		return nil
	}

	type phaseResult struct {
		index int
		name  string
		err   error
	}

	results := make(chan phaseResult, len(c.phases))
	var phases sync.WaitGroup
	phases.Add(len(c.phases))

	for index, phase := range c.phases {
		phase := phase
		index := index
		go func() {
			defer phases.Done()
			results <- phaseResult{index: index, name: phase.Name(), err: phase.Run(ctx)}
		}()
	}

	phases.Wait()
	close(results)

	failures := make([]error, len(c.phases))
	for result := range results {
		if result.err != nil && c.logger != nil {
			c.logger.Printf("%s monitoring phase failed: %v", result.name, result.err)
		}
		if result.err != nil {
			failures[result.index] = fmt.Errorf("%s monitoring phase: %w", result.name, result.err)
		}
	}

	if c.logger != nil {
		c.logger.Println("All monitoring jobs have been dispatched successfully.")
	}

	return errors.Join(failures...)
}
