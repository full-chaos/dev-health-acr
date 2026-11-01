// Package authworkload builds acr's client of the auth-service workload-token exchange (CHAOS-11195) from the
// ACR_AUTH_WORKLOAD_* settings. Building makes no network call and reads no subject file: the client reads the file
// again for every exchange, so a rotated projected token is picked up, and an auth-service outage never stops acr-api.
package authworkload

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/full-chaos/dev-health-go/workloadtoken"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
)

// Build returns the exchange client for cfg, or nil when cfg is off. A bad config is an error with no fallback
// credential; the error never echoes a configured value.
func Build(cfg config.AuthWorkloadConfig, logger *slog.Logger) (*workloadtoken.Client, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	subject := workloadtoken.KubernetesTokenFile(cfg.SubjectFile)
	if cfg.BootstrapFile != "" {
		subject = workloadtoken.BootstrapCredentialFile(cfg.BootstrapFile)
	}
	client, err := workloadtoken.New(workloadtoken.Config{
		ExchangeURL:    cfg.ExchangeURL,
		AllowPlainHTTP: cfg.AllowPlainHTTP,
		Subject:        subject,
		Transport:      newTransport(),
		Issuer:         cfg.Issuer,
		Logger:         logger,
	})
	if err != nil {
		return nil, errors.New("the auth workload client configuration is invalid (ACR_AUTH_WORKLOAD_*)")
	}
	return client, nil
}

// newTransport is a pool of its own with no proxy: the subject and every access token travel only to the host the
// exchange URL names.
func newTransport() http.RoundTripper { return outboundhttp.Transport() }
