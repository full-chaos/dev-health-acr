package hosted

import (
	"context"
	"log/slog"
	"time"
)

const (
	defaultPacketPurgeInterval   = 5 * time.Minute
	defaultPacketPurgeBatchLimit = 500

	// defaultWorkloadCredentialPurgeInterval is shorter than the packet
	// snapshot purge's: a workload re-exchanges a fresh
	// acr.client_credentials row roughly every WorkloadAccessTokenLifetime
	// (10 minutes, see internal/auth.WorkloadAccessTokenLifetime) for as
	// long as it runs, so these rows accumulate far faster than any other
	// purge target this package already sweeps.
	defaultWorkloadCredentialPurgeInterval   = time.Minute
	defaultWorkloadCredentialPurgeBatchLimit = 500
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

// startPacketPurgeLoop performs an initial bounded purge synchronously (which
// doubles as a DELETE-privilege proof: a role missing DELETE on
// acr.context_packet_snapshots fails this call), then starts a cancellable
// background ticker that repeats the bounded purge, reporting recurring
// failures to observe. The returned close function cancels the ticker and
// blocks until the background goroutine exits, so no goroutine is ever
// leaked across a runtime Close.
func startPacketPurgeLoop(ctx context.Context, purge packetPurgeFunc, now func() time.Time, observe packetPurgeFailureObserver) (func() error, error) {
	if now == nil {
		now = time.Now
	}
	if _, err := purge(ctx, now().UTC(), defaultPacketPurgeBatchLimit); err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	ticker := time.NewTicker(defaultPacketPurgeInterval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		runPurgeTickLoop(loopCtx, ticker.C, now, purge, defaultPacketPurgeBatchLimit, packetPurgeFailureMessage, observe)
	}()
	return func() error {
		cancel()
		<-done
		return nil
	}, nil
}

// workloadCredentialPurgeFailureMessage is startWorkloadCredentialPurgeLoop's
// own message, passed to runPurgeTickLoop -- see packetPurgeFailureMessage's
// doc comment.
const workloadCredentialPurgeFailureMessage = "workload credential purge tick failed; retrying on next tick"

// startWorkloadCredentialPurgeLoop is startPacketPurgeLoop's twin for
// CHAOS-4013 workload-exchanged credential rows: same initial-synchronous-
// purge-as-DELETE-privilege-proof, same cancellable background ticker, same
// never-panics-on-failure contract -- reusing runPurgeTickLoop directly
// since packetPurgeFunc's signature already matches
// storagepostgres.NewWorkloadCredentialPurger's. Only the interval/batch
// constants and the failure message differ.
func startWorkloadCredentialPurgeLoop(ctx context.Context, purge packetPurgeFunc, now func() time.Time, observe packetPurgeFailureObserver) (func() error, error) {
	if now == nil {
		now = time.Now
	}
	if _, err := purge(ctx, now().UTC(), defaultWorkloadCredentialPurgeBatchLimit); err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	ticker := time.NewTicker(defaultWorkloadCredentialPurgeInterval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		runPurgeTickLoop(loopCtx, ticker.C, now, purge, defaultWorkloadCredentialPurgeBatchLimit, workloadCredentialPurgeFailureMessage, observe)
	}()
	return func() error {
		cancel()
		<-done
		return nil
	}, nil
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
