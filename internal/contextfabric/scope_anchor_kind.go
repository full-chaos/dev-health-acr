package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ScopeAnchorRetrievalKind decides whether this interpretation carries a
// usable SCOPE ANCHOR KIND, and returns it, or "" when it does not.
//
// WHY THIS EXISTS. A children_of_scope question ("which repositories does the
// platform team own?") declares two different kinds: the MEMBERS being asked
// for (repository) and the ANCHOR they hang off (team). Only the first is on
// the frame -- ScopedSetExpression is {AnchorTerms, MemberKind} and has no
// anchor-kind field at all. So frameKindHints (graphrank/cohort_kind.go),
// which reads SubjectExpression.MemberKind()/GroupKind(), can only ever hint
// the MEMBER kind, and the kind-hinted retrieval arm consequently searches
// the anchor's own terms under the wrong kind: SearchKind("platform",
// repository) rather than SearchKind("platform", team). The anchor kind was
// never a retrieval input at all.
//
// It is not absent from the system, only from the frame. The model IS asked
// for it by name (genkitruntime/prompts.go: "in 'what are the statuses of the
// fullchaos team's projects' ... scope_anchor_term is 'fullchaos' and
// scope_anchor_kind is 'team'"), it is sanitized against the closed registry
// on the way in (genkitruntime/runtime.go, SanitizeGroupKind, with an
// unrecognized counter beside it), and it is carried on the classification
// receipt to FamilySample.ScopeAnchorKind and thence to
// QuestionFamilyOutcome.WinningSample -- which is already in scope at the
// engine's ResolveSubjects call site, right beside the Frame that call
// already passes. Its siblings on that same struct are already load-bearing
// in production (the engine reads WinningSample.ScopeAnchorTerm; the answer
// plan reads WinningSample.GroupKind). This function is the gate that turns
// that existing signal into a retrieval hint.
//
// THE RETURN IS "" FOR EVERYTHING THIS DOES NOT COVER, and "" means EXACTLY
// the behaviour that existed before this function: no third hint source, and
// every widened signature downstream behaves byte-identically to its previous
// version. There is no other disabled state and no fallback -- an anchor kind
// that fails any check below is DROPPED, never guessed at and never
// substituted with the member kind.
//
// The checks, in order, and why each one is a real refusal rather than
// defensive padding:
//
//  1. No frame, or a frame variant that declares no scope anchor. ONLY
//     children_of_scope has one. grouped_members is deliberately excluded:
//     its GroupKind is a GROUPING AXIS, not a scope anchor -- invariant I6
//     already forbids it equalling MemberKind, and frameKindHints ALREADY
//     hints it, so admitting it here would both double-count and mislabel it.
//  2. No anchor terms. An anchor kind with nothing to search for cannot seed
//     retrieval; hinting the kind alone would spend SearchKind calls on the
//     member terms under the anchor's kind, which is the mirror image of the
//     defect this fixes.
//  3. An empty or out-of-vocabulary kind. The receipt's sanitizer already
//     closes this against the registry, so a non-member value here means the
//     model emitted something invented; the paired *Unrecognized flag on the
//     receipt is what counts that, and this function's job is only to refuse
//     it.
//  4. An anchor kind EQUAL to the member kind. "The members of X under an
//     anchor that is itself an X" is not a scope relationship, and the whole
//     signal is the ASYMMETRY between the two: the prompt teaches the model
//     that difference explicitly, and the family-precedence check
//     (chaos4632_question_family_precedence.go) already keys row 2 on exactly
//     this inequality. Admitting an equal pair would hint a kind
//     frameKindHints has already contributed, adding retrieval cost and a
//     second, drifting derivation of one value -- the defect the frame exists
//     to prevent.
//
// Note what is NOT checked: whether any subject of that kind actually exists.
// That is retrieval's job, and a kind with no matching subject must produce an
// honest empty result, never a substituted one.
func ScopeAnchorRetrievalKind(frame *QuestionFrame, anchorKind SubjectKind) SubjectKind {
	if frame == nil {
		return ""
	}
	if frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope {
		return ""
	}
	scoped := frame.SubjectExpression.Scoped
	if scoped == nil || len(scoped.AnchorTerms) == 0 {
		return ""
	}
	if anchorKind == "" || !contractsv1.ValidContextFabricSubjectKind(anchorKind) {
		return ""
	}
	if anchorKind == scoped.MemberKind {
		return ""
	}
	return anchorKind
}
