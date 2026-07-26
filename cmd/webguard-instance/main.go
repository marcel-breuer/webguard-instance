package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/marcel-breuer/webguard-instance/internal/adapters/coreapi"
	"github.com/marcel-breuer/webguard-instance/internal/adapters/health"
	"github.com/marcel-breuer/webguard-instance/internal/adapters/runner"
	"github.com/marcel-breuer/webguard-instance/internal/adapters/scheduler"
	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/config"
)

type monitoringService interface {
	RunMonitoring(ctx context.Context) error
}

type serveFunc func(logger *log.Logger, service monitoringService, cfg config.Config) int

func main() {
	logger := log.New(os.Stdout, "", 0)
	cfg := config.FromEnv()
	coreClient := coreapi.NewClient(cfg.WebGuardCoreAPIURL, cfg.WebGuardCoreAPIKey, cfg.WebGuardLocation)
	service := application.NewExecutionController(runner.New(coreClient, cfg, logger), logger, cfg.RunMaxConcurrency)

	exitCode := run(os.Args[1:], logger, cfg, service, runServe, os.Stderr)
	os.Exit(exitCode)
}

func run(args []string, logger *log.Logger, cfg config.Config, service monitoringService, serve serveFunc, stderr io.Writer) int {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "serve":
		return serve(logger, service, cfg)
	case "monitoring":
		_ = service.RunMonitoring(context.Background())
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", command)
		fmt.Fprintln(stderr, "Usage:")
		fmt.Fprintln(stderr, "  webguard-instance serve")
		fmt.Fprintln(stderr, "  webguard-instance monitoring")
		return 1
	}
}

func runServe(logger *log.Logger, service monitoringService, cfg config.Config) int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go scheduler.RunEveryFiveMinutes(ctx, logger, service.RunMonitoring)

	if err := health.Start(ctx, cfg.Address, logger); err != nil {
		logger.Printf("Health server exited with error: %v", err)
		return 1
	}

	return 0
}
