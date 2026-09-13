package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-5640: `status`/completeness are SERVER-OWNED, and the authority for
// that ownership is the requirement-outcome derivation
// (DeriveContextFabricAnswerCompletenessState over result.Completeness.Outcomes)
// -- never status_shadow.go's DeriveServerStatus unchanged. That gate measures
// the served document against the FRAME's obligations, a proxy stated as such
// in its own header. Its own arm, "only a model complete can be
// contradicted", also means it can never distinguish partial from degraded.
//
// TWO THINGS THIS FILE KEEPS SEPARATE, ON PURPOSE. EXECUTION DISPOSITION --
// did the turn answer, ask for clarification, refuse, or find no match -- is
// not the same question as REQUIREMENT COMPLETENESS -- how much of an answer
// is here. A pending clarification is not an incomplete answer, and a
// refusal is not a degraded one; both are the wrong SHAPE for a completeness
// question, not a low score on the right one. Collapsing the two was never
// this package's mistake -- the wire vocabulary already keeps them apart --
// but a derivation reading only `result.Status` can make that mistake by
// omission, so AnswerDisposition is named here as its own value, checked
// FIRST, and completeness is only ever asked about a disposition of
// `answer`.
//
// NEVER ASSIGNED BY SPELLING. ContextFabricAnswerCompletenessState and
// ContextFabricInvestigationStatus happen to share three token spellings
// (complete/partial/degraded) for unrelated reasons -- one is "what the
// outcome rows add up to", the other is "what kind of turn this was" -- and
// a string cast between them would silently keep working right up to the day
// either vocabulary renamed or added a member out of step with the other.
// answerCompletenessStateToStatus is the one explicit, total table that
// crosses between them; nothing else in this file compares the two
// vocabularies any other way.

// AnswerDisposition names WHAT KIND of terminal exit a turn reached. CLOSED,
// four members.
type AnswerDisposition string

const (
	// AnswerDispositionAnswer: the turn produced an answer -- model status
	// complete, partial or degraded. Completeness is a question that can be
	// asked about this disposition, and only this one.
	AnswerDispositionAnswer AnswerDisposition = "answer"
	// AnswerDispositionClarification: the turn is pending a clarification.
	// Not an incomplete answer -- nothing was served to hold completeness
	// against.
	AnswerDispositionClarification AnswerDisposition = "clarification"
	// AnswerDispositionRefusal: the turn was refused, with a named basis
	// (result.RefusalBasis is non-empty). Distinct from a plain no_match:
	// a refusal is a DECISION not to act; an ordinary no_match is the
	// decision having found nothing to act on.
	AnswerDispositionRefusal AnswerDisposition = "refusal"
	// AnswerDispositionNoMatch: the turn found no matching subject and
	// carries no refusal basis.
	AnswerDispositionNoMatch AnswerDisposition = "no_match"
)

var answerDispositions = [...]AnswerDisposition{
	AnswerDispositionAnswer,
	AnswerDispositionClarification,
	AnswerDispositionRefusal,
	AnswerDispositionNoMatch,
}

// AnswerDispositionCount is the vocabulary size.
const AnswerDispositionCount = len(answerDispositions)

// AnswerDispositionVocabulary returns the closed vocabulary in published
// order. An ARRAY return, so the caller gets a copy.
func AnswerDispositionVocabulary() [AnswerDispositionCount]AnswerDisposition {
	return answerDispositions
}

// ValidAnswerDisposition reports membership.
func ValidAnswerDisposition(value AnswerDisposition) bool {
	for _, member := range answerDispositions {
		if member == value {
			return true
		}
	}
	return false
}

