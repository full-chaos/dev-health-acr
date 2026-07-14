package hosted

import (
	"errors"
	"net/url"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func newEntitlement(cfg config.Config) (entitlementChecker, error) {
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
