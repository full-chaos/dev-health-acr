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
	"strings"
	"syscall"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

const rootUsage = `Usage: acr-projector [serve|rebuild|rollback|priors|version]

Commands:
  serve             run the Context Fabric projection worker and readiness server
  rebuild --org ID  build an organization's graph aside at a fresh epoch and
                     reset checkpoints for that build (build-aside-and-swap,
                     CHAOS-3898 S2a-2, when ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED
                     is set) -- or purge the organization's projected graph
                     state in place otherwise (legacy path). Either way, the
                     next serve tick(s) replay a full snapshot from scratch.
  rollback --org ID restore an organization's PREVIOUS active epoch during
                     its post-flip grace window (CHAOS-3898 S2a-2; requires
                     ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED) -- the
                     disaster-recovery lever for a flip that should not have
                     happened. Refused outside the grace window.
  priors <sub>       CHAOS-3977 P5's own Bridge prior store operator surface
                     (curate/flip/rollback/revoke) -- see 'priors --help'.
  version            print build identity
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
	case "rebuild":
		return rebuild(args)
	case "rollback":
		return rollback(args)
	case "priors":
		return priorsCommand(args)
	default:
		return fmt.Errorf("unknown command %q; use serve, rebuild, rollback, priors, or version", command)
	}
}

// rebuild is the operator-facing entry point for CHAOS-3753's full-snapshot
// rebuild: purge the organization's backend state, reset its checkpoints,
// and let the next `serve` tick replay a bounded, complete-enumeration
// batch (devhealthsource's empty-cursor convention). It constructs the
// runtime as if projection were enabled regardless of
// ACR_CONTEXT_FABRIC_PROJECTION_ENABLED: an operator invoking this command
// has already made the decision, independent of the continuous-loop switch.
func rebuild(args []string) error {
	cfg, err := config.LoadProjector()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	flags := flag.NewFlagSet("acr-projector rebuild", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	org := flags.String("org", "", "organization ID to rebuild (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*org) == "" {
		return errors.New("acr-projector rebuild requires --org")
	}
	cfg.ProjectionEnabled = true
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime, err := openRuntime(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("open runtime: %w", err)
	}
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			logger.Error("error closing runtime", "failure_class", projectionrun.ClassifyFailure(closeErr))
		}
	}()
	if runtime.Coordinator == nil {
		return errors.New("acr-projector rebuild requires Postgres, ClickHouse, and a configured Zep graph backend")
	}
	if err := runtime.Coordinator.Rebuild(ctx, *org); err != nil {
		return fmt.Errorf("rebuild organization %s: %w", *org, err)
	}
	_, err = fmt.Printf("rebuilt organization %s\n", *org)
	return err
}

// rollback is the operator-facing entry point for CHAOS-3898 S2a-2's
// rollback lever: restore an organization's PREVIOUS active epoch while it
// is still within its post-flip grace window (design brief §3.1 step 4).
// Requires ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED -- Coordinator.Rollback
// itself refuses outright when Lifecycle is not configured.
func rollback(args []string) error {
	cfg, err := config.LoadProjector()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	flags := flag.NewFlagSet("acr-projector rollback", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	org := flags.String("org", "", "organization ID to roll back (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*org) == "" {
		return errors.New("acr-projector rollback requires --org")
	}
	cfg.ProjectionEnabled = true
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime, err := openRuntime(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("open runtime: %w", err)
	}
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			logger.Error("error closing runtime", "failure_class", projectionrun.ClassifyFailure(closeErr))
		}
	}()
	if runtime.Coordinator == nil {
		return errors.New("acr-projector rollback requires Postgres, ClickHouse, and a configured Zep graph backend")
	}
	if err := runtime.Coordinator.Rollback(ctx, *org); err != nil {
		return fmt.Errorf("rollback organization %s: %w", *org, err)
	}
	_, err = fmt.Printf("rolled back organization %s\n", *org)
	return err
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
			logger.Error("error closing runtime", "failure_class", projectionrun.ClassifyFailure(closeErr))
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
