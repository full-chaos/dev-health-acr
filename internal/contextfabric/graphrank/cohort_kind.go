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
	// CohortKindMemberKindUnservable: the frame declared a member kind the
	// COHORT WIRE CONTRACT cannot carry, so no cohort is built.
	//
	// FOUND ON THE RIG, not by reading code. contracts/v1's
	// ContextFabricCohort.validate permits exactly two kinds -- team and
	// project -- and refuses every other with "cohort violates v1 bounds".
	// The deleted prose matcher could only ever RETURN those two (it
	// returned project on a "project"/"initiative" hit and otherwise
	// defaulted to team), so that bound was unreachable for the entire life
	// of the old code. Reading the frame's declared MemberKind makes it
	// reachable for the first time: a question about repositories now
	// declares `repository`, discovery builds a repository cohort, and the
	// validator refuses the whole ANSWER -- an HTTP 500, not a degraded
	// answer. That is strictly worse than the wrong-kind cohort it replaced.
	//
	// So the consumer refuses FIRST, and says so. Widening the wire contract
	// to carry more cohort kinds is a contract change with a schema, an
	// OpenAPI document, an MCP manifest, fixtures and a consumer pin behind
	// it; it is not this slice's to make, and it is tracked separately as
	// the repository-cohort work. Until then a cohort kind outside the
	// contract is a REPORTED limitation with its own basis, which is what
	// makes it countable rather than a crash.
	CohortKindMemberKindUnservable CohortKindBasis = "member_kind_unservable"
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
	CohortKindMemberKindUnservable,
}

// WHERE THE ALLOW-LIST WENT, and why it is not here any more.
//
// `servableCohortKinds` -- the deny-by-default set of subject kinds a
// discovery arm can actually serve -- and its audit accessor used to live at
// this point in this file. They now live in `internal/contextfabric` beside
// the subject-kind vocabulary, together with the whole decision below them:
// `contextfabric.CohortMemberKindFor`.
//
// THE MOVE WAS FORCED BY A MEASURED DISAGREEMENT, not by tidiness. The
// requirement derivation must know whether a computed step that runs over a
// resolved member set can be served, and it cannot call into this package --
// this package imports it. So it rebuilt the predicate by hand, and three
// review rounds each found the next missing condition. Over the fifteen
// published subject kinds the derivation served a ranking row for all fifteen
// while this seam could serve three: TWELVE cells claiming an ordering that
// nothing computed. A fourth conjunct would have been a COPY of the table,
// which is where the drift restarts.
//
// So the decision moved DOWN and this file consumes it. There is no second
// table and no second predicate to keep in step.
//
// WHAT STAYED. `CohortKindBasis` above is this package's published telemetry
// vocabulary and it stays this package's, for two reasons: the vocabulary is
// what this seam's log lines and events carry, and this seam knows one thing
// the layer below cannot -- that retrieval was reached with no validated
// frame at all (`frame_absent`). The reason the shared predicate returns is
// mapped onto the basis by the explicit total table below.

// cohortKindBasisForDiscoverability maps the shared predicate's reason onto
// this seam's published basis vocabulary.
//
// EXPLICIT AND TOTAL rather than a cast, because the two vocabularies are
// owned by different layers: a member added below would otherwise reach this
// seam's telemetry as a basis this seam never declared. The default arm is
// not a fallback -- it is unreachable while the two vocabularies agree, and
// the vocabulary test asserts they do by quantifying over
// `contextfabric.CohortDiscoverabilityVocabulary()` and requiring every
// member to map to a valid basis. It returns the unservable basis rather than
// an empty one so a future member cannot make this function return a value
// `ValidCohortKindBasis` refuses, which would be a worse failure than a
// slightly wrong-but-refusing basis.
//
// `frame_absent` is deliberately NOT produced here. Only the caller knows
// there was no frame; the predicate below is total over an expression that
// exists.
func cohortKindBasisForDiscoverability(reason contextfabric.CohortDiscoverability) CohortKindBasis {
	switch reason {
	case contextfabric.CohortDiscoverable:
		return CohortKindFromFrameMemberKind
	case contextfabric.CohortNotACohortVariant:
		return CohortKindNotACohortVariant
	case contextfabric.CohortNoMemberKind:
		return CohortKindNoMemberKind
	case contextfabric.CohortMemberKindUnservable:
		return CohortKindMemberKindUnservable
	default:
		return CohortKindMemberKindUnservable
	}
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
//
// TWO KINDS COME BACK AND THE DIFFERENCE IS THE POINT.
//
//   - `servable` is what a caller may BUILD A COHORT FROM. It is empty on
//     every refusing basis, including member_kind_unservable, so a caller
//     that ignored the basis still cannot construct the cohort the refusal
//     exists to prevent. That emptiness is pinned by a test.
//   - `declared` is what the FRAME SAID, reported for telemetry and for
//     nothing else. On member_kind_unservable it is the refused kind; on
//     every other refusing basis there was no declared kind to report and it
//     is empty too.
//
// The split was added because the two had been the same value, which meant a
// refusal could say THAT a member kind was unservable but never WHICH. A
// question whose repository noun was its grouping axis rather than its member
// kind was read from its question text instead, in three separate documents,
// and the reading was wrong. A refusal a run cannot attribute to a kind is a
// refusal someone will attribute by guessing.
//
// IT NO LONGER DECIDES; IT ADAPTS. The three conditions it used to test in
// line are the shared predicate now, so this function is the nil-frame case
// plus a vocabulary mapping. Both returned kinds come straight from the
// predicate, so the guarantees documented above are the predicate's
// guarantees and cannot be weakened here by accident.
func cohortKindFromFrame(frame *contextfabric.QuestionFrame) (servable contextfabric.SubjectKind, declared contextfabric.SubjectKind, basis CohortKindBasis) {
	// The ONLY condition this seam decides for itself, because it is the
	// only one the layer below cannot see: a turn that reached retrieval
	// with no validated frame has no expression to ask about.
	if frame == nil {
		return "", "", CohortKindFrameAbsent
	}
	// Everything else is the shared predicate -- is it a cohort variant,
	// does it declare a member kind, is there an arm for that kind -- stated
	// once, in the layer both this seam and the requirement derivation can
	// reach.
	servableKind, declaredKind, reason := contextfabric.CohortMemberKindFor(frame.SubjectExpression)
	return servableKind, declaredKind, cohortKindBasisForDiscoverability(reason)
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
