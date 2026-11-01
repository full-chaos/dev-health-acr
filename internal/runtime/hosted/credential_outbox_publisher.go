package hosted

import (
	"context"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/authprojection"
	"github.com/full-chaos/dev-health-acr/internal/config"
)

const (
	defaultCredentialOutboxPublishInterval   = 30 * time.Second
	defaultCredentialOutboxPublishBatchLimit = 100
	credentialOutboxPublishFailureMessage    = "credential outbox publish tick failed; retrying on next tick"
	credentialOutboxPublishFullMessage       = "credential outbox publish batch full; more events wait for the next tick"
	credentialOutboxPublishHeldMessage       = "credential outbox publish holds aggregates the endpoint did not accept"
	credentialOutboxPublishStoppedMessage    = "credential outbox publish tick stopped"
)

type credentialOutboxTicker interface {
	Tick(ctx context.Context, limit int) (authprojection.Report, error)
}

func startCredentialOutboxPublisher(context.Context, credentialOutboxTicker, *slog.Logger, packetPurgeFailureObserver, time.Duration) func() error {
	return func() error { return nil }
}

func buildCredentialOutboxPublisher(config.Config, authprojection.Store, *slog.Logger) (*authprojection.Publisher, error) {
	return nil, nil
}
