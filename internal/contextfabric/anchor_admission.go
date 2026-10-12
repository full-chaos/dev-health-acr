package contextfabric

// Admission of a carried anchor binding: whether the binding a named parent
// holds may be the binding this turn starts from.
//
// A parent row's binding is evidence about an earlier turn. It is carried
// only when every check below passes, in this order, the live re-read last
// because it is the one check that reads the graph:
//
//  1. policy: the binding was decided under this build's binding policy;
//  2. question: the turn that PROVED the held identity (OriginResultID, not
//     merely the immediate parent) asked this turn's question, under the
//     engine's one same-question containment (carryOriginSameQuestionVerdict);
//  3. age: the proof is no older than the binding's maximum age, and not
//     dated after this turn;
//  4. ownership: the ownership state the proof stood on is recorded on both
//     sides and did not move (anchor_freshness.go);
//  5. live re-read: the held identity is still readable by this principal at
//     this turn's graph binding (rememberedSubjectReadable, the same re-read
//     the subject-substitution guard uses).
//
// A refused binding is never the starting point: the binder starts unbound.
// A turn that ends before admission runs keeps the parent's binding as it
// was (carried_not_evaluated); nothing was proven either way, and a later
// turn re-runs every check before the binding can be used.
//
// Admission decides what the SHADOW binding starts from. Nothing served
// reads it.

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AnchorBindingPolicyVersion names the rules a binding was decided under. A
// binding stamped with another policy, or none, is not admitted.
const AnchorBindingPolicyVersion = "anchor-binding.v2"

// DefaultAnchorBindingMaxAge is the age past which a proof is not carried.
const DefaultAnchorBindingMaxAge = 15 * time.Minute

// Bounds on a configured maximum age.
const (
	MinAnchorBindingMaxAge = time.Minute
	MaxAnchorBindingMaxAge = 24 * time.Hour
)

// AnchorBindingWitnessMax bounds the witness a binding persists.
const AnchorBindingWitnessMax = 8

// AnchorBindingAdmission is the closed outcome of admitting the parent's
// binding.
type AnchorBindingAdmission string

const (
	// AnchorBindingAdmissionNotApplicable: the parent carries no readable,
	// current-epoch binding that holds an identity.
	AnchorBindingAdmissionNotApplicable AnchorBindingAdmission = "not_applicable"
	// AnchorBindingAdmissionNotEvaluated: the turn ended before admission
	// ran.
	AnchorBindingAdmissionNotEvaluated AnchorBindingAdmission = "not_evaluated"
	// AnchorBindingAdmissionAdmitted: every check passed.
	AnchorBindingAdmissionAdmitted AnchorBindingAdmission = "admitted"
	// AnchorBindingAdmissionAdmittedFollowUp: the question changed under a
	// continuation, the follow-up switch is on, the binding is bound, and
	// every other check passed (anchor_binding_follow_up.go). It never feeds
	// the carry hint or the count kind; it only lets the turn offer the
	// remembered identity first when its own resolution commits nothing.
	AnchorBindingAdmissionAdmittedFollowUp AnchorBindingAdmission = "admitted_follow_up"
	// AnchorBindingAdmissionRefusedPolicy: decided under another policy.
	AnchorBindingAdmissionRefusedPolicy AnchorBindingAdmission = "refused_policy_version"
	// AnchorBindingAdmissionRefusedQuestionChanged: the proving turn asked
	// another question.
	AnchorBindingAdmissionRefusedQuestionChanged AnchorBindingAdmission = "refused_question_changed"
	// AnchorBindingAdmissionRefusedQuestionIndeterminate: one of the two
	// questions has no identity to compare.
	AnchorBindingAdmissionRefusedQuestionIndeterminate AnchorBindingAdmission = "refused_question_indeterminate"
	// AnchorBindingAdmissionRefusedOriginUnverifiable: the proving turn's
	// result could not be read.
	AnchorBindingAdmissionRefusedOriginUnverifiable AnchorBindingAdmission = "refused_origin_unverifiable"
	// AnchorBindingAdmissionRefusedExpired: the proof is older than the
	// maximum age, carries no proof time, or is dated after this turn.
	AnchorBindingAdmissionRefusedExpired AnchorBindingAdmission = "refused_expired"
	// AnchorBindingAdmissionRefusedOwnershipMoved: ownership moved since the
	// proof.
	AnchorBindingAdmissionRefusedOwnershipMoved AnchorBindingAdmission = "refused_ownership_moved"
	// AnchorBindingAdmissionRefusedOwnershipUnverified: the proof's or this
	// turn's ownership state is not recorded, so no movement can be ruled
	// out.
	AnchorBindingAdmissionRefusedOwnershipUnverified AnchorBindingAdmission = "refused_ownership_unverified"
	// AnchorBindingAdmissionRefusedSubjectUnreadable: the live re-read did
	// not prove the held identity readable; admission_remembered_check says
	// why.
	AnchorBindingAdmissionRefusedSubjectUnreadable AnchorBindingAdmission = "refused_subject_unreadable"
)

