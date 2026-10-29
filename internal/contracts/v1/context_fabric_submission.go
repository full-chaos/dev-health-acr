package v1

// Submission identity on the create-investigation route (CHAOS-11912, Ask Dev
// plan §2.7.5). A client names each user submit with a submission id; the
// service runs it once, bound to the request's semantic digest and to the
// authenticated principal, and answers every repeat of the id from the
// record without running it again.
//
// The header is ContextFabricSubmissionIDHeader and its grammar is
// ValidContextFabricClientCorrelationID (both declared with the event stream
// contract, whose `accepted` event echoes the id). Optional: a request
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
)

// ContextFabricSubmissionErrorCodes is the closed set of submission refusal
// codes, each a 409 of the create-investigation route.
func ContextFabricSubmissionErrorCodes() [4]string {
	return [4]string{ErrorCodeSubmissionMismatch, ErrorCodeSubmissionInProgress, ErrorCodeSubmissionUnsaved, ErrorCodeSubmissionAbandoned}
}
