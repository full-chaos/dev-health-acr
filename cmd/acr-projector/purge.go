package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// purgeDSNEnvironment names the DSN of the org purge's own database role. The
// purge deletes rows in tables the runtime role must not delete from, so it never
// runs on the runtime DSN.
const purgeDSNEnvironment = "ACR_POSTGRES_PURGE_DSN"

// errPurgeDSNRequired is what `acr-projector purge` returns when no purge DSN is
// set. It names the variable, so an operator who ran the purge as before sees
// what changed and what to set.
var errPurgeDSNRequired = errors.New("acr-projector purge requires " + purgeDSNEnvironment + " (or " + purgeDSNEnvironment + "_FILE): the purge runs as its own database role, not the runtime role, and refuses to run without it")

func purge(args []string) error {
	purgeDSN, err := config.SecretValue(os.LookupEnv, purgeDSNEnvironment)
	if err != nil {
		return fmt.Errorf("%s: %w", purgeDSNEnvironment, err)
	}
	if strings.TrimSpace(purgeDSN) == "" {
		return errPurgeDSNRequired
	}
	cfg, err := config.LoadProjector()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	flags := flag.NewFlagSet("acr-projector purge", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	org := flags.String("org", "", "organization ID to purge (required)")
	dryRun := flags.Bool("dry-run", false, "count what would be removed; change nothing")
	confirm := flags.String("confirm-hash", "", "organization key printed by --dry-run")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*org) == "" {
		return errors.New("acr-projector purge requires --org")
	}
	entitlementClient, err := purgeEntitlementClient()
	if err != nil {
		return err
	}
	defer entitlementClient.Close()

	cfg.PostgresDSN = purgeDSN
	cfg.ProjectionEnabled = true
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime, err := openRuntime(ctx, cfg, logger, nil)
	if err != nil {
		return fmt.Errorf("open runtime: %w", err)
	}
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			logger.Error("error closing runtime", "failure_class", projectionrun.ClassifyFailure(closeErr))
		}
	}()
	if runtime.Coordinator == nil || runtime.DB == nil {
		return errors.New("acr-projector purge requires Postgres, ClickHouse, and a configured graph backend")
	}
	deleter, ok := runtime.Backend.(orgpurge.GraphDropper)
	if !ok {
		return errors.New("acr-projector purge: the graph backend cannot list and delete organization graphs")
	}
	locker, err := projectionrun.NewPostgresOrgLocker(runtime.DB)
	if err != nil {
		return err
	}
	purger := &orgpurge.Purger{Logger: logger, DB: runtime.DB, Graph: deleter, Set: runtime.Coordinator, Entitlements: entitlementClient, Locker: locker}
	report, err := purger.Run(ctx, *org, orgpurge.Options{DryRun: *dryRun, ConfirmOrgKey: *confirm})
	if err != nil {
		if errors.Is(err, orgpurge.ErrIncomplete) {
			if encoded, marshalErr := json.MarshalIndent(report, "", "  "); marshalErr == nil {
				fmt.Println(string(encoded))
			}
			logger.ErrorContext(ctx, "projection organization purge incomplete", "org_key", report.OrgKey, "graphs", report.Graphs)
		}
		return fmt.Errorf("purge organization %s: %w", report.OrgKey, err)
	}
	logger.InfoContext(ctx, "projection organization purge finished", "org_key", report.OrgKey, "status", report.Status, "total_rows", report.TotalRows, "graphs", report.Graphs)
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	if report.KeptLegalHold > 0 {
		fmt.Println("note: legal_hold episodes were kept; release the hold, then run purge again")
	}
	if report.KeptAuditEvents > 0 {
		fmt.Println("note: audit_events were kept")
	}
	return nil
}

func purgeEntitlementClient() (*entitlements.Client, error) {
	rawURL := strings.TrimSpace(os.Getenv("ACR_DEV_HEALTH_ENTITLEMENT_URL"))
	tokenFile := strings.TrimSpace(os.Getenv("ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE"))
	if rawURL == "" || tokenFile == "" {
		return nil, errors.New("acr-projector purge requires ACR_DEV_HEALTH_ENTITLEMENT_URL and ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE: it must ask Dev Health whether the organization still exists")
	}
	baseURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("ACR_DEV_HEALTH_ENTITLEMENT_URL is invalid")
	}
	var proxyURL *url.URL
	if raw := strings.TrimSpace(os.Getenv("ACR_DEV_HEALTH_ENTITLEMENT_PROXY_URL")); raw != "" {
		if proxyURL, err = url.Parse(raw); err != nil {
			return nil, errors.New("ACR_DEV_HEALTH_ENTITLEMENT_PROXY_URL is invalid")
		}
	}
	return entitlements.New(entitlements.Config{BaseURL: baseURL, TokenFile: tokenFile, ProxyURL: proxyURL, CACertPath: strings.TrimSpace(os.Getenv("ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE"))})
}
