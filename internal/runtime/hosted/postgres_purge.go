package hosted

import (
	"context"
	"log/slog"
	"time"
)

const (
	defaultPacketPurgeInterval   = 5 * time.Minute
	defaultPacketPurgeBatchLimit = 500
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

// packetPurgeFailureMessage is the sole message ever passed to a
// packetPurgeFailureObserver.
const packetPurgeFailureMessage = "packet purge tick failed; retrying on next tick"

// runPurgeTickLoop drains tick until either ctx is cancelled or tick is
// closed, invoking purge with a bounded batch limit on every tick. It never
// panics on a purge failure: a transient failure is retried on the next
// tick, and is reported through observe using only the fixed redacted
// message so recurring failures remain operationally visible without
// leaking database error detail. The loop returns (does not leak) as soon
// as ctx is done, which lets the caller join the goroutine that runs this
// function from Close.
func runPurgeTickLoop(ctx context.Context, tick <-chan time.Time, now func() time.Time, purge packetPurgeFunc, limit int, observe packetPurgeFailureObserver) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-tick:
			if !ok {
				return
			}
			if _, err := purge(ctx, now().UTC(), limit); err != nil && observe != nil {
				observe(ctx, packetPurgeFailureMessage)
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
		runPurgeTickLoop(loopCtx, ticker.C, now, purge, defaultPacketPurgeBatchLimit, observe)
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
