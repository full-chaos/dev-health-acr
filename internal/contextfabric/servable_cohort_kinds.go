package contextfabric

import "sort"

// Whether a frame can produce a DISCOVERED COHORT at all, and the allow-list
// that decides it.
//
// WHY THIS LIVES HERE AND NOT IN THE DISCOVERY PACKAGE, which is the whole of
// the change that moved it. Two layers need this answer and only one of them
// could reach it:
//
//   - the discovery seam asks it to build a cohort, or to name why it did not;
//   - the requirement derivation asks it to decide whether a computed step
//     that RUNS OVER a resolved member set can be served at all.
//
// The discovery package imports this one, so the derivation could never call
// down to it. What happened instead is what always happens: the derivation
// rebuilt the predicate by hand, one condition at a time, and a review round
// found the next missing condition each time -- what a step READS versus what
// it RUNS OVER, then expression SHAPE versus declaring a member kind, then
// declaring a member kind versus that kind being SERVABLE. Three rounds, three
// conjuncts, and the fourth was going to be a COPY of the allow-list below --
// a deny-by-default table whose own comment says it grows only when an arm is
// proven, maintained in two places.
//
// The measured cost of the third gap: over the fifteen published subject
// kinds, the derivation served a ranking row for all fifteen while the seam
// could serve three. TWELVE cells claimed an ordering that nothing computed.
//
// So the dependency is inverted. The decision moves DOWN to the layer that
// already owns the subject-kind vocabulary, both layers call it, and a fourth
// condition cannot diverge because there is no second place to add one.
//
// WHAT DID NOT MOVE, deliberately. The discovery seam's own telemetry
// vocabulary stays in the discovery package: it publishes that vocabulary and
// it knows one thing this layer cannot (a turn that reached retrieval with no
// validated frame at all). It maps the reason below onto its vocabulary
// through an explicit total table, the same shape `unavailableRequirementCause`
// uses for the other cross-layer vocabulary pair in this package -- a mapping,
// never a cast, because the two are owned by different layers.

// servableCohortKinds is the set a DISCOVERY ARM CAN ACTUALLY SERVE. It is
// deliberately a DENY-BY-DEFAULT list, and it is deliberately NARROWER than
// the wire contract: since the contract widening, ContextFabricCohort.validate
// admits all fifteen published subject kinds, and being carriable is not the
// same fact as being discoverable. A subject kind added to the frame
// vocabulary stays unservable here until the change that PROVES an arm for
// it, which is the safe direction -- the unsafe one answers a real question
// with a cohort nothing filled in.
//
// `repository` was added by the change that proved the arm, not by a tidy-up
// to match the contract. The proof is a fixture per cohort variant that can
// carry the kind -- grouped_members, children_of_scope and discovered_kind --
// each driving DiscoverContext through the falkorgraph reader, each red
// before this line moved. The candidate pool was never the obstacle: the
// exact-name census already fetches repository nodes.
//
// NOT every question about repositories reaches this map. "Open incidents per
// repository" declares repository as the GROUPING AXIS and `incident` as the
// member kind -- invariant I6 refuses a grouped expression that groups a kind
// by itself -- so it refuses here on `incident`, and serving it is an
// incident-cohort arm with its own candidate pool, tracked separately.
//
// MOVED, not rewritten. This table's membership is byte-for-byte what the
// discovery package carried; the change that moved it proved that by keeping
// the pin test that asserts the exact three members. A move that also widened
// the table would have hidden a policy change inside a refactor.
var servableCohortKinds = map[SubjectKind]bool{
	SubjectTeam:       true,
	SubjectProject:    true,
	SubjectRepository: true,
}

// CohortDiscoverability names WHY a subject expression can or cannot produce a
// discovered cohort. Closed, and exhaustive over the expression union.
//
// It is a vocabulary rather than a bool because the three refusing cases are
// actionable by different parties and a caller that collapses them cannot say
// which: `not_a_cohort_variant` means the question named one subject and there
// is nothing to enumerate; `no_member_kind` means the variant enumerates but
// declares no kind to enumerate; `member_kind_unservable` means the kind was
// declared and no discovery arm exists for it, which is the one a NEW ARM
// would fix. Collapsing any two of them produces a bare count.
type CohortDiscoverability string

