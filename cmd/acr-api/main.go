package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

const rootUsage = `Usage: acr-api [serve|version|credentials]

Commands:
  serve        run the ACR HTTP API
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
	case "credentials":
		return runCredentialCLI(context.Background(), args, os.LookupEnv, os.Stdout, os.Stderr)
	default:
		return fmt.Errorf("unknown command %q; use serve, version, or credentials", command)
	}
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
