package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"testing"

	"github.com/marcel-breuer/webguard-instance/internal/config"
)

type fakeMonitoringService struct {
	runMonitoringCalls int
}

func (f *fakeMonitoringService) RunMonitoring(context.Context) error {
	f.runMonitoringCalls++
	return nil
}

func TestRunDefaultsToServe(t *testing.T) {
	t.Parallel()

	service := &fakeMonitoringService{}
	var serveCalls int

	exitCode := run(
		nil,
		log.New(io.Discard, "", 0),
		config.Config{},
		service,
		func(_ *log.Logger, _ monitoringService, _ config.Config) int {
			serveCalls++
			return 0
		},
		io.Discard,
	)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if serveCalls != 1 {
		t.Fatalf("expected serve to be called once, got %d", serveCalls)
	}
	if service.runMonitoringCalls != 0 {
		t.Fatalf("expected monitoring not to run, got %d calls", service.runMonitoringCalls)
	}
}

func TestRunMonitoringCommand(t *testing.T) {
	t.Parallel()

	service := &fakeMonitoringService{}

	exitCode := run(
		[]string{"monitoring"},
		log.New(io.Discard, "", 0),
		config.Config{},
		service,
		func(_ *log.Logger, _ monitoringService, _ config.Config) int {
			t.Fatalf("serve should not be called for monitoring command")
			return 1
		},
		io.Discard,
	)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if service.runMonitoringCalls != 1 {
		t.Fatalf("expected monitoring to run once, got %d", service.runMonitoringCalls)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	service := &fakeMonitoringService{}

	exitCode := run(
		[]string{"unknown-command"},
		log.New(io.Discard, "", 0),
		config.Config{},
		service,
		func(_ *log.Logger, _ monitoringService, _ config.Config) int {
			t.Fatalf("serve should not be called for unknown command")
			return 1
		},
		&stderr,
	)

	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if service.runMonitoringCalls != 0 {
		t.Fatalf("expected monitoring not to run, got %d", service.runMonitoringCalls)
	}
	if stderr.Len() == 0 {
		t.Fatalf("expected usage output on stderr")
	}
}
