package contextfabric

// CHAOS-4098: the decisive path's synthesized-status override.
//
// THE DEFECT. In the v9 post-fix rerun (RUN_TAG 20260822T135331Z-84134,
// corpus case 60, member=expected_kind, arm=inferred_tier, the HINTED
// call) the synthesis model returned status "clarification_required" for a
// question whose subject resolution had already COMMITTED a subject. The
// engine copied that status into the composed result verbatim, the result
// carried no SubjectResolution.ClarificationPrompt -- there was none to
// carry, because a committed resolution mints no prompt -- and
// InvestigationResult.Validate() rejected the whole answer on the
// contract's own rule that a clarification result requires a prompt. The
// caller received ErrInvalidResult (HTTP 500) instead of the answer the
// investigation had already paid two model calls to produce.
//
// The hole predates CHAOS-4085 entirely: both halves of it -- the
// validator's rule and SynthesisDraft.ValidateAgainst's acceptance of the
// status -- date to the Reset 0 commit, and the failure reproduces with
// the commit-affirmation gate fully exempted. It surfaced only now because
// it needs the model to pick that status, which it does rarely and
// (measured over four shards) exactly once.
//
// WHY THE STATUS IS INADMISSIBLE HERE RATHER THAN WRONG IN GENERAL.
// Clarification is an ENGINE terminal, not a synthesis outcome. Every
// clarification a caller can act on is composed by ACR -- the subjectless
// terminal (unresolved.go) and the window-confirmation gate (window.go) --
// and each mints the prompt and the offers that make it actionable. The
// synthesis step has no offers to mint and no prompt to write; by the time
// it runs, the question of WHAT to ask has already been decided. So a
// draft that asks for clarification is not requesting anything the caller
// could answer. It is saying the evidence did not support a conclusion.
//
// WHY no_match, AND WHY NOT AN ERROR. no_match is the contract's existing
// word for exactly that, and it is legal WITH committed subjects -- see
// statusSentence's own doc comment, which spells out that no_match means
// "nothing to answer with", NOT "no subject was found", and renders
// separate prose for the committed case. Refusing the whole investigation
// instead (ErrModelOutput -> 502) was the considered alternative: it
// attributes the fault to the model, which is honest, but it throws away a
// complete, validated, structurally-grounded answer -- drivers, findings,
// claimed facts, limitations and all -- over the one field of it that ACR
// composes for itself anyway. The engine already owns this field's
// meaning; it should use it rather than fail on it.
//
// STRICTLY SUBTRACTIVE, AND NEVER A PROMOTION. This override can only ever
// move a status from clarification_required to no_match. It never promotes
// a status toward answer-capable (complete/partial), never invents a
// clarification prompt, never adds a driver, a finding or a claim, and
// never touches SubjectResolution. A model that under-claims is left to
// under-claim; only the one status the engine cannot compose a valid
// result for is rewritten.

