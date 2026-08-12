package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

const rootUsage = `Usage: acr-projector [serve|version]

Commands:
  serve    run the Context Fabric projection worker and readiness server
  version  print build identity
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, err := fmt.Print(rootUsage)
		return err
	}
	command := "serve"
	if len(args) > 0 && args[0] != "-version" && args[0] != "--version" {
		command = args[0]
		args = args[1:]
	}
	if len(args) > 0 && (args[0] == "-version" || args[0] == "--version") {
		command = "version"
	}
	switch command {
	case "version":
		info := version.Current()
		fmt.Printf("%s commit=%s built=%s\n", info.Version, info.Commit, info.Date)
		return nil
	case "serve":
		return serve(args)
	default:
		return fmt.Errorf("unknown command %q; use serve or version", command)
	}
}

func serve(args []string) error {
	cfg, err := config.LoadProjector()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	flags := flag.NewFlagSet("acr-projector serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	listen := flags.String("listen", cfg.ListenAddress, "readiness HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg.ListenAddress = *listen
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	info := version.Current()
	logger.Info("starting acr-projector", append([]any{"version", info.Version, "commit", info.Commit, "build_date", info.Date}, cfg.SafeAttributes()...)...)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime, err := openRuntime(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("open runtime: %w", err)
	}
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			logger.Error("error closing runtime", "error", closeErr)
		}
	}()

	server := &http.Server{Addr: cfg.ListenAddress, Handler: readinessHandler(info.Version, runtime.Checks), ReadHeaderTimeout: 5 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()

	coordinatorErr := make(chan error, 1)
	if runtime.Coordinator != nil {
		go func() { coordinatorErr <- runtime.Coordinator.Run(ctx) }()
	} else {
		logger.Warn("projection coordinator did not start; readiness server is running in disabled mode")
	}

	var runErr error
	select {
	case <-ctx.Done():
		runErr = ctx.Err()
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("readiness server: %w", err)
		}
	case err := <-coordinatorErr:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			runErr = fmt.Errorf("projection coordinator: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("shut down readiness server: %w", shutdownErr))
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}
	return nil
}