func anchorBindingAdmissions() []AnchorBindingAdmission {
	return []AnchorBindingAdmission{
		AnchorBindingAdmissionNotApplicable, AnchorBindingAdmissionNotEvaluated, AnchorBindingAdmissionAdmitted, AnchorBindingAdmissionAdmittedFollowUp,
		AnchorBindingAdmissionRefusedPolicy, AnchorBindingAdmissionRefusedQuestionChanged, AnchorBindingAdmissionRefusedQuestionIndeterminate,
		AnchorBindingAdmissionRefusedOriginUnverifiable, AnchorBindingAdmissionRefusedExpired, AnchorBindingAdmissionRefusedOwnershipMoved,
		AnchorBindingAdmissionRefusedOwnershipUnverified, AnchorBindingAdmissionRefusedSubjectUnreadable,
	}
}

// anchorBindingAdmissionInput is everything admitAnchorBinding reads.
type anchorBindingAdmissionInput struct {
	Held     AnchorBinding
	Question carryOriginVerdict
	Now      time.Time
	MaxAge   time.Duration
	Movement AnchorOwnershipMovement
	// FollowUp admits a changed question onto a bound binding as
	// admitted_follow_up (the follow-up switch).
	FollowUp bool
	// Remembered is the live re-read, asked only when every other check
	// passed; nil means it was not asked.
	Remembered func() rememberedSubjectCheck
}

// anchorBindingAdmissionDecision is the outcome and what it read.
type anchorBindingAdmissionDecision struct {
	Outcome    AnchorBindingAdmission
	Remembered rememberedSubjectCheck
}

// admitAnchorBinding decides admission for a held binding. PURE apart from
// the re-read it is handed.
func admitAnchorBinding(in anchorBindingAdmissionInput) anchorBindingAdmissionDecision {
	notChecked := rememberedSubjectCheck{Check: SubjectSubstitutionRememberedNotChecked}
	refuse := func(outcome AnchorBindingAdmission) anchorBindingAdmissionDecision {
		return anchorBindingAdmissionDecision{Outcome: outcome, Remembered: notChecked}
	}
	switch {
	case !in.Held.active():
		return refuse(AnchorBindingAdmissionNotApplicable)
	case in.Held.PolicyVersion != AnchorBindingPolicyVersion:
		return refuse(AnchorBindingAdmissionRefusedPolicy)
	}
	admitted := AnchorBindingAdmissionAdmitted
	switch in.Question {
	case carryOriginSameQuestion:
	case carryOriginDrifted:
		if !in.FollowUp || in.Held.State != AnchorBindingBound {
			return refuse(AnchorBindingAdmissionRefusedQuestionChanged)
		}
		admitted = AnchorBindingAdmissionAdmittedFollowUp
	case carryOriginIndeterminateQuestion:
		return refuse(AnchorBindingAdmissionRefusedQuestionIndeterminate)
	default:
		return refuse(AnchorBindingAdmissionRefusedOriginUnverifiable)
	}
	if in.Held.ProvenAt.IsZero() || in.Held.ProvenAt.After(in.Now) || in.Now.Sub(in.Held.ProvenAt) > in.MaxAge {
		return refuse(AnchorBindingAdmissionRefusedExpired)
	}
	switch in.Movement {
	case AnchorOwnershipUnmoved:
	case AnchorOwnershipMoved:
		return refuse(AnchorBindingAdmissionRefusedOwnershipMoved)
	default:
		return refuse(AnchorBindingAdmissionRefusedOwnershipUnverified)
	}
	if in.Remembered == nil {
		return refuse(AnchorBindingAdmissionRefusedSubjectUnreadable)
	}
	remembered := in.Remembered()
	if !remembered.readable() {
		return anchorBindingAdmissionDecision{Outcome: AnchorBindingAdmissionRefusedSubjectUnreadable, Remembered: remembered}
	}
	return anchorBindingAdmissionDecision{Outcome: admitted, Remembered: remembered}
}