import (
	"context"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// synthesisClarificationUnavailableLimitation is the answer-facing
// disclosure appended when this override fires.
//
// FIXED and non-interpolated, the same discipline withRetrievalDegradation
// and commitRetractionLimitation already apply: it names no model, no
// status string, no subject and no mechanism. A limitation is prose a
// reader sees, and the one consequence for that reader is that the answer
// reports finding nothing rather than asking them a question. The
// operator-facing detail -- which status was overridden, and why -- belongs
// in telemetry, which receives it.
const synthesisClarificationUnavailableLimitation = "This question could not be answered from the evidence assembled, and no clarification could be offered to narrow it further."

// SynthesisStatusOverrideReason is the closed vocabulary naming WHY the
// engine overrode a synthesized status. One value today; a type rather
// than a bare string so a second cause cannot be added as free text.
type SynthesisStatusOverrideReason string

// SynthesisStatusOverrideClarificationUnavailable is the only reason this
// override fires: the synthesis step asked for clarification on a path
// that has no clarification to offer.
const SynthesisStatusOverrideClarificationUnavailable SynthesisStatusOverrideReason = "clarification_unavailable"

// SynthesisStatusOverrideOutcome is one override event, reported to
// telemetry. Closed enums and one count only -- never question text, never
// answer text, never a model string, never a subject identifier.
type SynthesisStatusOverrideOutcome struct {
	// From is the status the synthesis step returned, To the status the
	// engine served instead. Both are closed contract enums. Recorded as a
	// PAIR rather than as From alone: a reader diagnosing a rate change
	// needs to know the override still lands where this file says it does,
	// and a To that ever drifts is exactly the regression that would
	// otherwise present only as "no_match got more common".
	From InvestigationStatus
	To   InvestigationStatus
	// Reason is the closed cause label. See
	// SynthesisStatusOverrideReason.
	Reason SynthesisStatusOverrideReason
	// CommittedCount is how many subjects the resolution had committed at
	// the moment of the override. It is the one number that separates the
	// two shapes this can take: a committed resolution (the observed
	// case -- there was a subject, and the answer about it found nothing)
	// from an uncommitted one (no subject, and the engine's own subjectless
	// terminal did not fire). The second would be a genuine engine
	// routing bug rather than model under-claiming, and without this count
	// the two are indistinguishable in the telemetry stream.
	CommittedCount int
}

// applySynthesisStatusOverride rewrites result IN PLACE when the synthesis
// step returned a status the decisive path cannot serve, and returns the
// event describing what it did (nil when it did nothing).
//
// INVARIANTS, all structural rather than incidental:
//
//   - NARROW TRIGGER: fires ONLY for clarification_required, and only when
//     SubjectResolution.ClarificationPrompt is empty. A result that
//     genuinely carries a prompt is a real clarification terminal and is
//     left completely alone -- this function must never rewrite one of the
//     engine's own terminals, which reach Validate through their own call
//     sites (unresolved.go, window.go) and are correct as composed.
//   - NEVER A PROMOTION: the only status transition written is
//     clarification_required -> no_match. There is no path that assigns
//     complete, partial or degraded.
//   - RECOMPOSES WHAT THE STATUS CHANGED: DirectJudgment and
//     DeterministicAnswer both open with statusSentence(status,
//     resolution), so overriding the status without re-rendering them
//     would leave the two fields a caller reads FIRST asserting that
//     clarification is required on a result that says no_match. They are
//     recomposed from the SAME shared renderers the original composition
//     used (composeDirectJudgmentFrom / composeDeterministicAnswerFrom),
//     never patched, so the two can never disagree.
//   - RESOLUTION IS READ, NEVER WRITTEN: no candidate, no committed
//     subject, and no clarification prompt is added, removed or altered.
//   - IDEMPOTENT: a second call is a no-op. The status is no longer
//     clarification_required, so the trigger no longer matches; the
//     disclosure is appended through appendBoundedLimitations, which skips
//     an addition already stated; Coverage.Partial is already true.
//   - ATOMIC DISCLOSURE: the status, its recomposed prose, the limitation
//     and Coverage.Partial are all written in the same pass, before the
//     caller validates or saves, so no consumer ever sees a partially
//     applied override.
func applySynthesisStatusOverride(result *InvestigationResult) *SynthesisStatusOverrideOutcome {
	if result == nil || result.Status != InvestigationClarificationRequired {
		return nil
	}
	// A real clarification terminal carries the prompt that makes it
	// actionable. Trimmed rather than compared to "": the contract's own
	// rule (validate_context_fabric_result.go) rejects a whitespace-only
	// prompt as absent too, so a value this function treated as present
	// would still fail validation below and reproduce the very defect.
	if strings.TrimSpace(result.SubjectResolution.ClarificationPrompt) != "" {
		return nil
	}
	outcome := &SynthesisStatusOverrideOutcome{
		From:           result.Status,
		To:             InvestigationNoMatch,
		Reason:         SynthesisStatusOverrideClarificationUnavailable,
		CommittedCount: len(result.SubjectResolution.Committed),
	}
	result.Status = InvestigationNoMatch
	// Read from the RESULT, not from any draft: Synthesize copies the
	// draft's Drivers and ClaimedFacts into the result verbatim, and the
	// result's copies are the ones that were validated and are about to be
	// served, so rendering from them keeps the prose bound to the document
	// it describes.
	result.DirectJudgment = composeDirectJudgmentFrom(result.Status, result.Drivers, result.SubjectResolution)
	result.DeterministicAnswer = composeDeterministicAnswerFrom(result.Status, result.Drivers, result.ClaimedFacts, result.SubjectResolution)
	// appendBoundedLimitations is the ONE path by which anything is added
	// to a composed result's limitations (CHAOS-3746 round-17 finding 1).
	// The disclosure is registered service-authored in
	// serviceAuthoredLimitations, so it can add to the count of displaced
	// model caveats but can never itself be the caveat displaced.
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{synthesisClarificationUnavailableLimitation})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	// The synthesis step declined to draw a conclusion from what it was
	// given. Coverage.Partial is the contract's existing word for an
	// answer that does not cover what it set out to cover, and it is the
	// same field the retrieval-degradation path (engine.go) and the
	// commit-affirmation gate (chaos4085_commit_affirmation.go) set for
	// the same reason.
	result.Coverage.Partial = true
	return outcome
}

// recordSynthesisStatusOverride emits the override event. Unlike
// CHAOS-4085's retraction sink this needs no type assertion:
// RecordSynthesisStatusOverride is a method on EngineTelemetry itself, so
// every implementation must carry it or fail to compile. That is
// deliberate -- CHAOS-4085 shipped its retraction telemetry behind an
// OPTIONAL interface, nothing in production implemented it, and every
// retraction vanished silently until #207 noticed. A decision branch's
// telemetry must not be able to go missing by omission.
func (e *Engine) recordSynthesisStatusOverride(ctx context.Context, principal storage.Principal, outcome *SynthesisStatusOverrideOutcome) {
	if outcome == nil || e.telemetry == nil {
		return
	}
	e.telemetry.RecordSynthesisStatusOverride(ctx, principal, *outcome)
}
