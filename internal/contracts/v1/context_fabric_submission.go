package v1

import (
	"fmt"
	"slices"
	"time"
)

// Submission identity on the create-investigation route (CHAOS-11912, Ask Dev
// plan §2.7.5). A client names each user submit with a submission id; the
// service runs it once, bound to the request's semantic digest and to the
// authenticated principal, and answers every repeat of the id from the
// record without running it again.
//
// The header is ContextFabricSubmissionIDHeader and its grammar is
// ValidContextFabricClientCorrelationID (both declared with the event stream
// contract, whose `accepted` event echoes the id). Optional, except for a
// browser-class credential (CHAOS-12093), whose create without one is
// ErrorCodeSubmissionRequired: a request
// without it is run as before, with no submission record and an
// organization-bearer result.
const (
	// ContextFabricSubmissionInProgressRetryAfterSeconds is the Retry-After
	// of a 409 submission_in_progress: the cadence a client polls at.
	ContextFabricSubmissionInProgressRetryAfterSeconds = 3

	// ErrorCodeSubmissionMismatch: 409, the submission id was already used
	// for a request with a different semantic digest. Not retryable with
	// that id.
	ErrorCodeSubmissionMismatch = "submission_mismatch"
	// ErrorCodeSubmissionInProgress: 409, the submission's run is still
	// live. Retryable, after Retry-After.
	ErrorCodeSubmissionInProgress = "submission_in_progress"
	// ErrorCodeSubmissionUnsaved: 409, the submission was answered but the
	// answer was not stored, so there is nothing to replay. Not retryable
	// with that id; ask again with a new one.
	ErrorCodeSubmissionUnsaved = "submission_unsaved"
	// ErrorCodeSubmissionAbandoned: 409, the submission's run ended without
	// an answer, or ran past its budget plus 30 seconds. Not retryable with
	// that id; ask again with a new one.
	ErrorCodeSubmissionAbandoned = "submission_abandoned"
	// ErrorCodeSubmissionRequired: 400, a create by a browser-class
	// credential (CHAOS-12093) that names no single well-formed
	// X-ACR-Submission-ID: it is missing, malformed or repeated. Refused
	// before admission and model work; nothing was admitted or run. Not
	// retryable as sent: send one submission id.
	ErrorCodeSubmissionRequired = "submission_required"
	// ContextFabricSubmissionRequiredMessage is the message of a
	// submission_required refusal.
	ContextFabricSubmissionRequiredMessage = "This credential must name one well-formed submission id on every create"

	// ErrorCodeConcurrencyLimited: 429, the submission would exceed a shared
	// running-run limit (CHAOS-11914). details.limit names which: principal
	// or org. Retryable, after Retry-After; nothing was admitted.
	ErrorCodeConcurrencyLimited = "concurrency_limited"
	// ErrorCodeAllowanceExhausted: 429, the organization admitted its monthly
	// allowance of runs on the deployment's model. Retry-After is the start
	// of the next calendar month UTC; nothing was admitted.
	ErrorCodeAllowanceExhausted = "allowance_exhausted"
	// ContextFabricConcurrencyLimitDetailsKey is the error.details key that
	// names the limit a concurrency_limited refusal hit.
	ContextFabricConcurrencyLimitDetailsKey = "limit"
)

// ContextFabricConcurrencyLimitVocabulary is the closed set of
// details.limit values.
func ContextFabricConcurrencyLimitVocabulary() [2]string {
	return [2]string{"principal", "org"}
}

// ContextFabricSubmissionErrorCodes is the closed set of submission refusal
// codes, each a 409 of the create-investigation route.
func ContextFabricSubmissionErrorCodes() [4]string {
	return [4]string{ErrorCodeSubmissionMismatch, ErrorCodeSubmissionInProgress, ErrorCodeSubmissionUnsaved, ErrorCodeSubmissionAbandoned}
}

// ContextFabricSubmissionStatusSchema is the schema_version of the
// submission status read (CHAOS-11913).
const ContextFabricSubmissionStatusSchema = "context_fabric_submission_status.v1"

// ContextFabricSubmissionState is the closed state of a submission.
type ContextFabricSubmissionState string

const (
	// ContextFabricSubmissionRunning: the run is live.
	ContextFabricSubmissionRunning ContextFabricSubmissionState = "running"
	// ContextFabricSubmissionDone: result_id names the final result.
	ContextFabricSubmissionDone ContextFabricSubmissionState = "done"
	// ContextFabricSubmissionDoneUnsaved: answered, not stored; no result_id.
	ContextFabricSubmissionDoneUnsaved ContextFabricSubmissionState = "done_unsaved"
	// ContextFabricSubmissionAbandoned: ended without an answer, or ran past
	// its budget plus 30 seconds.
	ContextFabricSubmissionAbandoned ContextFabricSubmissionState = "abandoned"
)

// ContextFabricSubmissionStateVocabulary is the closed set of states, in the
// published enum's order.
func ContextFabricSubmissionStateVocabulary() [4]ContextFabricSubmissionState {
	return [4]ContextFabricSubmissionState{ContextFabricSubmissionRunning, ContextFabricSubmissionDone, ContextFabricSubmissionDoneUnsaved, ContextFabricSubmissionAbandoned}
}

// ContextFabricSubmissionStatus is the body of
// GET /api/v1/context-fabric/investigations/submissions/{submission_id}: the
// caller's own submission record. A client that lost a response polls it and
// replays result_id when the state is done.
type ContextFabricSubmissionStatus struct {
	SchemaVersion    string                       `json:"schema_version"`
	SubmissionID     string                       `json:"submission_id"`
	State            ContextFabricSubmissionState `json:"state"`
	ReservedResultID string                       `json:"reserved_result_id"`
	// ResultID is present exactly when State is done; it is the reserved id,
	// or the reused answer's id when the run served one.
	ResultID string `json:"result_id,omitempty"`
	// ThreadID is the X-ACR-Thread-ID the submission was admitted with, when
	// it named one.
	ThreadID  string    `json:"thread_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate holds the status to its schema.
func (s ContextFabricSubmissionStatus) Validate() error {
	states := ContextFabricSubmissionStateVocabulary()
	switch {
	case s.SchemaVersion != ContextFabricSubmissionStatusSchema:
		return fmt.Errorf("submission status schema_version must be %q", ContextFabricSubmissionStatusSchema)
	case !ValidContextFabricClientCorrelationID(s.SubmissionID):
		return fmt.Errorf("submission status submission_id violates v1 bounds")
	case !slices.Contains(states[:], s.State):
		return fmt.Errorf("submission status state is not a member of the closed vocabulary")
	case !stringLengthBetween(s.ReservedResultID, 8, 256):
		return fmt.Errorf("submission status reserved_result_id violates v1 bounds")
	case (s.State == ContextFabricSubmissionDone) != (s.ResultID != ""):
		return fmt.Errorf("submission status result_id is present exactly when the state is done")
	case s.ResultID != "" && !stringLengthBetween(s.ResultID, 8, 256):
		return fmt.Errorf("submission status result_id violates v1 bounds")
	case s.ThreadID != "" && !ValidContextFabricClientCorrelationID(s.ThreadID):
		return fmt.Errorf("submission status thread_id violates v1 bounds")
	case s.UpdatedAt.IsZero():
		return fmt.Errorf("submission status updated_at is required")
	}
	return nil
}
