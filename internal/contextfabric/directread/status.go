package directread

// The status vocabulary of run_operation (CHAOS-7036 design section D.7).
// Three independent fields describe one call. They are closed sets: a new
// member is a contract change (see the repository AGENTS.md enum rule).
//
// The rules they carry, restated so a caller does not have to open the
// design:
//
//   - CallOperationUnavailable is a 404 from the ops query service: the
//     document is not registered OR its routing row is off. acr cannot tell
//     which, and it does not retry on another path.
//   - CompletenessDeclaredComplete means the read returned the whole set it
//     was asked for (no page cut, no row cap reached, no disclosure field
//     fired). CompletenessDeclaredPartial carries a CompletenessReason. An
//     answer whose verdict needs a field or a cap the read lacks is
//     CompletenessUnknown (OperationPolicy.Verdict).
//   - An empty result with unknown completeness is ResultEmptyUnverified.
//     It is never "no data" and never "healthy".

// CallStatus is the terminal state of one run_operation call.
type CallStatus string

const (
	// CallServed: the upstream answered and the edge admitted the answer.
	CallServed CallStatus = "served"
	// CallRefused: acr refused the call; no upstream request was made, or
	// the edge refused the upstream answer (row outside the grant). The
	// refusal carries a RefusalCode.
	CallRefused CallStatus = "refused"
	// CallOperationUnavailable: the query service answered 404.
	CallOperationUnavailable CallStatus = "operation_unavailable"
	// CallUpstreamError: the query service answered with an error.
	CallUpstreamError CallStatus = "upstream_error"
	// CallUpstreamTimeout: the deadline passed before the answer.
	CallUpstreamTimeout CallStatus = "upstream_timeout"
)

// CallStatusVocabulary is the closed set of call states.
func CallStatusVocabulary() [5]CallStatus {
	return [5]CallStatus{CallServed, CallRefused, CallOperationUnavailable, CallUpstreamError, CallUpstreamTimeout}
}

// Completeness states whether the read covered what it was asked for.
type Completeness string

const (
	CompletenessDeclaredComplete Completeness = "declared_complete"
	CompletenessDeclaredPartial  Completeness = "declared_partial"
	CompletenessUnknown          Completeness = "unknown"
)

// CompletenessVocabulary is the closed set of completeness states.
func CompletenessVocabulary() [3]Completeness {
	return [3]Completeness{CompletenessDeclaredComplete, CompletenessDeclaredPartial, CompletenessUnknown}
}

// ResultState states whether data came back and what an empty answer means.
type ResultState string

const (
	ResultData            ResultState = "data"
	ResultEmptyUnverified ResultState = "empty_unverified"
	ResultEmptyDeclared   ResultState = "empty_declared"
)

// ResultStateVocabulary is the closed set of result states.
func ResultStateVocabulary() [3]ResultState {
	return [3]ResultState{ResultData, ResultEmptyUnverified, ResultEmptyDeclared}
}

// ResultStateFor applies the D.7 rule: a non-empty answer is data; an empty
// answer is empty_declared only when the read was declared complete,
// and empty_unverified in every other case.
func ResultStateFor(empty bool, completeness Completeness) ResultState {
	switch {
	case !empty:
		return ResultData
	case completeness == CompletenessDeclaredComplete:
		return ResultEmptyDeclared
	default:
		return ResultEmptyUnverified
	}
}
