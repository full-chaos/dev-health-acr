package falkorgraph

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Read attempt failure causes: the closed vocabulary of the cause field of
// GraphReadAttemptTelemetry. The first six are transient and retried while
// attempts remain; ReadFailureTimeout and ReadFailurePoolTimeout are never
// retried.
const (
	ReadFailureEOF               = "eof"
	ReadFailureConnectionReset   = "connection_reset"
	ReadFailureConnectionRefused = "connection_refused"
	ReadFailureBrokenPipe        = "broken_pipe"
	ReadFailureConnectionAborted = "connection_aborted"
	ReadFailureLoading           = "loading"
	ReadFailureTimeout           = "timeout"
	ReadFailurePoolTimeout       = "pool_timeout"
	// DependencyCauseOther is the dependency cause of a driver error with no
	// class above (a TLS alert, an unknown reply).
	DependencyCauseOther = "other"
)

// GraphReadAttemptTelemetry is the optional extension of GraphTelemetry for a
// graph read attempt that failed with a cause from the closed vocabulary
// above. retried is true when another attempt follows. Nothing else about the
// failure is passed: no address, no driver text, no graph key.
type GraphReadAttemptTelemetry interface {
	RecordGraphReadAttemptFailed(ctx context.Context, operation string, attempt, maxAttempts int, cause string, retried bool)
}

func (t SlogTelemetry) RecordGraphReadAttemptFailed(ctx context.Context, operation string, attempt, maxAttempts int, cause string, retried bool) {
	t.logger().WarnContext(ctx, "context_fabric: graph read attempt failed",
		"operation", contextfabric.SanitizeLogAttr(operation), "attempt", attempt, "max_attempts", maxAttempts,
		"cause", contextfabric.SanitizeLogAttr(cause), "retried", retried)
}

// readFailureCause classifies a raw driver error from the read client.
// retry is true only for a dropped, reset, refused or aborted connection and
// a server still loading its dataset. A timeout is never retried: a read that
// missed RequestTimeout would add the same load again. A context error has no
// cause and is never retried.
func readFailureCause(err error) (cause string, retry bool) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", false
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return ReadFailureEOF, true
	case errors.Is(err, syscall.ECONNRESET):
		return ReadFailureConnectionReset, true
	case errors.Is(err, syscall.ECONNREFUSED):
		return ReadFailureConnectionRefused, true
	case errors.Is(err, syscall.EPIPE):
		return ReadFailureBrokenPipe, true
	case errors.Is(err, syscall.ECONNABORTED):
		return ReadFailureConnectionAborted, true
	case isLoadingReply(err.Error()):
		return ReadFailureLoading, true
	case errors.Is(err, redis.ErrPoolTimeout):
		return ReadFailurePoolTimeout, false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ReadFailureTimeout, false
	}
	return "", false
}

// isLoadingReply matches the LOADING error code, alone or followed by detail.
func isLoadingReply(message string) bool {
	return message == "LOADING" || strings.HasPrefix(message, "LOADING ")
}

// readRetryBackoff is the wait before attempt+1: 50ms, 100ms, 200ms, 400ms.
func readRetryBackoff(attempt int) time.Duration {
	return 50 * time.Millisecond << (attempt - 1)
}

// retryRead runs one read on the read client, whose driver resend is off, up
// to Config.MaxAttempts times. Writes never come here: a write may commit
// after its connection drops, and the projector replay is its only retry.
//
// A wait that would end at or after the caller's deadline is not started, so
// no attempt is sent past the deadline. When the caller's context has ended,
// a failure with a cause returns the context's error, which is what the
// driver's own retry returned before (TestReadsHonourTheCallersDeadlineOnTheNetworkIO).
//
// A cancel is honoured between attempts and during the wait, not inside a
// driver call, the same as main (client.go:24-27,
// TestACancelDoesNotStopAReadInFlight): a cancel that lands while the driver
// sets up a connection or waits on the socket lets that send go out, and a
// reply that arrives returns as is.
//
// Each failed attempt with a transient cause is recorded once: retried=true
// only when the next send starts, retried=false when the attempts, the
// deadline or the caller's end stop it. A timeout or pool timeout is recorded
// only while the caller's context is live, so a read that ran into the
// caller's own deadline is not counted as a dependency timeout.
func retryRead[T any](ctx context.Context, s *sdkAPI, operation string, call func() (T, error)) (T, error) {
	maxAttempts := int(s.config.MaxAttempts)
	for attempt := 1; ; attempt++ {
		value, err := call()
		if err == nil {
			return value, nil
		}
		cause, retry := readFailureCause(err)
		if cause == "" {
			return value, err
		}
		if ctxErr := contextEnded(ctx); ctxErr != nil {
			if retry {
				s.recordReadAttemptFailed(ctx, operation, attempt, maxAttempts, cause, false)
			}
			return value, ctxErr
		}
		wait := readRetryBackoff(attempt)
		if !retry || attempt >= maxAttempts || !waitEndsBeforeDeadline(ctx, wait) {
			s.recordReadAttemptFailed(ctx, operation, attempt, maxAttempts, cause, false)
			return value, err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.recordReadAttemptFailed(ctx, operation, attempt, maxAttempts, cause, false)
			return value, ctx.Err()
		case <-timer.C:
		}
		if ctxErr := contextEnded(ctx); ctxErr != nil {
			s.recordReadAttemptFailed(ctx, operation, attempt, maxAttempts, cause, false)
			return value, ctxErr
		}
		s.recordReadAttemptFailed(ctx, operation, attempt, maxAttempts, cause, true)
	}
}

// contextEnded reports the caller's end. The read client's socket deadline is
// the context deadline, and the socket can time out before the context's own
// timer marks it done, so a passed deadline counts as ended too.
func contextEnded(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func waitEndsBeforeDeadline(ctx context.Context, wait time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) > wait
}

func (s *sdkAPI) recordReadAttemptFailed(ctx context.Context, operation string, attempt, maxAttempts int, cause string, retried bool) {
	if sink, ok := s.config.Telemetry.(GraphReadAttemptTelemetry); ok {
		sink.RecordGraphReadAttemptFailed(ctx, operation, attempt, maxAttempts, cause, retried)
	}
}
