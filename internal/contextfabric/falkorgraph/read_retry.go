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
// A cancel is honoured between attempts, during the wait, after a new
// connection's setup before the command is written (refuseAnEndedRead), and
// when the call returns: a reply, or a server error with no transient cause,
// that comes back after the caller's context ended returns the context's
// error. A cancel does not interrupt a socket wait already in progress
// (TestACancelDoesNotStopAReadInFlight). Known limit, not pinned: a cancel
// between the pool get and the write on a reused idle connection still sends
// the read once.
//
// Each failed attempt with a transient cause is recorded once: retried=true
// only when the next attempt is written, retried=false when the attempts, the
// deadline or the caller's end stop it. A retried attempt is recorded when the
// next attempt returns (nextAttemptWritten), so a next attempt that the
// caller's end stopped during its connection setup records retried=false. A timeout or pool timeout is recorded
// only while the caller's context is live, so a read that ran into the
// caller's own deadline is not counted as a dependency timeout.
func retryRead[T any](ctx context.Context, s *sdkAPI, operation string, call func() (T, error)) (T, error) {
	maxAttempts := int(s.config.MaxAttempts)
	pendingCause := ""
	for attempt := 1; ; attempt++ {
		value, err := call()
		if pendingCause != "" {
			s.recordReadAttemptFailed(ctx, operation, attempt-1, maxAttempts, pendingCause, nextAttemptWritten(ctx, err))
			pendingCause = ""
		}
		if err == nil {
			if ctxErr := s.endedAfterReply(ctx, operation, nil); ctxErr != nil {
				var zero T
				return zero, ctxErr
			}
			return value, nil
		}
		cause, retry := readFailureCause(err)
		if cause == "" {
			if ctxErr := s.endedAfterReply(ctx, operation, err); ctxErr != nil {
				var zero T
				return zero, ctxErr
			}
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
		pendingCause = cause
	}
}

// nextAttemptWritten reports whether the attempt after a retry reached the
// command write. It did not when it came back with a context error (the pool
// wait or refuseAnEndedRead) or with a timeout after the caller's context
// ended (the setup or the read ran into the caller's deadline). Known limit: a
// retry that was written and then ran into the caller's deadline with no reply
// is recorded as not retried, since its error is the same as a setup timeout.
func nextAttemptWritten(ctx context.Context, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errServerBudgetSpent) {
		return false
	}
	cause, _ := readFailureCause(err)
	return cause != ReadFailureTimeout || contextEnded(ctx) == nil
}

// refuseAnEndedRead is the read client's OnConnect step. go-redis runs it after
// a new connection's setup (HELLO, CLIENT SETINFO) with the caller's context and
// before the command is written; an error closes that connection and fails the
// call, so a read cancelled during the setup is never sent.
func refuseAnEndedRead(ctx context.Context, _ *redis.Conn) error {
	return ctx.Err()
}

// endedAfterReply returns the caller's context error when the call came back
// with a reply after the context ended. The reply still proves the connection
// works, so it is recorded for classifyConnError as it would have been.
func (s *sdkAPI) endedAfterReply(ctx context.Context, operation string, replyErr error) error {
	ctxErr := ctx.Err()
	if ctxErr == nil {
		return nil
	}
	if replyErr == nil || isProofOfLife(replyErr, classifyFalkorError(operation, replyErr)) {
		s.everConnected.Store(true)
	}
	return ctxErr
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
