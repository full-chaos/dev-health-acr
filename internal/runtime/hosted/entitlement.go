package hosted

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/authworkload"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
)

// entitlementDeps are the optional collaborators of the remote entitlement client; the zero value is valid.
type entitlementDeps struct {
	logger  *slog.Logger
	metrics *hostedmetrics.Instruments
}

type localEntitlement struct{}

func (localEntitlement) HasEntitlement(ctx context.Context, orgID, entitlement string) (bool, error) {
	if strings.TrimSpace(orgID) == "" || (entitlement != "agent_context_runtime" && entitlement != "acr_service_model" && entitlement != "platform_model_fallback") {
		return false, errors.New("unsupported entitlement request")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func (localEntitlement) Check(ctx context.Context) error { return ctx.Err() }
func (localEntitlement) Close() error                    { return nil }

func newEntitlement(cfg config.Config, deps entitlementDeps) (entitlementChecker, error) {
	if cfg.EntitlementMode() == config.EntitlementModeLocal {
		return localEntitlement{}, nil
	}
	baseURL, err := url.Parse(cfg.DevHealthEntitlementURL)
	if err != nil {
		return nil, errors.New("entitlement origin configuration is invalid")
	}
	var proxyURL *url.URL
	if cfg.DevHealthEntitlementProxyURL != "" {
		proxyURL, err = url.Parse(cfg.DevHealthEntitlementProxyURL)
		if err != nil {
			return nil, errors.New("entitlement proxy configuration is invalid")
		}
	}
	remote := entitlements.Config{
		BaseURL: baseURL, TokenFile: cfg.DevHealthEntitlementTokenFile, Timeout: cfg.DevHealthEntitlementTimeout,
		MaxResponseBytes: cfg.DevHealthEntitlementMaxResponseBytes, ProxyURL: proxyURL, CACertPath: cfg.DevHealthEntitlementCACertPath,
	}
	if deps.metrics != nil {
		remote.Observe = deps.metrics.EntitlementCredential
	}
	if cfg.EntitlementCredential() == config.EntitlementCredentialWorkload {
		exchange, err := authworkload.Build(cfg.AuthWorkload, deps.logger)
		if err != nil {
			return nil, err
		}
		if exchange == nil {
			return nil, errors.New("the workload entitlement credential needs the auth workload client (ACR_AUTH_WORKLOAD_*)")
		}
		// The token file stays named in the config for a rollback, but this credential never reads it.
		remote.Workload, remote.TokenFile = exchange, ""
	}
	client, err := entitlements.New(remote)
	if err != nil {
		return nil, err
	}
	return client, nil
}
