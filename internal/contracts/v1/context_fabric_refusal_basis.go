package v1

// CHAOS-5442: the closed vocabulary naming WHY the server refused to act on
// a question's frame -- and, since the continuation member, on the prior
// context a window-only continuation would have carried.
//
// WHY THIS IS NOT ContextFabricTerminalReason. That vocabulary names the
// CHANNEL a non-complete result explained itself through -- limitation,
// degraded reason, warning, clarification, or undisclosed. It answers "where
// did the engine put its explanation", never "what did the engine decide".
// A frame refusal reported as `limitation_disclosed` is therefore correctly
// classified and still says nothing: the reader learns that a sentence
// exists, not that the question named a population no discovery arm can
// build. The two vocabularies are orthogonal and a refusing terminal carries
// both.
//
// WHY IT IS ON THE WIRE AT ALL. The refusal was already decided (the frame
// gate, internal/contextfabric.DecideFrameGate) and already observable at
// Info on the frame-validation line. What it was not was READABLE by anyone
// consuming the answer: six corpus rows terminated `no_match` carrying the
// ordinary empty-pool sentence, so a consumer -- and the corpus instrument --
// could not tell "this kind has no discovery arm" from "this graph is
// empty". MISSING IS NOT NONE: an absent RefusalBasis means the turn was not
// refused; a present one names the refusal.
type ContextFabricRefusalBasis string

const (
	// ContextFabricRefusalBasisMemberKindUnservable: the frame validated
	// and then declared a member kind NO DISCOVERY ARM SERVES. No amount
	// of retrieval can produce this answer, which is why the refusal
	// happens above retrieval rather than as an empty result below it.
	//
	// The spelling is deliberately IDENTICAL to the internal
	// CohortMemberKindUnservable / CohortKindMemberKindUnservable token
	// the gate decides on and the Info line already prints. One name for
	// one fact across the log line, the wire and the corpus declaration:
	// a reader correlating a served refusal with its log line must not
	// have to translate.
	ContextFabricRefusalBasisMemberKindUnservable ContextFabricRefusalBasis = "member_kind_unservable"
	// ContextFabricRefusalBasisFrameInvariantViolated: the frame failed a
	// frame invariant and one bounded repair did not fix it. The design's
	// §13.1 terminal for this state is "frame = refused, family =
	// unclassified, refuse to guess".
	//
	// WHICH invariant failed is deliberately NOT a member here. The
	// invariant vocabulary is a server-internal validation detail whose
	// members are added and renamed on their own schedule; promoting it
	// to the wire would make every future invariant a contract change,
	// and the caller's question is "did the server refuse my frame", not
	// "which of the server's internal checks fired". The invariant name
	// stays on the receipt and the frame-validation log line, which is
	// where an operator reads it.
	ContextFabricRefusalBasisFrameInvariantViolated ContextFabricRefusalBasis = "frame_invariant_violated"
	// ContextFabricRefusalBasisUnspecified: the gate refused and its
	// outcome is not one this vocabulary names.
	//
	// A FAIL-CLOSED MEMBER, not a placeholder. FrameGate.Refuses() refuses
	// on any outcome it does not recognise -- deliberately, so a future
	// member cannot be admitted by default -- and a refusal that reached
	// the wire with an EMPTY basis would be indistinguishable from a turn
	// that was never refused, which is the exact collapse this whole field
	// exists to end. So an unrecognised refusal says so, loudly, rather
	// than disappearing. Reaching it in production means a gate member was
	// added without a line here.
	ContextFabricRefusalBasisUnspecified ContextFabricRefusalBasis = "unspecified"
	// ContextFabricRefusalBasisContinuationContextUnverifiable: the request
	// was a window-only continuation -- the same question, one redeemed
	// window offer and nothing else changed -- and the server could not
	// verify the prior turn's semantic context it would continue. The prior
	// result could not be read, was built on a different graph epoch, was
	// recorded under a different definition standard, or its reading could
	// not be expressed as a valid frame for this turn.
	//
	// A REFUSAL, NOT A FRESH READING. Answering under a new interpretation
	// would serve a reading the caller never confirmed, beside a window the
	// caller confirmed for a different reading. So the server stops above
	// retrieval, reads no canonical fact, and states that a fresh
	// investigation is required.
	//
	// NOT A FRAME REFUSAL. The question's own frame was not refused and no
	// frame invariant was judged against the question the caller asked. The
	// member describes the CARRIER, so neither of the two frame members above
	// is true of it, and it has its own fixed sentence
	// (ContextFabricContinuationContextUnverifiableLimitation) rather than
	// the member-kind sentence, which names a population this refusal is not
	// about.
	ContextFabricRefusalBasisContinuationContextUnverifiable ContextFabricRefusalBasis = "continuation_context_unverifiable"
	// ContextFabricRefusalBasisDeclaredKindUnmatched: the question's frame
	// declared a subject kind, retrieval ran, and NOTHING it could offer
	// carried that kind -- no kind option, no anchor, handle or candidate
	// option, and no subject candidate. The server stops rather than ask a
	// question it has supplied no means of answering.
	//
	// IT IS NOT member_kind_unservable, and the distinction is the whole
	// reason this member exists rather than reusing that one. That member
	// says NO DISCOVERY ARM SERVES the kind -- a statement about the
	// service, decided above retrieval, true of every question that names
	// that kind. This one says this ORG'S GRAPH, for THESE TERMS, offered
	// nothing of a kind the service serves perfectly well: measured on the
	// 2026-09-12 corpus, three other rows in the same replicate resolved
	// projects while the two rows this member describes could not, because
	// the token they named matches no project identity. Filing the second
	// under the first would tell an operator to build a discovery arm that
	// already exists.
	//
	// NOT A FRAME REFUSAL either, and deliberately outside
	// ValidContextFabricFrameRefusalBasis below for the same reason
	// continuation_context_unverifiable is: the frame VALIDATED and its
	// gate passed. What failed is retrieval's ability to offer anything the
	// frame's own declaration could accept, which is decided after seventy
	// candidates have been ranked, not before the search runs. The
	// member-kind sentence would therefore be false of it, so it carries
	// its own (ContextFabricDeclaredKindUnmatchedLimitation).
	ContextFabricRefusalBasisDeclaredKindUnmatched ContextFabricRefusalBasis = "declared_kind_unmatched"
)

