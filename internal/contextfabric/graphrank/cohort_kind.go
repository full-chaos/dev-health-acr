package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The cohort kind, and the kind hints, read off the QuestionFrame.
//
// SEAM 7 (CHAOS-4736). What this file replaces, and why the replacement is a
// deletion rather than a rewrite:
//
// Neither replaced function's identifier is spelled anywhere in this tree:
// the ticket's acceptance IS a grep showing no declaration and no caller, so
// naming them here would defeat the proof. They are named in the PR body and
// in CHAOS-4736.
//
//   - The cohort-kind matcher (discover.go) concatenated RequestedJudgment
//     and SubjectTerms, lower-cased, and returned `project` on the substring
//     "project"/"initiative", `team` on "team"/"group", and DEFAULTED to
//     `team`. `repository` was unreachable through it by construction. It
//     was observed firing that default in production: "Show me open
//     incidents per repository" served a `team` cohort.
//   - The kind-hint matcher (chaos4348_reachability.go) read the SAME two
//     model-prose fields for the kind-hinted pool search, whole-word rather
//     than substring, and so disagreed with the first matcher on the same
//     question.
//
// Both were a keyword table over model prose deciding STRUCTURE, which is
// the banned shape the ticket names. The frame already carries the answer as
// a closed-vocabulary field the model filled in deliberately, and the union's
// MemberKind()/GroupKind() accessors were written by the frame slice for
// exactly this call site. So the substitution is: read the declared field,
// delete the guessers.
//
// THERE IS NO PROSE FALLBACK ON THE FAILURE PATH. A turn whose frame did not
// validate discovers no cohort and reports WHY in the closed vocabulary
// below. Falling back to a substring match when the frame is missing would
// keep the banned shape alive on precisely the inputs least able to afford a
// guess, and would make the deletion cosmetic.

// CohortKindBasis is the CLOSED vocabulary naming what decided the cohort
// kind, or what prevented a cohort from being discovered at all.
//
// It exists because every one of these outcomes used to be indistinguishable
// from "the graph had no matching nodes". A cohort question that returns no
// cohort is a real product outcome and it must be diagnosable from the run's
// own artifacts, not by re-reading source.
type CohortKindBasis string

const (
	// CohortKindFromFrameMemberKind: the frame declared a member kind and
	// the cohort was discovered for it. The only basis that discovers.
	CohortKindFromFrameMemberKind CohortKindBasis = "frame_member_kind"
	// CohortKindFrameAbsent: no validated frame reached retrieval, so
	// there is no declared kind to discover. This is the basis that
	// replaces the old matcher's silent `team` default, and counting it is
	// how the cost of having no fallback becomes visible instead of
	// arriving as an unexplained empty cohort.
	CohortKindFrameAbsent CohortKindBasis = "frame_absent"
	// CohortKindNotACohortVariant: the frame validated and its subject
	// expression names ONE subject (named_subject) or the organization
	// itself with nothing to enumerate. Discovering a cohort here would
	// invent a set the question never asked for. This is the frame-side
	// replacement for the old Shape gate.
	CohortKindNotACohortVariant CohortKindBasis = "not_a_cohort_variant"
	// CohortKindNoMemberKind: the expression IS a cohort variant but
	// declares no member kind. explicit_set is the case that reaches this:
	// its operands are NAMED, so its members come from subject resolution,
	// and there is nothing for graph discovery to find. DECLARED BEHAVIOUR
	// CHANGE: the old Shape gate admitted `explicit_cohort` and the old
	// matcher then handed it a `project`-or-`team` guess, so an explicit
	// comparison could acquire a discovered cohort of every authorized
	// subject of a guessed kind, beside the named operands it actually
	// asked about.
	CohortKindNoMemberKind CohortKindBasis = "no_member_kind"
)

var cohortKindBases = [...]CohortKindBasis{
	CohortKindFromFrameMemberKind,
	CohortKindFrameAbsent,
	CohortKindNotACohortVariant,
	CohortKindNoMemberKind,
}

// CohortKindBasisCount is the closed vocabulary's size.
const CohortKindBasisCount = len(cohortKindBases)

// CohortKindBasisVocabulary returns the closed vocabulary in declared order.
func CohortKindBasisVocabulary() [CohortKindBasisCount]CohortKindBasis {
	return cohortKindBases
}

// ValidCohortKindBasis reports membership. The empty value is not a member:
// cohortKindFromFrame is total and always names a basis.
func ValidCohortKindBasis(value CohortKindBasis) bool {
	for _, member := range cohortKindBases {
		if member == value {
			return true
		}
	}
	return false
}

// cohortKindFromFrame is the whole substitution: the declared member kind, or
// the named reason there is not one.
//
// TOTAL AND PURE over a possibly-nil frame. It reads the union and nothing
// else -- not Shape, not RequestedJudgment, not SubjectTerms, not the family.
func cohortKindFromFrame(frame *contextfabric.QuestionFrame) (contextfabric.SubjectKind, CohortKindBasis) {
	if frame == nil {
		return "", CohortKindFrameAbsent
	}
	if !frame.SubjectExpression.IsCohortVariant() {
		return "", CohortKindNotACohortVariant
	}
	kind, ok := frame.SubjectExpression.MemberKind()
	if !ok {
		return "", CohortKindNoMemberKind
	}
	return kind, CohortKindFromFrameMemberKind
}

// frameKindHints returns the kinds this turn's frame declares, for the
// CHAOS-4348 kind-hinted pool search.
//
// It replaces the deleted kind-hint matcher, and the shape of the
// replacement matters:
// the old function had to be able to return NOTHING (a silent default would
// have turned every kindless question into a spurious `team` hint and
// defeated CHAOS-4348's own "no hint, no call" byte-identical requirement).
// Reading declared fields preserves that property for free -- a frame that
// declares no kind yields no hints -- without a keyword table.
//
// BOTH AXES OF A GROUPED EXPRESSION ARE HINTS. "Project statuses for each
// team" needs `project` (the members) and `team` (the grouping axis) in the
// pool, and the old matcher happened to get both only because the words for
// both appeared in the prose. Reading them from GroupKind and MemberKind is
// what makes that reliable rather than coincidental.
//
// Order is FIXED by the subject-kind vocabulary's own order, so a caller
// iterating the result never depends on Go's randomized map order -- the same
// guarantee the deleted matcher documented, kept for the same reason.
func frameKindHints(frame *contextfabric.QuestionFrame) []contextfabric.SubjectKind {
	if frame == nil {
		return nil
	}
	declared := make(map[contextfabric.SubjectKind]bool, 2)
	if kind, ok := frame.SubjectExpression.MemberKind(); ok {
		declared[kind] = true
	}
	if kind, ok := frame.SubjectExpression.GroupKind(); ok {
		declared[kind] = true
	}
	if len(declared) == 0 {
		return nil
	}
	var hints []contextfabric.SubjectKind
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		if declared[contextfabric.SubjectKind(kind)] {
			hints = append(hints, contextfabric.SubjectKind(kind))
		}
	}
	return hints
}
