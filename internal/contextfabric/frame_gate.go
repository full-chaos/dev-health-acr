package contextfabric

import (
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// FrameGate is the ORDERING SEAM the design's §13.5.2 flow states:
//
//	consensus -> winning sample whole -> A1 (emitted fields) ->
//	normalize/derive -> A2 (derived values) -> repair once if A1 or A2
//	failed -> FRAME IMMUTABLE -> resolution (phase B) -> fact read (phase C)
//
// Frame validity and the refuse basis are DECIDED at interpretation, before
// any retrieval path exists to disagree with them, and the decision is
// CARRIED rather than re-derived. Retrieval then obeys a verdict that was
// already reached; it never reaches its own.
//
// WHY THIS TYPE EXISTS AT ALL, stated once because it is the whole ticket.
// Frame validation shipped as a SHADOW: resolveFrame (model_runtime.go)
// validated the proposal, recorded the outcome on the receipt, emitted the
// telemetry -- and gated NOTHING, by design, so that slice could prove zero
// behaviour change. The frame then rode out to retrieval only when it was
// VALID, which meant a REFUSED frame did not stop the turn: it degraded to
// "no frame observed", the fail-safe every seam-7 consumer takes for a frame
// that was never proposed. A frame the server had just refused and a frame
// the model never emitted became the same state one line later.
//
// Measured on the rig (embeddings on, corpus row neg-illegal-i6-self-group):
// a frame whose GroupKind equalled its MemberKind -- refused by invariant I6
// at frame_invariants.go's checkI6 -- was served with a team cohort and a
// prose limitation saying the request asked to group teams by team. The
// server knew, said so in a sentence, and answered anyway. That is the
// deviation this type closes, and it closes it by making the refusal a value
// the engine must consume rather than a fact it may ignore.
//
// The zero value is FrameGateNotEvaluated, which ALLOWS. That is deliberate
// and it is not the "missing reads as zero" defect: a caller that never ran
// interpretation (this package's own unit callers, the basis-discarding
// ResolveSubjects wrapper) has genuinely decided nothing, and pretending it
// decided "refuse" would refuse every question those callers ask. The
// production path always stamps a decided value, and
// TestTheDeployedInterpreterAlwaysDecidesTheFrameGate pins that it does, so
// `not_evaluated` on a rig line means exactly one thing: the line came from
// something that is not the deployed interpreter.
type FrameGate struct {
	// Outcome is the closed verdict. Always set on the production path.
	Outcome FrameGateOutcome
	// FailedInvariant is the FIRST invariant that failed, empty unless
	// Outcome is FrameGateRejectedInvalid. It is the same value the
	// receipt and the frame-validation telemetry line already carry, read
	// from the same FrameValidationResult, so the three cannot disagree.
	FailedInvariant FrameInvariant
	// RefuseBasis is the cohort discoverability reason, empty unless
	// Outcome is FrameGateRefusedBasis. Today the only refusing member is
	// CohortMemberKindUnservable -- see DecideFrameGate for why the other
	// two non-discoverable reasons deliberately do NOT refuse.
	RefuseBasis CohortDiscoverability
	// DeclaredMemberKind is the member kind the refused frame named, empty
	// unless Outcome is FrameGateRefusedBasis.
	//
	// CARRIED, NOT RE-DERIVED, and that is the whole reason it is a field
	// rather than something the terminal recomputes. DecideFrameGate
	// already asks CohortMemberKindFor for the reason it refuses on; the
	// declared kind falls out of the SAME call. A consumer that re-ran the
	// predicate to learn the kind would be a second reading of it taken at
	// a later moment against a frame that may by then be nil -- the exact
	// two-readings-of-one-decision drift this seam exists to remove, and
	// the terminal path is downstream of the point where a refused frame
	// stops riding along.
	DeclaredMemberKind SubjectKind
}

// FrameGateOutcome is the closed vocabulary. Every member is a DECISION;
// there is no "unknown" member, because a gate that cannot say what it
// decided is the shadow this type replaces.
type FrameGateOutcome string

const (
	// FrameGateNotEvaluated: nothing decided this gate. It IS the empty
	// string on purpose, so that the Go zero value and "no decision" are
	// the same state rather than two states one `if` apart -- a separate
	// spelling would give an unset FrameGate a third meaning nobody wrote
	// down. The only member that means "no decision" rather than a
	// verdict, and it ALLOWS; see the type doc comment for why.
	FrameGateNotEvaluated FrameGateOutcome = ""
	// FrameGateNotProposed: the interpretation carried no frame at all.
	// ALLOWS, and is deliberately DISTINCT from FrameGateNotEvaluated: one
	// says the model emitted nothing, the other says nobody looked. They
	// were the same state before this type and that is precisely the
	// confusion that let a refused frame pass as an absent one.
	FrameGateNotProposed FrameGateOutcome = "not_proposed"
	// FrameGatePassed: the frame validated through phases A1 and A2 and
	// its subject expression has a servable population (or is not a cohort
	// variant at all). ALLOWS.
	FrameGatePassed FrameGateOutcome = "passed"
	// FrameGateRejectedInvalid: the frame failed a frame invariant. The
	// design's §13.1 terminal for this state is "frame = refused, family =
	// unclassified, refuse to guess" -- so it REFUSES, and FailedInvariant
	// names which invariant refused it.
	FrameGateRejectedInvalid FrameGateOutcome = "rejected_invalid"
	// FrameGateRefusedBasis: the frame validated, and then declared a
	// member kind no discovery arm serves. REFUSES, with RefuseBasis
	// naming the basis. The refusal itself is not new -- discovery has
	// reported member_kind_unservable since seam 7 -- what is new is that
	// it is reached BEFORE retrieval may offer or commit a subject into a
	// frame that can never be served.
	FrameGateRefusedBasis FrameGateOutcome = "refused_basis"
)

var frameGateOutcomes = [...]FrameGateOutcome{
	FrameGateNotEvaluated,
	FrameGateNotProposed,
	FrameGatePassed,
	FrameGateRejectedInvalid,
	FrameGateRefusedBasis,
}

// FrameGateOutcomeCount is the closed vocabulary's size.
const FrameGateOutcomeCount = len(frameGateOutcomes)

// FrameGateOutcomeVocabulary returns the closed vocabulary in declared order.
func FrameGateOutcomeVocabulary() [FrameGateOutcomeCount]FrameGateOutcome {
	return frameGateOutcomes
}

// ValidFrameGateOutcome reports membership. The empty string IS a member --
// it is FrameGateNotEvaluated -- which is the one place this vocabulary
// departs from its siblings in this package, and it departs deliberately: a
// gate nobody ran is a real, nameable state that every unit caller in this
// repository legitimately produces, and refusing it as invalid would make the
// most common non-production value the one no predicate accepts.
func ValidFrameGateOutcome(value FrameGateOutcome) bool {
	for _, member := range frameGateOutcomes {
		if member == value {
			return true
		}
	}
	return false
}

// DecideFrameGate is the whole decision, TOTAL and PURE over a validation
// result, and it runs at interpretation -- the point §13.5.2's order puts it.
//
// hasProposal distinguishes "the model emitted no frame" from "the model
// emitted a frame that refused". resolveFrame returns early on the first, so
// a caller that never validated anything passes false and gets
// FrameGateNotProposed rather than a verdict about a frame that never existed
// (the phantom-failure trap FrameValidationEvent's own doc comment already
// refuses for the invariant histogram).
//
// ONLY member_kind_unservable REFUSES among the discoverability reasons, and
// the two exclusions are load-bearing rather than an oversight:
//
//   - not_a_cohort_variant is the ordinary named_subject / organization_scope
//     case. It is the MOST COMMON reason in production and refusing on it
//     would refuse nearly every question the product answers.
//   - no_member_kind is explicit_set, whose members come from SUBJECT
//     RESOLUTION rather than from discovery (cohortKindFromFrame's own doc
//     comment). Refusing before resolution would refuse exactly the shape
//     that needs resolution to run.
//
// member_kind_unservable is different in kind, not merely in degree: the
// frame named a population and NO ARM CAN BUILD IT, so no amount of retrieval
// can produce the answer, and every subject offered into it is an offer
// toward a question the server has already established it cannot serve.
func DecideFrameGate(result FrameValidationResult, hasProposal bool) FrameGate {
	if !hasProposal {
		return FrameGate{Outcome: FrameGateNotProposed}
	}
	if result.Outcome != FrameValidationOutcomeValid {
		return FrameGate{Outcome: FrameGateRejectedInvalid, FailedInvariant: result.Failure.Invariant}
	}
	if _, declaredKind, reason := CohortMemberKindFor(result.Frame.SubjectExpression); reason == CohortMemberKindUnservable {
		// declaredKind rides out of the same call that decided the
		// refusal. It is what makes the served disclosure actionable --
		// the kind is the one thing the asker can change about the
		// question -- and reading it here rather than downstream is what
		// keeps one decision one reading.
		return FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: reason, DeclaredMemberKind: declaredKind}
	}
	return FrameGate{Outcome: FrameGatePassed}
}

