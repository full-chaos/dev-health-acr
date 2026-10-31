package v1

import (
	"regexp"
	"strconv"
)

// The run lease contract (CHAOS-11949, Ask Dev plan §2.7.9). With the
// deployment's run lease on (ACR_CONTEXT_FABRIC_RUN_LEASE_ENABLED), a create
// request that asks for the event stream and names X-ACR-Submission-ID runs
// under a lease ACR holds, not under its connection:
//
//   - a disconnect does not cancel the run;
//   - GET ContextFabricSubmissionEventsPath (Accept: text/event-stream)
//     attaches to the run's stream from either replica. Without Last-Event-ID
//     it replays from `accepted`; with it, from the event after that seq. The
//     events are the run's own: the same seq, request_id and data. A cursor
//     the log cannot serve -- malformed, never written, older than the kept
//     window, or at or past the terminal -- is 410 cursor_expired; the client
//     then reads the submission status and replays the result by id;
//   - POST ContextFabricSubmissionCancelPath cancels the run. 202 when the
//     run is running (the first request records t_req; a repeat keeps it),
//     204 when the submission is already final. The run ends with
//     error{code: canceled} within ContextFabricRunCancelEffectiveSeconds,
//     unless its answer had already finished; a finished answer is kept;
//   - a run whose replica died ends with error{code: run_lost} once its lease
//     has ended (ContextFabricRunLeaseSeconds).
//
// The attach stream's terminal is a marker of the run's own terminal: a
// `result` carries the stored result exactly as GET by id serves it to the
// caller now (the stored-result gate decides it again), an `error` carries the
// run's error.v1 envelope; an answer that was served but not stored is
// error{code: submission_unsaved}. The log is bounded: it keeps the latest
// ContextFabricInvestigationRunEventLogMaxEvents non-terminal events of a run
// and its terminal.
//
// Both routes are the caller's own, like the submission status read: another
// principal's, an unknown, a malformed id, and a turn of a closed thread are
// 404 not_found, one body.

// ContextFabricInvestigationRunEventLogMaxEvents is the per-run log cap.
const ContextFabricInvestigationRunEventLogMaxEvents = 128

// ContextFabricRunLeaseSeconds is the lease term: a run whose replica stops
// renewing ends as run_lost at most this long after the last renewal (plus
// the takeover cadence).
const ContextFabricRunLeaseSeconds = 15

// ContextFabricRunCancelEffectiveSeconds bounds t_eff - t_req.
const ContextFabricRunCancelEffectiveSeconds = 5

// ContextFabricLastEventIDHeader is the SSE resume header of an attach.
const ContextFabricLastEventIDHeader = "Last-Event-ID"

// Error codes of the run lease.
const (
	// ErrorCodeCanceled: the run ended because its owner canceled it. 409,
	// not retryable under the submission id.
	ErrorCodeCanceled = "canceled"
	// ErrorCodeRunLost: the replica running it stopped and its lease ended;
	// the run ended without an answer. 503, not retryable under the
	// submission id (ask again with a new one).
	ErrorCodeRunLost = "run_lost"
	// ErrorCodeCursorExpired: the attach cursor (Last-Event-ID) cannot be
	// served. 410, not retryable with that cursor.
	ErrorCodeCursorExpired = "cursor_expired"
)

// The error.v1 status and message of each code.
const (
	ContextFabricCanceledStatus       = 409
	ContextFabricCanceledMessage      = "The investigation was canceled"
	ContextFabricRunLostStatus        = 503
	ContextFabricRunLostMessage       = "The investigation stopped: the service instance running it is gone"
	ContextFabricCursorExpiredStatus  = 410
	ContextFabricCursorExpiredMessage = "The event stream cannot resume from this cursor; read the submission status instead"
)

// ContextFabricRunErrorCodes is the closed set of run lease codes.
func ContextFabricRunErrorCodes() [3]string {
	return [3]string{ErrorCodeCanceled, ErrorCodeRunLost, ErrorCodeCursorExpired}
}

// ContextFabricRunTerminalEnvelope is the error.v1 envelope of a canceled or
// lost run, for requestID (the run's own).
func ContextFabricRunTerminalEnvelope(requestID, code string) ErrorEnvelope {
	detail := ErrorDetail{Code: code}
	switch code {
	case ErrorCodeCanceled:
		detail.HTTPStatus, detail.Message = ContextFabricCanceledStatus, ContextFabricCanceledMessage
	case ErrorCodeRunLost:
		detail.HTTPStatus, detail.Message = ContextFabricRunLostStatus, ContextFabricRunLostMessage
	}
	return ErrorEnvelope{SchemaVersion: ErrorSchema, RequestID: requestID, Error: detail}
}

// lastEventIDPattern is the decimal grammar of a cursor: no sign, no leading
// zero, no space.
var lastEventIDPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,15})$`)

// ParseContextFabricLastEventID reads one Last-Event-ID value: a seq from 0
// to the largest exact JavaScript integer. ok is false for anything else.
func ParseContextFabricLastEventID(value string) (seq int64, ok bool) {
	if !lastEventIDPattern.MatchString(value) {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed > contextFabricEventMaxInteger {
		return 0, false
	}
	return parsed, true
}