var contextFabricRefusalBases = [...]ContextFabricRefusalBasis{
	ContextFabricRefusalBasisMemberKindUnservable,
	ContextFabricRefusalBasisFrameInvariantViolated,
	ContextFabricRefusalBasisUnspecified,
	ContextFabricRefusalBasisContinuationContextUnverifiable,
	ContextFabricRefusalBasisDeclaredKindUnmatched,
}

// ValidContextFabricFrameRefusalBasis reports whether a basis is one of the
// members that describe a refusal of the QUESTION'S OWN FRAME -- the members
// the frame gate can produce and the member-kind refusal sentence
// (ContextFabricRefusalBasisLimitation) may name.
//
// AN ALLOW-LIST OF THE FRAME MEMBERS. The member-kind sentence says the
// question "asked about a population of <kind>, which this service has no way
// to enumerate" -- a statement about the question's frame -- and the frame gate
// maps only its own verdicts. The continuation member refuses for a different
// reason: a kind sentence naming it would be false, and if the recogniser
// accepted that sentence it would be classified as a service disclosure that
// nothing may displace; a gate that produced it would claim a frame refusal
// that no frame decision took. A member added later is excluded until someone
// decides it describes the frame.
func ValidContextFabricFrameRefusalBasis(value ContextFabricRefusalBasis) bool {
	switch value {
	case ContextFabricRefusalBasisMemberKindUnservable,
		ContextFabricRefusalBasisFrameInvariantViolated,
		ContextFabricRefusalBasisUnspecified:
		return true
	default:
		return false
	}
}

// ContextFabricRefusalBasisCount is the closed vocabulary's size.
const ContextFabricRefusalBasisCount = len(contextFabricRefusalBases)

// ContextFabricRefusalBasisVocabulary returns the closed vocabulary in
// declared order.
func ContextFabricRefusalBasisVocabulary() [ContextFabricRefusalBasisCount]ContextFabricRefusalBasis {
	return contextFabricRefusalBases
}

// ValidContextFabricRefusalBasis reports membership of the NON-EMPTY
// members.
//
// The empty string is NOT a member and that is the whole point: it is the
// absence of a refusal, checked by the caller against the rest of the
// document rather than smuggled in here as a fourth quasi-member. An
// allow-list, never a deny-list with an else -- a deny-list admits the next
// member and the zero value by default.
func ValidContextFabricRefusalBasis(value ContextFabricRefusalBasis) bool {
	for _, member := range contextFabricRefusalBases {
		if member == value {
			return true
		}
	}
	return false
}