// Refuses reports whether retrieval must not run for this turn.
//
// Stated as REFUSES rather than as "allows" on purpose: the two allowing
// members that are not FrameGatePassed (not_evaluated, not_proposed) are
// the ones a future member is most likely to be added beside, and a
// permissive default on an unrecognised member is the failure this whole
// seam exists to remove. An unrecognised Outcome therefore REFUSES.
func (g FrameGate) Refuses() bool {
	switch g.Outcome {
	case FrameGateNotEvaluated, FrameGateNotProposed, FrameGatePassed:
		return false
	default:
		return true
	}
}

// Observable renders the gate as the single closed-vocabulary token the
// decision line carries: the outcome, plus the invariant or basis that
// decided it. Never empty -- an unset FrameGate renders as its own token so
// a missing decision can never read as a passing one on a log line.
func (g FrameGate) Observable() string {
	switch g.Outcome {
	case FrameGateRejectedInvalid:
		return fmt.Sprintf("rejected:%s", nonEmptyGateDetail(string(g.FailedInvariant)))
	case FrameGateRefusedBasis:
		return fmt.Sprintf("refused:%s", nonEmptyGateDetail(string(g.RefuseBasis)))
	case FrameGateNotEvaluated:
		// The empty Outcome renders as a WORD, never as an empty log
		// value: `frame_gate=""` on a rig line is indistinguishable from a
		// key nobody wrote, and this whole seam turns on being able to
		// tell "not decided" from "decided to allow".
		return "not_evaluated"
	default:
		return string(g.Outcome)
	}
}