// admitAnchorBindingParent runs admission for the tracker's parent binding,
// once, after this turn's ownership freshness is acquired. A nil tracker is
// the shadow switched off.
func (e *Engine) admitAnchorBindingParent(ctx context.Context, principal storage.Principal, request InvestigationRequest, binding ResolvedGraphBinding, t *anchorBindingTracker) {
	if t == nil {
		return
	}
	held := AnchorBinding{}
	if t.parent.Status == AnchorBindingParentPresent {
		held = t.parent.Binding
	}
	in := anchorBindingAdmissionInput{
		Held: held, Now: t.now, MaxAge: e.anchorBindingMaxAge(),
		Movement: anchorOwnershipMovement(held, t.freshness),
		FollowUp: e.anchorBindingFollowUpEnabled,
	}
	if held.active() {
		in.Question = e.carryOriginSameQuestionVerdict(ctx, principal, request, held.OriginResultID)
		in.Remembered = func() rememberedSubjectCheck {
			return e.rememberedSubjectReadable(ctx, principal, request, binding, SubjectRef{Kind: held.Kind, CanonicalID: held.CanonicalID})
		}
	}
	decision := admitAnchorBinding(in)
	t.admission, t.admissionRemembered = decision.Outcome, decision.Remembered
	if held.active() {
		t.admissionAgeSeconds = int64(t.now.Sub(held.ProvenAt) / time.Second)
		if held.ProvenAt.IsZero() {
			t.admissionAgeSeconds = -1
		}
	}
}

func (e *Engine) anchorBindingMaxAge() time.Duration {
	if e.anchorBindingMaxAgeSetting == 0 {
		return DefaultAnchorBindingMaxAge
	}
	return e.anchorBindingMaxAgeSetting
}

// validateAnchorBindingMaxAge bounds a configured maximum age; zero selects
// the default.
func validateAnchorBindingMaxAge(age time.Duration) error {
	if age == 0 {
		return nil
	}
	if age < MinAnchorBindingMaxAge || age > MaxAnchorBindingMaxAge {
		return fmt.Errorf("context fabric anchor binding max age %s is outside [%s, %s]", age, MinAnchorBindingMaxAge, MaxAnchorBindingMaxAge)
	}
	return nil
}

// anchorBindingWitness is the bounded record of every identity the proving
// turn proved, in the order it proved them whatever their kinds,
// "<kind>:<canonical id>".
func anchorBindingWitness(proposal anchorBindingProposal) []string {
	// nil when the turn proved nothing, so a binding encodes and decodes to
	// the same value.
	var out []string
	for _, ref := range proposal.Stream {
		if len(out) == AnchorBindingWitnessMax {
			break
		}
		out = append(out, string(ref.Kind)+":"+ref.ID)
	}
	return out
}
