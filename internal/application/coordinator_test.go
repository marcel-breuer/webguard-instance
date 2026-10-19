package application

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
)

type testPhase struct {
	name string
	run  func(context.Context) error
}

func (p testPhase) Name() string { return p.name }

func (p testPhase) Run(ctx context.Context) error { return p.run(ctx) }

func TestCoordinatorRunsPhasesInParallelAndReturnsFailures(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	phaseError := errors.New("unavailable")

	phase := func(name string, err error) testPhase {
		return testPhase{name: name, run: func(context.Context) error {
			started <- struct{}{}
			<-release
			return err
		}}
	}

	done := make(chan error, 1)
	go func() {
		done <- NewCoordinator(log.New(io.Discard, "", 0), phase("response", nil), phase("ssl", phaseError)).Run(context.Background())
	}()

	<-started
	<-started
	close(release)

	if err := <-done; !errors.Is(err, phaseError) {
		t.Fatalf("expected joined phase error, got %v", err)
	}
}

func TestCoordinatorHandlesNoPhases(t *testing.T) {
	t.Parallel()

	if err := NewCoordinator(nil).Run(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}
