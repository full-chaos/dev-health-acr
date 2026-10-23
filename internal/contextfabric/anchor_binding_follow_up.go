package contextfabric

// A natural follow-up: a continuation whose question changed from the one
// that proved the parent's binding, for example "and how many teams
// contribute to it?" after an answer about one repository.
//
// Before this switch, admission refuses every changed question
// (refused_question_changed), and a follow-up whose own text names no
// subject ends no_match: the subject the user refers to is lost.
//
// With EngineOptions.AnchorBindingFollowUpEnabled on (it needs
// AnchorBindingCarryEnabled), a changed question under a continuation is
// admitted as admitted_follow_up when the parent binding is bound and every
// other admission check passes (policy, age, ownership, the live re-read).
// That admission NEVER feeds the carry hint or the count kind, which read
// admitted only: per the conservative contract (design rev 4 §3.3), the
// remembered anchor is the pre-selected option of a clarification, never a
// silent serve on memory alone. So the turn's own resolution decides:
//
//   - it commits a subject of its own: unchanged (served when it is the
//     remembered one; the subject-substitution guard when it is another);
//   - it commits nothing: the remembered identity is offered FIRST among the
//     candidates, in the subject-substitution guard's remembered-offer form
//     (receipt minted from the parent's result, redeemable through
//     prior_subject_receipts, no new contract token), with a prompt saying
//     the follow-up names no subject. Offered only when the follow-up's frame
//     admits the binding's kind as its scope anchor, and only for the
//     identity the parent served, whose label the caller already saw.
//
// The binding is kept (held, its origin unchanged), so a further follow-up
// within the maximum age can be answered the same way.

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// followUpClarificationPrompt is the prompt of a follow-up that names no
// subject, with the remembered one offered first.
const followUpClarificationPrompt = "This follow-up does not name the subject it is about. Pick the subject you mean: the one the earlier answer was about is listed first."

// offerFollowUpAnchor offers the remembered identity first on a follow-up
// admitted as admitted_follow_up whose own resolution committed nothing, and
// records the carry decision. Any other turn passes through unchanged.
func (t *anchorBindingTracker) offerFollowUpAnchor(resolution SubjectResolution, frame *QuestionFrame, parent parentAnchorEvidence, namedParent string) SubjectResolution {
	if t == nil || t.admissionOutcome() != AnchorBindingAdmissionAdmittedFollowUp {
		return resolution
	}
	held := anchorRef{Kind: t.parent.Binding.Kind, ID: t.parent.Binding.CanonicalID}
	switch {
	case len(resolution.Committed) != 0:
		t.carry = BoundAnchorCarryFollowUpCommitted
		return resolution
	case ScopeAnchorRetrievalKind(frame, held.Kind) != held.Kind:
		t.carry = BoundAnchorCarryFollowUpFrameRefused
		return resolution
	case !parent.held() || parent.Subject.Kind != held.Kind || parent.Subject.CanonicalID != held.ID:
		t.carry = BoundAnchorCarryFollowUpParentMismatch
		return resolution
	}
	// The guard's own issuer rule: the result whose subject this is, else the
	// named parent.
	issuer := subjectSubstitutionReceiptIssuer(subjectSubstitutionDecision{ParentResultID: parent.ResultID}, namedParent)
	remembered := SubjectCandidate{
		ReceiptID:      subjectSubstitutionReceiptID(issuer, parent.Subject),
		Subject:        parent.Subject,
		State:          contractsv1.ContextFabricResolutionAmbiguous,
		MatchedTerms:   []string{},
		MatchReasons:   []string{"the subject the earlier answer was about"},
		Confidence:     0,
		EvidenceRefIDs: []string{},
		Provider:       providerOfSubject(resolution.Candidates, parent.Subject),
	}
	candidates := []SubjectCandidate{remembered}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind == held.Kind && candidate.Subject.CanonicalID == held.ID {
			continue
		}
		candidates = append(candidates, candidate)
	}
	out := resolution
	out.Candidates = candidates
	out.ClarificationPrompt = followUpClarificationPrompt
	t.carry = BoundAnchorCarryFollowUpOffered
	return out
}

// followUpOfferEndsTurn is true when the turn offered the remembered identity
// and committed nothing: the offer is then the whole answer, a clarification.
func (t *anchorBindingTracker) followUpOfferEndsTurn(resolution SubjectResolution) bool {
	if t == nil || len(resolution.Committed) != 0 {
		return false
	}
	return t.carry == BoundAnchorCarryFollowUpOffered || t.carry == BoundAnchorCarryFollowUpOfferedReconciled
}

// reconcileFollowUpStructure removes, after a follow-up offer, any anchor
// option the turn's structure offer holds for the offered identity, so the
// caller sees the remembered subject once, through one redemption path. The
// anchor need itself stays disclosed: an anchor need with no option left is
// the documented "missing, nothing offerable" shape. Any other turn passes
// through unchanged.
func (t *anchorBindingTracker) reconcileFollowUpStructure(material StructureOfferMaterial) StructureOfferMaterial {
	if t == nil || t.carry != BoundAnchorCarryFollowUpOffered {
		return material
	}
	held := anchorRef{Kind: t.parent.Binding.Kind, ID: t.parent.Binding.CanonicalID}
	kept := make([]contractsv1.ContextFabricAnchorOption, 0, len(material.AnchorOptions))
	for _, option := range material.AnchorOptions {
		if option.Kind == held.Kind && option.CanonicalID == held.ID {
			continue
		}
		kept = append(kept, option)
	}
	if len(kept) == len(material.AnchorOptions) {
		return material
	}
	out := material
	out.AnchorOptions = kept
	t.carry = BoundAnchorCarryFollowUpOfferedReconciled
	return out
}