// DeriveAnswerDisposition classifies result's terminal exit from
// result.Status and result.RefusalBasis alone -- never from
// result.Completeness, which is the other half this file keeps distinct.
//
// TOTAL over the five ContextFabricInvestigationStatus members: asserted by
// TestDeriveAnswerDispositionIsTotalOverInvestigationStatus. The default arm
// is unreached over that closed vocabulary; it exists so a status this
// package does not yet know about is reported as the least-claiming
// disposition rather than silently folded into `answer`.
func DeriveAnswerDisposition(result InvestigationResult) AnswerDisposition {
	// RefusalBasis is checked FIRST, ahead of Status, and this order is
	// load-bearing rather than cosmetic. The wire validator forbids
	// RefusalBasis only alongside `complete` (validateCompleteness's "a
	// refusal cannot claim to have answered"), and RefusalBasis's own doc
	// comment (contracts/v1/context_fabric_completeness.go) says it is
	// ORTHOGONAL to every other field, naming the DECISION rather than a
	// channel -- so a status-first switch would reclassify a producer that
	// pairs a refusal with `clarification_required`, `partial` or
	// `degraded` as that status's disposition instead of a refusal,
	// running the completeness derivation over a turn that was never
	// retrieved. Checking the orthogonal field first cannot lose the
	// `answer` case: that pairing is already refused at the wire.
	if result.RefusalBasis != "" {
		return AnswerDispositionRefusal
	}
	switch result.Status {
	case InvestigationClarificationRequired:
		return AnswerDispositionClarification
	case InvestigationNoMatch:
		return AnswerDispositionNoMatch
	case InvestigationComplete, InvestigationPartial, InvestigationDegraded:
		return AnswerDispositionAnswer
	default:
		return AnswerDispositionNoMatch
	}
}

// CompletenessAuthorityBasis names WHY DeriveCompletenessAuthority reports
// the ServerState it does, or why it declines to report one at all. CLOSED,
// three members.
type CompletenessAuthorityBasis string

const (
	// CompletenessAuthorityBasisOutcomeDerived: disposition is `answer` and
	// the outcome set produced a real state (complete/partial/degraded).
	// ServerState carries that state.
	CompletenessAuthorityBasisOutcomeDerived CompletenessAuthorityBasis = "outcome_derived"
	// CompletenessAuthorityBasisUnavailable: disposition is `answer`, but
	// the outcome set is empty -- DeriveContextFabricAnswerCompletenessState
	// itself reports `not_derived`. A legacy stored result predating the
	// outcome layer, or a half-stamped one, reads this way. THE HONEST
	// ANSWER, never fabricated agreement: an answer with no semantic state
	// discloses that it has none rather than being reported the strongest
	// completeness there is.
	CompletenessAuthorityBasisUnavailable CompletenessAuthorityBasis = "unavailable"
	// CompletenessAuthorityBasisNotAnAnswer: disposition is clarification,
	// refusal, or no_match. Completeness is not a question this derivation
	// asks about those dispositions at all -- see this file's own header.
	CompletenessAuthorityBasisNotAnAnswer CompletenessAuthorityBasis = "not_an_answer"
)

var completenessAuthorityBases = [...]CompletenessAuthorityBasis{
	CompletenessAuthorityBasisOutcomeDerived,
	CompletenessAuthorityBasisUnavailable,
	CompletenessAuthorityBasisNotAnAnswer,
}

// CompletenessAuthorityBasisCount is the vocabulary size.
const CompletenessAuthorityBasisCount = len(completenessAuthorityBases)

// CompletenessAuthorityBasisVocabulary returns the closed vocabulary in
// published order.
func CompletenessAuthorityBasisVocabulary() [CompletenessAuthorityBasisCount]CompletenessAuthorityBasis {
	return completenessAuthorityBases
}

// ValidCompletenessAuthorityBasis reports membership.
func ValidCompletenessAuthorityBasis(value CompletenessAuthorityBasis) bool {
	for _, member := range completenessAuthorityBases {
		if member == value {
			return true
		}
	}
	return false
}

