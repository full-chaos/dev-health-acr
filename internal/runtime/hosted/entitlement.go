package hosted

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

type localEntitlement struct{}

func (localEntitlement) HasEntitlement(ctx context.Context, orgID, entitlement string) (bool, error) {
	if strings.TrimSpace(orgID) == "" || entitlement != "agent_context_runtime" {
		return false, errors.New("unsupported entitlement request")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func (localEntitlement) Check(ctx context.Context) error { return ctx.Err() }
func (localEntitlement) Close() error                    { return nil }

func newEntitlement(cfg config.Config) (entitlementChecker, error) {
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
	client, err := entitlements.New(entitlements.Config{
		BaseURL: baseURL, TokenFile: cfg.DevHealthEntitlementTokenFile, Timeout: cfg.DevHealthEntitlementTimeout,
		MaxResponseBytes: cfg.DevHealthEntitlementMaxResponseBytes, ProxyURL: proxyURL, CACertPath: cfg.DevHealthEntitlementCACertPath,
		AllowInsecureLoopback: cfg.DevHealthEntitlementAllowInsecureLoopback,
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}
