package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

const rootUsage = `Usage: acr-api [serve|healthcheck|version|credentials]

Commands:
  serve        run the ACR HTTP API
  healthcheck  verify an ACR readiness endpoint
  version      print build identity
  credentials  administer ACR client credentials
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
	case "healthcheck":
		return healthcheck(args)
	case "credentials":
		return runCredentialCLI(context.Background(), args, os.LookupEnv, os.Stdout, os.Stderr)
	default:
		return fmt.Errorf("unknown command %q; use serve, healthcheck, version, or credentials", command)
	}
}

func healthcheck(args []string) error {
	flags := flag.NewFlagSet("acr-api healthcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("url", "http://127.0.0.1:8080/readyz", "readiness endpoint")
	timeout := flags.Duration("timeout", 3*time.Second, "request timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return errors.New("healthcheck timeout must be positive")
	}

	client := &http.Client{Timeout: *timeout}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, *endpoint, nil)
	if err != nil {
		return fmt.Errorf("create healthcheck request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("readiness request failed: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("readiness endpoint returned %s", response.Status)
	}
	return nil
}

func serve(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	flags := flag.NewFlagSet("acr-api serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	listen := flags.String("listen", cfg.ListenAddress, "HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg.ListenAddress = *listen
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	info := version.Current()
	logger.Info("starting acr-api", append([]any{
		"version", info.Version,
		"commit", info.Commit,
		"build_date", info.Date,
	}, cfg.SafeAttributes()...)...)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server, closeRuntime, err := prepareServer(ctx, serverBuildRequest{
		config: cfg, logger: logger, serviceVersion: info.Version,
		openRuntime: func(ctx context.Context, options hosted.Options) (*hosted.Runtime, error) {
			return hosted.Open(ctx, cfg, options)
		},
		newServer: newAPIServer,
	})
	if err != nil {
		return err
	}
	runErr := server.Run(ctx)
	if closeRuntime == nil {
		return runErr
	}
	return errors.Join(runErr, closeRuntime())
}