// CompletenessAuthorityVersion identifies THIS derivation series.
//
// It is reported on every observation for the same reason
// ServerStatusShadowVersion is on status_shadow.go's: a disagreement rate
// measured under one rule must never be spliced with one measured under
// another. This is a NEW series, not a continuation of
// ServerStatusShadowVersion's -- the two measure different things (this one
// reads the outcome-derivation authority the design record names; that one
// reads a frame-obligations proxy the design record says is not it) -- so
// restarting under a new version, rather than bumping the old one, is what
// keeps that fact visible to whoever reads the series later.
const CompletenessAuthorityVersion = "completeness-authority.outcome-derived.v1"

// CompletenessAuthorityObservation is ONE observation: the model's status,
// the disposition it reduces to, the server's outcome-derived completeness
// state when that disposition can carry one, and whether the two authorities
// disagree.
type CompletenessAuthorityObservation struct {
	// ModelStatus is the status the served document actually carries at the
	// point this observation was taken -- copied, never mutated by taking
	// the observation itself (DeriveCompletenessAuthority is pure).
	ModelStatus InvestigationStatus
	// Disposition is the execution disposition DeriveAnswerDisposition
	// derived from ModelStatus and RefusalBasis.
	Disposition AnswerDisposition
	// Basis names why ServerState does or does not carry a value.
	Basis CompletenessAuthorityBasis
	// ServerState is the outcome-derivation's own state. Empty unless
	// Basis is CompletenessAuthorityBasisOutcomeDerived.
	ServerState contractsv1.ContextFabricAnswerCompletenessState
	// Derived is whether ServerState carries a real value. Distinguishes
	// "the server evaluated this and it says X" from "the server declined
	// to evaluate this at all" the same way status_shadow.go's own Derived
	// field does -- see that field's doc comment.
	Derived bool
	// Disagreed is true whenever a value WAS derived and the status it maps
	// to differs from ModelStatus, in EITHER direction.
	//
	// DELIBERATELY SYMMETRIC, unlike status_shadow.go's Disagreed (which
	// only ever contradicts a model `complete`). That asymmetry is exactly
	// why the old shadow could not decide a partial-versus-degraded
	// question: a model-authored partial staying partial and a
	// model-authored degraded staying degraded both read `Disagreed=false`
	// under that rule regardless of what the outcome rows actually said.
	// This field reads true for THAT case too: distinguishing partial from
	// degraded requires comparing against all three answer labels, not
	// only the strongest one. The
	// asymmetry survives only in the FLIP (applyServerCompletenessAuthority),
	// which has a safety reason of its own to keep it -- see that
	// function's doc comment.
	Disagreed bool
	// Version identifies this derivation series.
	Version string
}

// DeriveCompletenessAuthority is the measurement: what the outcome-derivation
// authority would say about this result's completeness, beside what the
// model's status already says.
//
// PURE: reads result, mutates nothing -- the same discipline
// ComputeAnswerCompleteness and status_shadow.go's DeriveServerStatus both
// keep.
//
// It reads result.Completeness.Outcomes, not result.Completeness.State: the
// outcome ROWS are the one authority (see context_fabric_requirement_outcome.go's
// own header, "APPEND... DERIVE LAST"), and re-deriving from them here rather
// than trusting a State a caller might have stamped at an earlier stage is
// what makes this function agree with ComputeAnswerCompleteness on any
// result the two are ever asked about together.
func DeriveCompletenessAuthority(result InvestigationResult) CompletenessAuthorityObservation {
	observation := CompletenessAuthorityObservation{
		ModelStatus: result.Status,
		Disposition: DeriveAnswerDisposition(result),
		Version:     CompletenessAuthorityVersion,
	}
	if observation.Disposition != AnswerDispositionAnswer {
		// A pending clarification is not an incomplete answer, and a
		// refusal is not a degraded one. Nothing here is fabricated: no
		// ServerState, no Derived, no Disagreed.
		observation.Basis = CompletenessAuthorityBasisNotAnAnswer
		return observation
	}
	state := contractsv1.DeriveContextFabricAnswerCompletenessState(result.Completeness.Outcomes)
	if state == contractsv1.ContextFabricAnswerCompletenessNotDerived || state == "" {
		// A legacy row, or one whose outcome set was never stamped. The
		// honest report is "no semantic state", not the vacuous `complete`
		// an empty set derives under any other reading -- see
		// ContextFabricAnswerCompletenessNotDerived's own doc comment.
		observation.Basis = CompletenessAuthorityBasisUnavailable
		return observation
	}
	observation.Basis = CompletenessAuthorityBasisOutcomeDerived
	observation.ServerState = state
	observation.Derived = true
	if mapped, ok := answerCompletenessStateToStatus(state); ok {
		observation.Disagreed = mapped != result.Status
	}
	return observation
}

