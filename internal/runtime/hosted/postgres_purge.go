package hosted

import (
	"context"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
)

const (
	defaultPacketPurgeInterval   = 5 * time.Minute
	defaultPacketPurgeBatchLimit = 500

	// defaultWorkloadCredentialPurgeInterval is shorter than the packet
	// snapshot purge's: a workload re-exchanges a fresh
	// acr.client_credentials row roughly every WorkloadAccessTokenLifetime
	// (10 minutes, see authverify.WorkloadAccessTokenLifetime) for as
	// long as it runs, so these rows accumulate far faster than any other
	// purge target this package already sweeps.
	defaultWorkloadCredentialPurgeInterval   = time.Minute
	defaultWorkloadCredentialPurgeBatchLimit = 500

	// The OAuth rows (acr.oauth_authorization_requests and dynamically
	// registered acr.oauth_clients, CHAOS-6191) grow only as fast as the
	// rate-limited /authorize and /register routes are used, so the sweep
	// runs at the packet snapshot cadence (one heartbeat line per tick).
	defaultOAuthPurgeInterval   = 5 * time.Minute
	defaultOAuthPurgeBatchLimit = 500
)

// packetPurgeFunc purges expired context packet snapshots up to a bounded
// batch limit, returning the number purged.
type packetPurgeFunc func(ctx context.Context, before time.Time, limit int) (int, error)

// packetPurgeFailureObserver is invoked once for every purge tick that
// fails. It receives only a fixed, redacted message — never the underlying
// error — so a database error that might embed a DSN, credential, or other
// sensitive operational detail is never propagated past this boundary. A
// nil observer is a valid no-op.
type packetPurgeFailureObserver func(ctx context.Context, message string)

// packetPurgeFailureMessage is the message startPacketPurgeLoop passes to
// runPurgeTickLoop; startWorkloadCredentialPurgeLoop passes its own.
const packetPurgeFailureMessage = "packet purge tick failed; retrying on next tick"

// runPurgeTickLoop drains tick until either ctx is cancelled or tick is
// closed, invoking purge with a bounded batch limit on every tick. It never
// panics on a purge failure: a transient failure is retried on the next
// tick, and is reported through observe using only the fixed redacted
// message (never the underlying error) so recurring failures remain
// operationally visible without leaking database error detail. The loop
// returns (does not leak) as soon as ctx is done, which lets the caller
// join the goroutine that runs this function from Close.
func runPurgeTickLoop(ctx context.Context, tick <-chan time.Time, now func() time.Time, purge packetPurgeFunc, limit int, message string, observe packetPurgeFailureObserver) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-tick:
			if !ok {
				return
			}
			if _, err := purge(ctx, now().UTC(), limit); err != nil && observe != nil {
				observe(ctx, message)
			}
		}
	}
}

// startBoundedPurgeLoop performs an initial bounded purge synchronously (which
// doubles as a DELETE-privilege proof: a role missing DELETE on the purged
// table fails this call), then starts a cancellable background ticker that
// repeats the bounded purge every interval, reporting recurring failures to
// observe with the fixed message. The returned close function cancels the
// ticker and blocks until the background goroutine exits, so no goroutine is
// ever leaked across a runtime Close. The packet, workload credential and
// OAuth loops differ only in their interval, batch limit and message.
func startBoundedPurgeLoop(ctx context.Context, purge packetPurgeFunc, now func() time.Time, observe packetPurgeFailureObserver, interval time.Duration, limit int, message string) (func() error, error) {
	if now == nil {
		now = time.Now
	}
	if _, err := purge(ctx, now().UTC(), limit); err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		runPurgeTickLoop(loopCtx, ticker.C, now, purge, limit, message, observe)
	}()
	return func() error {
		cancel()
		<-done
		return nil
	}, nil
}

// startPacketPurgeLoop starts the bounded context packet snapshot purge; see
// startBoundedPurgeLoop (its initial purge proves DELETE on
// acr.context_packet_snapshots).
func startPacketPurgeLoop(ctx context.Context, purge packetPurgeFunc, now func() time.Time, observe packetPurgeFailureObserver) (func() error, error) {
	return startBoundedPurgeLoop(ctx, purge, now, observe, defaultPacketPurgeInterval, defaultPacketPurgeBatchLimit, packetPurgeFailureMessage)
}

// workloadCredentialPurgeFailureMessage is startWorkloadCredentialPurgeLoop's
// own message, passed to runPurgeTickLoop -- see packetPurgeFailureMessage's
// doc comment.
const workloadCredentialPurgeFailureMessage = "workload credential purge tick failed; retrying on next tick"

