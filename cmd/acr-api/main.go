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

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
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
	var evidenceCodec *contextpacket.EvidenceIDCodec
	if cfg.Environment == "production" || cfg.RequireBackingStores {
		evidenceCodec, err = contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{
			ActiveKID: cfg.EvidenceIDActiveKID,
			Keys:      cfg.EvidenceIDKeys,
		})
		if err != nil {
			return fmt.Errorf("initialize evidence id codec: %w", err)
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	info := version.Current()
	logger.Info("starting acr-api", append([]any{
		"version", info.Version,
		"commit", info.Commit,
		"build_date", info.Date,
	}, cfg.SafeAttributes()...)...)

	capabilities := api.StaticCapabilitiesProvider{Value: contractsv1.Capabilities{
		SchemaVersion:         contractsv1.CapabilitiesSchema,
		Service:               "dev-health-acr",
		ServiceVersion:        info.Version,
		MinimumSidecarVersion: cfg.MinimumSidecarVersion,
		SupportedSchemaVersions: []string{
			contractsv1.ContextPacketRequestSchema,
			contractsv1.ContextPacketSchema,
			contractsv1.ContextPacketItemSchema,
			contractsv1.EvidenceRefSchema,
			contractsv1.ExpandedEvidenceSchema,
			contractsv1.CapabilitiesSchema,
			contractsv1.AgentEpisodeCreateSchema,
			contractsv1.AgentEpisodeSchema,
			contractsv1.ClientCredentialSchema,
			contractsv1.ErrorSchema,
		},
		EnabledTools: []string{},
		Entitlements: contractsv1.CapabilityEntitlements{
			AgentContextRuntime: false,
		},
		Permissions: contractsv1.CapabilityPermissions{},
		Limits: contractsv1.CapabilityLimits{
			MaxItems:           cfg.MaxItems,
			MaxOutputTokens:    cfg.MaxOutputTokens,
			MaxSerializedBytes: cfg.MaxSerializedBytes,
			RequestsPerMinute:  cfg.RequestsPerMinute,
		},
	}}

	checks := []api.ReadinessCheck{
		api.CheckFunc{CheckName: "configuration", Fn: func(context.Context) error { return cfg.Validate() }},
	}
	if cfg.RequireBackingStores {
		checks = append(checks,
			api.CheckFunc{CheckName: "evidence_id_codec", Fn: func(context.Context) error {
				if evidenceCodec == nil {
					return errors.New("evidence id codec is not configured")
				}
				return nil
			}},
			api.CheckFunc{CheckName: "clickhouse_configuration", Fn: func(context.Context) error {
				if cfg.ClickHouseDSN == "" {
					return errors.New("ClickHouse is not configured")
				}
				return nil
			}},
			api.CheckFunc{CheckName: "postgres_configuration", Fn: func(context.Context) error {
				if cfg.PostgresDSN == "" {
					return errors.New("PostgreSQL is not configured")
				}
				return nil
			}},
		)
	}

	app, err := api.NewApp(api.AppConfig{
		ServiceName:    "dev-health-acr",
		ServiceVersion: info.Version,
		RequestTimeout: cfg.RequestTimeout,
	}, api.Dependencies{
		Capabilities:         capabilities,
		ReadinessChecks:      checks,
		EvidenceStoreFactory: contextpacket.NewEvidenceStoreFactory(evidenceCodec),
	}, logger)
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}

	server, err := api.NewServer(api.ServerConfig{
		ListenAddress:     cfg.ListenAddress,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ShutdownTimeout:   cfg.ShutdownTimeout,
	}, app.Handler(), logger)
	if err != nil {
		return fmt.Errorf("initialize server: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx)
}