// answerCompletenessStateToStatus is the ONE explicit, total crossing
// between the outcome-completeness vocabulary and the investigation-status
// vocabulary. See this file's own header for why a cast is refused here.
//
// TOTAL over ContextFabricAnswerCompletenessStateVocabulary(): asserted by
// TestAnswerCompletenessStateToStatusIsTotal. ContextFabricAnswerCompletenessNotDerived
// has no status to map to -- callers check Derived/Basis before consulting
// the mapped value, never the other way round.
func answerCompletenessStateToStatus(state contractsv1.ContextFabricAnswerCompletenessState) (InvestigationStatus, bool) {
	switch state {
	case contractsv1.ContextFabricAnswerCompletenessComplete:
		return InvestigationComplete, true
	case contractsv1.ContextFabricAnswerCompletenessPartial:
		return InvestigationPartial, true
	case contractsv1.ContextFabricAnswerCompletenessDegraded:
		return InvestigationDegraded, true
	case contractsv1.ContextFabricAnswerCompletenessNotDerived:
		return "", false
	default:
		return "", false
	}
}

// applyServerCompletenessAuthority is the gated FLIP: when enabled, the
// server's own outcome-derived completeness state may CORRECT the served
// status -- never route, never widen a contract, and default OFF
// (EngineOptions.ServerCompletenessAuthorityEnabled's own doc comment).
//
// DOWNGRADE-ONLY, deliberately asymmetric even though the MEASUREMENT above
// is symmetric. Two guardrails, both carried over from status_shadow.go's
// DeriveServerStatus ("A NON-COMPLETE MODEL STATUS IS NOT SECOND-GUESSED"):
//
//  1. Only a model-claimed `complete` may be corrected. This finds answers
//     the model called COMPLETE that the outcome rows say were not; a
//     correction in the other direction -- promoting a model-authored
//     partial or degraded UP to complete -- would let the model's own
//     preferred label override a lower server verdict, which defeats the
//     purpose of an independent authority.
//  2. A non-answer disposition is never touched. A pending clarification is
//     not an incomplete answer and a refusal is not a degraded one; this
//     function only ever looks at a result whose disposition is `answer`,
//     and only after DeriveCompletenessAuthority has already refused to
//     derive anything for the other three.
//
// result.Completeness is RECOMPUTED after a flip, never hand-patched: the
// validator requires completeness.terminal_status to equal result.Status
// exactly (validateCompleteness), and ComputeAnswerCompleteness is the one
// place that invariant is produced -- see that function's own doc comment
// for why it must never be bypassed.
func applyServerCompletenessAuthority(result InvestigationResult, enabled bool, observation CompletenessAuthorityObservation) InvestigationResult {
	if !enabled || !observation.Derived {
		return result
	}
	if result.Status != InvestigationComplete {
		return result
	}
	mapped, ok := answerCompletenessStateToStatus(observation.ServerState)
	if !ok || mapped == InvestigationComplete {
		return result
	}
	result.Status = mapped
	result.Completeness = ComputeAnswerCompleteness(result)
	return result
}
