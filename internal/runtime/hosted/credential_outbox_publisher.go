package hosted

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/authprojection"
	"github.com/full-chaos/dev-health-acr/internal/authworkload"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
)

const (
	defaultCredentialOutboxPublishInterval   = 30 * time.Second
	defaultCredentialOutboxPublishBatchLimit = 100
	credentialOutboxPublishFailureMessage    = "credential outbox publish tick failed; retrying on next tick"
	credentialOutboxPublishFullMessage       = "credential outbox publish batch full; more events wait for the next tick"
	credentialOutboxPublishHeldMessage       = "credential outbox publish holds aggregates the endpoint did not accept"
	credentialOutboxPublishStoppedMessage    = "credential outbox publish tick stopped"
)

// credentialOutboxTicker is *authprojection.Publisher's tick.
type credentialOutboxTicker interface {
	Tick(ctx context.Context, limit int) (authprojection.Report, error)
}

// startCredentialOutboxPublisher runs the publisher's tick every interval in the background. Unlike the purge loops
// it makes no call at start: an auth-service outage never stops acr-api, and a failed tick only waits for the next.
// A tick that did anything logs counts with fixed text; no event, aggregate, credential, token or answer text is
// ever logged.
func startCredentialOutboxPublisher(ctx context.Context, ticker credentialOutboxTicker, logger *slog.Logger, observe packetPurgeFailureObserver, interval time.Duration) func() error {
	if interval <= 0 {
		interval = defaultCredentialOutboxPublishInterval
	}
	purge := func(ctx context.Context, _ time.Time, limit int) (int, error) {
		report, err := ticker.Tick(ctx, limit)
		if logger != nil {
			if report.Applied+report.Gap+report.DigestMismatch+report.Refused+report.Other+report.NoAnswer > 0 {
				logger.InfoContext(ctx, "credential outbox publish", "source", "publisher",
					"applied", report.Applied, "gap", report.Gap, "digest_mismatch", report.DigestMismatch,
					"refused", report.Refused, "other", report.Other, "no_answer", report.NoAnswer)
			}
			if report.Held > 0 {
				logger.WarnContext(ctx, credentialOutboxPublishHeldMessage, "held", report.Held)
			}
			if report.Stopped != "" {
				logger.WarnContext(ctx, credentialOutboxPublishStoppedMessage, "stopped_by", string(report.Stopped))
			}
			if report.Full {
				logger.InfoContext(ctx, credentialOutboxPublishFullMessage, "applied", report.Applied)
			}
		}
		return report.Applied, err
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	tick := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer tick.Stop()
		runPurgeTickLoop(loopCtx, tick.C, time.Now, purge, defaultCredentialOutboxPublishBatchLimit, credentialOutboxPublishFailureMessage, observe)
	}()
	return func() error {
		cancel()
		<-done
		return nil
	}
}

// buildCredentialOutboxPublisher returns the publisher for cfg, or nil when ACR_AUTH_PROJECTION_URL is unset. It makes
// no network call and reads no subject file (the workload client reads it per exchange).
func buildCredentialOutboxPublisher(cfg config.Config, store authprojection.Store, logger *slog.Logger) (*authprojection.Publisher, error) {
	if !cfg.AuthProjection.Enabled() {
		return nil, nil
	}
	tokens, err := authworkload.Build(cfg.AuthWorkload, logger)
	if err != nil {
		return nil, err
	}
	if tokens == nil {
		return nil, errors.New("ACR_AUTH_PROJECTION_URL needs the auth workload client (ACR_AUTH_WORKLOAD_*)")
	}
	client, err := authprojection.New(cfg.AuthProjection.URL, tokens, outboundhttp.Transport())
	if err != nil {
		return nil, errors.New("the auth projection configuration is invalid (ACR_AUTH_PROJECTION_URL)")
	}
	return authprojection.NewPublisher(store, client, nil), nil
}

var _ authprojection.Store = (*storagepostgres.OutboxPublisher)(nil)