// startWorkloadCredentialPurgeLoop is startPacketPurgeLoop's twin for
// CHAOS-4013 workload-exchanged credential rows -- reusing runPurgeTickLoop
// through startBoundedPurgeLoop since packetPurgeFunc's signature already
// matches storagepostgres.NewWorkloadCredentialPurger's.
func startWorkloadCredentialPurgeLoop(ctx context.Context, purge packetPurgeFunc, now func() time.Time, observe packetPurgeFailureObserver) (func() error, error) {
	return startBoundedPurgeLoop(ctx, purge, now, observe, defaultWorkloadCredentialPurgeInterval, defaultWorkloadCredentialPurgeBatchLimit, workloadCredentialPurgeFailureMessage)
}

// oauthPurgeFailureMessage is startOAuthPurgeLoop's own message -- see
// packetPurgeFailureMessage's doc comment.
const oauthPurgeFailureMessage = "oauth purge tick failed; retrying on next tick"

// startOAuthPurgeLoop is startPacketPurgeLoop's twin for CHAOS-6191: expired
// acr.oauth_authorization_requests and idle dynamic acr.oauth_clients. Its
// initial purge proves DELETE on both tables.
func startOAuthPurgeLoop(ctx context.Context, purge packetPurgeFunc, now func() time.Time, observe packetPurgeFailureObserver) (func() error, error) {
	return startBoundedPurgeLoop(ctx, purge, now, observe, defaultOAuthPurgeInterval, defaultOAuthPurgeBatchLimit, oauthPurgeFailureMessage)
}

// oauthPurger is *storagepostgres.OAuthStore's purge, as the hosted runtime
// uses it (an interface so the tick's logging is testable without a database).
type oauthPurger interface {
	PurgeExpired(ctx context.Context, now time.Time, requestGrace, clientIdle time.Duration, limit int) (storagepostgres.OAuthPurgeResult, error)
}

// oauthPurgeFunc adapts an oauthPurger to a packetPurgeFunc with the
// configured windows. EVERY tick that reaches the database logs exactly one
// info line carrying the two row counts (never an id, client name or URI),
// zeros included: the line is the loop's heartbeat, so a loop that stopped, or
// one whose predicate silently selects nothing, is visible at Info as a missing
// line or as counts that stay zero while the tables grow. The count line is
// written before an error is returned, so a tick that deleted requests and
// then failed on the client statement is still visible; the failure itself is
// reported by the tick loop's fixed redacted warning.
func oauthPurgeFunc(purger oauthPurger, requestGrace, clientIdle time.Duration, logger *slog.Logger) packetPurgeFunc {
	return func(ctx context.Context, before time.Time, limit int) (int, error) {
		result, err := purger.PurgeExpired(ctx, before, requestGrace, clientIdle, limit)
		if logger != nil {
			logger.InfoContext(ctx, "oauth purge", "requests", result.Requests, "clients", result.Clients)
		}
		return result.Requests + result.Clients, err
	}
}

// startConfiguredOAuthPurge starts the CHAOS-6191 OAuth purge loop ONLY when
// the OAuth login is configured, for the workload purge's reason (see
// openPostgres): its initial synchronous purge issues a DELETE against
// acr.oauth_authorization_requests and acr.oauth_clients, which a deployment
// without OAuth has never needed granted, so starting it there would fail
// startup for no benefit. The windows are cfg's: config.Load is the one place
// they are read and validated. Unconfigured, it does nothing and returns a
// no-op close.
func startConfiguredOAuthPurge(ctx context.Context, cfg config.Config, purger oauthPurger, logger *slog.Logger) (func() error, error) {
	if !cfg.OAuthConfigured() {
		return func() error { return nil }, nil
	}
	return startOAuthPurgeLoop(ctx, oauthPurgeFunc(purger, cfg.OAuthRequestPurgeGrace, cfg.OAuthClientIdleTTL, logger), nil, packetPurgeSlogObserver(logger))
}

// packetPurgeSlogObserver adapts a *slog.Logger into a
// packetPurgeFailureObserver, logging only the fixed redacted message at
// warn level. A nil logger yields a nil observer (no-op), matching the
// package's nil-safe observer contract.
func packetPurgeSlogObserver(logger *slog.Logger) packetPurgeFailureObserver {
	if logger == nil {
		return nil
	}
	return func(ctx context.Context, message string) {
		logger.WarnContext(ctx, message)
	}
}
