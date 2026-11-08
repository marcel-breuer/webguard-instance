package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/marcel-breuer/webguard-instance/internal/adapters/coreapi"
	"github.com/marcel-breuer/webguard-instance/internal/adapters/health"
	"github.com/marcel-breuer/webguard-instance/internal/adapters/runner"
	"github.com/marcel-breuer/webguard-instance/internal/adapters/scheduler"
	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/config"
)

type monitoringController interface {
	RunMonitoring(ctx context.Context) error
	BeginDrain()
	IsDraining() bool
	WaitForIdle(ctx context.Context) bool
}

// monitoringControllers are scheduled independently so slow auxiliary phases
// never cause a response run to be skipped as overlapping.
type monitoringControllers struct {
	controllers []monitoringController
	telemetry   *application.Telemetry
}

type serveFunc func(logger *log.Logger, controllers monitoringControllers, cfg config.Config) int

type healthStarter func(ctx context.Context, address string, logger *log.Logger, handler http.Handler) error

func main() {
	logger := log.New(os.Stdout, "", 0)
	cfg := config.FromEnv()
	telemetry := application.NewTelemetry()
	coreClient := coreapi.NewClientWithInstanceAPIPath(cfg.WebGuardCoreAPIURL, cfg.WebGuardCoreAPIKey, cfg.WebGuardLocation, cfg.WebGuardInstanceAPIBasePath)
	coreClient.SetAPIKeyFile(cfg.WebGuardCoreAPIKeyFile)
	coreClient.SetTelemetry(telemetry)
	service := runner.NewWithTelemetry(coreClient, cfg, logger, telemetry)

	exitCode := run(os.Args[1:], logger, cfg, newMonitoringControllers(service, logger, cfg, telemetry), runServe, os.Stderr)
	os.Exit(exitCode)
}

func newMonitoringControllers(service *runner.MonitoringService, logger *log.Logger, cfg config.Config, telemetry *application.Telemetry) monitoringControllers {
	limiter := application.NewExecutionLimiter(cfg.RunMaxConcurrency)
	controller := func(name string, run application.MonitoringRunnerFunc) monitoringController {
		return application.NewExecutionControllerWithTelemetry(run, logger, cfg.RunMaxConcurrency, telemetry,
			application.WithExecutionLimiter(limiter),
			application.WithRunName(name),
		)
	}
	return monitoringControllers{
		controllers: []monitoringController{
			controller("response", service.RunResponse),
			controller("auxiliary", service.RunAuxiliary),
		},
		telemetry: telemetry,
	}
}

// RunMonitoring runs every controller once concurrently and joins their errors.
func (m monitoringControllers) RunMonitoring(ctx context.Context) error {
	failures := make([]error, len(m.controllers))
	var runs sync.WaitGroup
	for index, controller := range m.controllers {
		runs.Add(1)
		go func() {
			defer runs.Done()
			failures[index] = controller.RunMonitoring(ctx)
		}()
	}
	runs.Wait()
	return errors.Join(failures...)
}

func (m monitoringControllers) ready(cfg config.Config) bool {
	for _, controller := range m.controllers {
		if controller.IsDraining() {
			return false
		}
	}
	return cfg.IsReady()
}

// beginDrain stops new runs on every controller before any wait starts, so no
// controller can start work while another one is still finishing.
func (m monitoringControllers) beginDrain() {
	for _, controller := range m.controllers {
		controller.BeginDrain()
	}
}

func (m monitoringControllers) waitForIdle(ctx context.Context) bool {
	idle := true
	for _, controller := range m.controllers {
		if !controller.WaitForIdle(ctx) {
			idle = false
		}
	}
	return idle
}

func run(args []string, logger *log.Logger, cfg config.Config, controllers monitoringControllers, serve serveFunc, stderr io.Writer) int {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "serve":
		return serve(logger, controllers, cfg)
	case "monitoring":
		if err := controllers.RunMonitoring(context.Background()); err != nil {
			if logger != nil {
				logger.Printf("Monitoring command failed: %v", err)
			}
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", command)
		fmt.Fprintln(stderr, "Usage:")
		fmt.Fprintln(stderr, "  webguard-instance serve")
		fmt.Fprintln(stderr, "  webguard-instance monitoring")
		return 1
	}
}

func runServe(logger *log.Logger, controllers monitoringControllers, cfg config.Config) int {
	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	return serveUntil(signalContext, logger, controllers, cfg, health.StartWithHandler)
}

func serveUntil(shutdown context.Context, logger *log.Logger, controllers monitoringControllers, cfg config.Config, startHealth healthStarter) int {
	dispatchContext, stopDispatch := context.WithCancel(context.Background())
	defer stopDispatch()
	healthContext, stopHealth := context.WithCancel(context.Background())
	defer stopHealth()

	go func() {
		<-shutdown.Done()
		controllers.beginDrain()
		stopDispatch()
		drainContext, cancelDrain := context.WithTimeout(context.Background(), cfg.ShutdownDrainTimeout)
		defer cancelDrain()
		if !controllers.waitForIdle(drainContext) && logger != nil {
			logger.Println("Shutdown drain deadline reached with monitoring work still active.")
		}
		stopHealth()
	}()

	for _, controller := range controllers.controllers {
		go scheduler.RunEveryMinuteWithTelemetry(dispatchContext, logger, controllers.telemetry, controller.RunMonitoring)
	}

	readiness := health.ReadinessFunc(func() bool { return controllers.ready(cfg) })
	if err := startHealth(healthContext, cfg.Address, logger, health.NewHandler(readiness, controllers.telemetry)); err != nil {
		logger.Printf("Health server exited with error: %v", err)
		return 1
	}

	return 0
}