// ObservableRefuseBasis renders the refuse basis for the decision line,
// "none" when there is not one. The explicit token is what makes a line with
// no basis distinguishable from a line whose basis key was never written --
// the missing-versus-measured-zero rule applied to a string.
func (g FrameGate) ObservableRefuseBasis() string {
	if g.RefuseBasis == "" {
		return "none"
	}
	return string(g.RefuseBasis)
}

// nonEmptyGateDetail keeps the rendered token parseable when a caller built a
// FrameGate literal with a refusing outcome and no detail. Such a value is a
// programming error, and the token says so rather than rendering `rejected:`
// with a trailing colon that a log reader would have to guess at.
func nonEmptyGateDetail(value string) string {
	if value == "" {
		return "unspecified"
	}
	return value
}

// RefusalBasis maps this gate to the CLOSED WIRE vocabulary a refused turn
// discloses (CHAOS-5442), or to the empty value when the gate did not refuse.
//
// AN ALLOW-LIST, NAMING EVERY MEMBER'S CLASS, never a deny-list with an else.
// The three allowing members are named explicitly and return "not refused";
// the two refusing members are named explicitly and return their token; and
// the default arm returns `unspecified` rather than "not refused", because
// Refuses() itself refuses on an unrecognised outcome. The two must agree in
// BOTH directions or a future gate member would refuse a turn while the
// served document said the turn was never refused -- a silent collapse of
// exactly the distinction this field adds. TestTheGateAndTheWireBasisAgree
// asserts the biconditional over the whole vocabulary.
func (g FrameGate) RefusalBasis() contractsv1.ContextFabricRefusalBasis {
	switch g.Outcome {
	case FrameGateNotEvaluated, FrameGateNotProposed, FrameGatePassed:
		return ""
	case FrameGateRefusedBasis:
		// The gate's own basis is the wire's basis, one token for one
		// fact. It is validated rather than cast blindly: a
		// CohortDiscoverability member that is not a wire member is a
		// vocabulary drift, and disclosing `unspecified` is how that
		// drift becomes visible instead of shipping an unrecognised
		// string to every consumer.
		//
		// FRAME MEMBERS ONLY. The wire vocabulary also names a refusal of a
		// continuation's CARRIER, which no frame decision can take; a gate
		// basis spelling that member is drift of exactly the kind this
		// validation exists to surface, so it maps to `unspecified` too.
		if basis := contractsv1.ContextFabricRefusalBasis(g.RefuseBasis); contractsv1.ValidContextFabricFrameRefusalBasis(basis) {
			return basis
		}
		return contractsv1.ContextFabricRefusalBasisUnspecified
	case FrameGateRejectedInvalid:
		return contractsv1.ContextFabricRefusalBasisFrameInvariantViolated
	default:
		return contractsv1.ContextFabricRefusalBasisUnspecified
	}
}

// ObservableRefusalBasis renders the WIRE refusal basis for a log line,
// "none" when the gate did not refuse.
//
// Distinct from ObservableRefuseBasis above, which renders the gate's own
// CohortDiscoverability basis and is therefore "none" for a frame refused on
// an INVARIANT -- correct for the line it serves (the frame-validation line,
// which carries the failed invariant beside it) and wrong for a line that
// claims to say whether the turn was refused at all.
//
// The explicit token lives HERE, at the one place the value is decided,
// rather than inside a sink: a sink-local substitution is a second authority
// that keeps the ordinary line looking right while every other implementation
// of the recorder emits an empty value.
func (g FrameGate) ObservableRefusalBasis() string {
	if basis := g.RefusalBasis(); basis != "" {
		return string(basis)
	}
	return "none"
}