const (
	// CohortDiscoverable: the expression is a cohort variant, it declares a
	// member kind, and an arm exists for that kind. The only member that
	// discovers.
	CohortDiscoverable CohortDiscoverability = "discoverable"
	// CohortNotACohortVariant: the expression names ONE subject
	// (named_subject) or the organization itself, with nothing to
	// enumerate. Discovering a cohort here would invent a set the question
	// never asked for.
	//
	// THIS CONDITION IS LOAD-BEARING AND IS NOT SUBSUMED BY THE KIND TEST
	// BELOW, which is the trap that makes this a switch rather than a
	// single lookup. `SubjectExpression.MemberKind()` answers `ok` for
	// `named_subject` (it reads ExpectedKind, so the kind-hinted pool
	// search stops treating a named subject as kindless) and for
	// `organization_scope` (it reads the optional Org.MemberKind). Both are
	// legal frames, both can declare `team`, and neither can ever produce a
	// cohort. Deciding this on the declared kind alone would serve a
	// ranking row for both -- the exact defect the change before this one
	// shipped a red-at-parent proof against.
	CohortNotACohortVariant CohortDiscoverability = "not_a_cohort_variant"
	// CohortNoMemberKind: the expression IS a cohort variant but declares
	// no member kind. `explicit_set` is the case that reaches this: its
	// operands are NAMED, each with its own kind, so its members come from
	// subject resolution and there is nothing for graph discovery to find.
	CohortNoMemberKind CohortDiscoverability = "no_member_kind"
	// CohortMemberKindUnservable: the expression declares a member kind and
	// no discovery arm serves it. The declared kind is reported beside this
	// reason, because a refusal a run cannot attribute to a kind is a
	// refusal someone will attribute by guessing.
	CohortMemberKindUnservable CohortDiscoverability = "member_kind_unservable"
)

var cohortDiscoverabilityReasons = [...]CohortDiscoverability{
	CohortDiscoverable,
	CohortNotACohortVariant,
	CohortNoMemberKind,
	CohortMemberKindUnservable,
}

// CohortDiscoverabilityCount is four.
const CohortDiscoverabilityCount = len(cohortDiscoverabilityReasons)

// CohortDiscoverabilityVocabulary returns the closed vocabulary in declared
// order.
func CohortDiscoverabilityVocabulary() [CohortDiscoverabilityCount]CohortDiscoverability {
	return cohortDiscoverabilityReasons
}

// ValidCohortDiscoverability reports membership. The empty value is not a
// member: CohortMemberKindFor is total and always names a reason.
func ValidCohortDiscoverability(value CohortDiscoverability) bool {
	for _, member := range cohortDiscoverabilityReasons {
		if member == value {
			return true
		}
	}
	return false
}

// CohortMemberKindFor is THE predicate: can this expression produce a
// discovered cohort, and of what kind.
//
// TOTAL AND PURE over the expression union. It reads the union and the
// allow-list and nothing else -- not Shape, not RequestedJudgment, not
// SubjectTerms, not the family.
//
// TWO KINDS COME BACK AND THE DIFFERENCE IS THE POINT.
//
//   - `servable` is what a caller may BUILD A COHORT FROM. It is empty on
//     every refusing reason, including member_kind_unservable, so a caller
//     that ignored the reason still cannot construct the cohort the refusal
//     exists to prevent. That emptiness is pinned by a test.
//   - `declared` is what the EXPRESSION SAID, reported for telemetry and for
//     nothing else. On member_kind_unservable it is the refused kind; on
//     every other refusing reason there was no declared kind to report and it
//     is empty too.
//
// THE ORDER OF THE THREE TESTS IS PART OF THE CONTRACT, not an implementation
// detail: variant first, then kind, then arm. Reordering them would change
// which reason a frame gets, and each reason points at a different party.
func CohortMemberKindFor(expression SubjectExpression) (servable SubjectKind, declared SubjectKind, reason CohortDiscoverability) {
	if !expression.IsCohortVariant() {
		return "", "", CohortNotACohortVariant
	}
	kind, ok := expression.MemberKind()
	if !ok {
		return "", "", CohortNoMemberKind
	}
	if !servableCohortKinds[kind] {
		return "", kind, CohortMemberKindUnservable
	}
	return kind, kind, CohortDiscoverable
}

// CohortMemberSetResolvable is the boolean projection of the predicate above,
// for the callers that need the decision and not the reason.
//
// It is defined AS the predicate rather than beside it so the two cannot
// disagree: a caller reading this and a caller reading the reason are reading
// one function.
func CohortMemberSetResolvable(expression SubjectExpression) bool {
	_, _, reason := CohortMemberKindFor(expression)
	return reason == CohortDiscoverable
}

// ServableCohortKindsForAudit returns the allow-list's members, sorted, so a
// test in a package that can see the real fact providers can quantify over
// "every kind an arm exists for" without importing the map.
//
// It exists for that audit alone. No decision calls it -- CohortMemberKindFor
// reads the map directly -- so this function cannot become a second, drifting
// definition of what is servable.
func ServableCohortKindsForAudit() []SubjectKind {
	kinds := make([]SubjectKind, 0, len(servableCohortKinds))
	for kind, admitted := range servableCohortKinds {
		if admitted {
			kinds = append(kinds, kind)
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}
