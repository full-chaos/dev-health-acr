package contextfabric

import (
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4636: building the grouped cohort (design §6.2, acceptance question
// Q-A -- "what are the project statuses for each team, and what are the main
// drivers?").
//
// WHERE THE GROUPING COMES FROM, and why it is the inverse of the design's
// own phrasing. §6.2 describes nested iteration: "resolve the groups (teams),
// then for each group resolve its members (projects)". That direction is NOT
// available for a subjectless cohort with the mechanisms that exist today,
// and this was verified rather than assumed:
//
//   - falkorgraph's hopWalk runs only `for _, subject := range
//     request.Resolution.Committed`. Q-A commits nothing -- it is a
//     subjectless discovered cohort -- so hopWalk never runs, and the
//     OWNED_BY_TEAM edges it would have surfaced are never fetched.
//   - the subjectless path is served by chaos4348ExactNameCandidates, whose
//     own doc comment records that it never touches Paths, so
//     GraphContext.Paths is structurally empty of ownership edges here.
//   - CHAOS-4099's factScopeEligibility table has NO team-to-project or
//     project-to-team policy, and its "WHAT IS DELIBERATELY ABSENT" block
//     rejects that direction explicitly. It also yields a fact-READ
//     PERMISSION, never a subject list, so it is the wrong instrument even
//     where it applies.
//
// What IS available, free, at exactly the right moment: the project-subject
// facts the plan already reads carry their owning team INSIDE their declared
// table's Key columns -- metrics' team_breakdown keys on
// (team_id, team_name, day), workload's breakdown on
// (team_id, team_name, work_scope_id, computed_at), health's risk_breakdown
// carries scope/scope_id/scope_name rows where scope == "team", and flow
// partitions on (team_id, provider, work_scope_id). So the group is read off
// the members' OWN facts, after the fact read and before synthesis.
//
// Same answer, no new port, no new query. The consequence is stated rather
// than hidden: GROUPS EXIST ONLY AFTER THE FACT READ. That is why the
// pre-read stage can clamp nothing but the flat member cap, and why
// member-first narrowing (decision D2) necessarily lives in stages 2 and 3.

// groupColumnCandidates are the row columns that name an owning team, in
// preference order. They are the columns the producers actually emit, not a
// guess: see this file's header for the four providers and their declared
// Key tuples.
//
// Ordered because a row can carry more than one: health's risk_breakdown
// carries a generic (scope, scope_id, scope_name) triple that is only a team
// when scope == "team", while metrics and workload carry an unambiguous
// team_id directly. The unambiguous columns win.
var groupIDColumnCandidates = []string{"team_id", "scope_id"}

var groupLabelColumnCandidates = []string{"team_name", "scope_name"}

// groupScopeColumn and groupScopeTeamValue are health's own discriminator:
// its breakdown rows are per-scope, and only the rows whose scope says "team"
// name a team. A row carrying scope="project" would otherwise be read as a
// team whose id is a project's, which is a wrong grouping rather than a
// missing one.
const (
	groupScopeColumn   = "scope"
	groupScopeTeamKind = "team"
)

// cohortGroupAssignment is one member's derived group.
type cohortGroupAssignment struct {
	canonicalID string
	label       string
	// kind is the subject kind the SOURCE says this group is, decided where
	// the row was accepted rather than anywhere later. groupAssignmentsFromValue
	// admits a row only when its scope column says "team" and reads
	// team_id/team_name, so every assignment it produces is a team by
	// construction -- this field simply stops that knowledge being thrown
	// away and re-derived from somewhere less trustworthy.
	kind SubjectKind
}

// CohortGroupingRefusal is the CLOSED vocabulary naming why grouping refused
// to build, for telemetry. Empty means it did not refuse.
type CohortGroupingRefusal string

const (
	// CohortGroupingRefusalNone is the absence of a refusal.
	CohortGroupingRefusalNone CohortGroupingRefusal = ""
	// CohortGroupingRefusalGroupKindSourceMismatch: the plan's declared group
	// kind is not the kind the facts actually group by.
	//
	// The plan's GroupKind comes from the model's own question frame; a
	// group's canonical id and label come from a canonical fact row. Stamping
	// the former onto the latter produces a subject whose identity half is
	// model-chosen and half source-chosen -- e.g. {repository, team_security}
	// from rows that are unambiguously team rows. That identity exists in no
	// source, and once group subjects became citable it would have been a
	// forgery route into a served answer. Fail closed: build nothing.
	CohortGroupingRefusalGroupKindSourceMismatch CohortGroupingRefusal = "group_kind_source_mismatch"
	// CohortGroupingRefusalNoMemberPlaced: the plan declared a group axis
	// and NOT ONE member could be placed on it, because no member's own
	// canonical facts carried a group-scoped row at all.
	//
	// DISTINCT FROM THE MISMATCH ABOVE, in the one way that matters to
	// whoever reads the artifact. A mismatch means the source DISAGREED: it
	// grouped by something, and naming that something tells the reader where
	// to look instead. This means the source was SILENT -- there is no other
	// axis to point at, and SourceKind stays zero because there is no kind to
	// name rather than because the field was forgotten.
	//
	// DISTINCT FROM A PARTIAL PLACEMENT, which is not a refusal at all. Some
	// members unplaced while groups were built is the deliberate, documented
	// behaviour of BuildCohortGroups and is pinned by
	// TestBuildCohortGroupsLeavesAnUnplaceableMemberUngrouped; stamping this
	// token there would relabel a served grouped answer as a refusal. The
	// firing condition is ZERO groups built, never "some member was unplaced".
	//
	// WHY THIS IS A REFUSAL AND NOT A NEW VOCABULARY. The engine's own
	// comment on the mismatch branch already calls this case "the same
	// posture as the nothing-placed branch below, for a different and more
	// serious reason": both deliver no group axis, both clear plan.GroupKind,
	// both owe the reader a sentence. Two postures, one vocabulary.
	CohortGroupingRefusalNoMemberPlaced CohortGroupingRefusal = "no_member_placed"
)

// canonicalCohortGroupingRefusals maps each member to ITSELF, so a telemetry
// lookup returns a compile-time constant rather than the caller's own value --
// the same reasoning every other closed vocabulary in this package documents.
var canonicalCohortGroupingRefusals = map[CohortGroupingRefusal]CohortGroupingRefusal{
	CohortGroupingRefusalNone:                    CohortGroupingRefusalNone,
	CohortGroupingRefusalGroupKindSourceMismatch: CohortGroupingRefusalGroupKindSourceMismatch,
	CohortGroupingRefusalNoMemberPlaced:          CohortGroupingRefusalNoMemberPlaced,
}

// CohortGroupingOutcome carries WHY grouping refused and the two kinds that
// disagreed, so a caller can both log the refusal and DISCLOSE it to the
// reader without re-deriving either kind. Both kinds are closed-vocabulary
// subject kinds; neither is model text.
type CohortGroupingOutcome struct {
	Refusal CohortGroupingRefusal
	// PlannedKind is what the question frame asked to group by.
	PlannedKind SubjectKind
	// SourceKind is what the facts actually group by. Zero unless a refusal
	// names a mismatch -- on CohortGroupingRefusalNoMemberPlaced the source
	// named no kind at all, so zero there is the truthful value rather than
	// an unset one.
	SourceKind SubjectKind
	// Ungrouped is how many members could not be placed on the planned axis.
	//
	// Non-zero only with CohortGroupingRefusalNoMemberPlaced, where it is the
	// whole cohort by construction. BuildCohortGroups already returns this
	// number as its second result; carrying it HERE is what lets the reader's
	// disclosure and the operator's telemetry line read one object instead of
	// each re-deriving it from a sibling variable at its own call site -- the
	// re-derivation being where a count and its reason drift apart.
	//
	// It is deliberately NOT populated on a partial placement: a partial
	// placement is not a refusal, this struct is the refusal record, and a
	// count on a zero-valued refusal would be a number with no sentence.
	Ungrouped int
}

// ValidCohortGroupingRefusal reports membership of the closed vocabulary.
func ValidCohortGroupingRefusal(reason CohortGroupingRefusal) bool {
	_, ok := canonicalCohortGroupingRefusals[reason]
	return ok
}

// BuildCohortGroups partitions cohort by the group kind the plan declared,
// reading each member's owning group off that member's own canonical facts.
//
// It returns the groups and the number of members it could NOT place. An
// unplaced member is not an error and is not dropped: it stays in the
// flattened Members list, ungrouped, and is counted so the caller can be told.
// That case is real on this data -- the providers carrying the team
// association join on compounding risk, so a member whose facts came back
// empty genuinely has no derivable group -- and inventing a group for it, or
// silently removing it, would both be worse than saying so.
//
// Returns nil groups when the plan declares no group axis, when the cohort is
// absent, or when NOTHING could be grouped. A grouped answer with zero groups
// is not a grouped answer; falling back to the flat one is the honest
// degradation, and the plan's own validation is what then reports that the
// group axis went unsatisfied.
//
// CHAOS-4733: every returned group's Complete is seeded from cohort.Complete
// as it stands on entry -- the DISCOVERY-level (pre-grouping) flag
// DiscoveredCohort or a truncated census set, not a bare true. A discovery
// cap is a cap on the WHOLE cohort before any group exists, so a member
// never discovered could belong to any group; no group built from a capped
// cohort may claim Complete=true. Truncated is deliberately NOT seeded the
// same way -- it stays false here, because Truncated is this group's own
// Total-vs-presented signal (see ContextFabricCohortGroup's doc comment) and
// nothing has trimmed anything yet; the discovery-level truncation is
// preserved at the COHORT level instead (ApplyGroupedCohortCompleteness).
// Together these are what make grouping a CONSERVATIVE fold rather than a
// rewrite: cohort.Complete can come back true after grouping only if it was
// already true before grouping, and cohort.Truncated never comes back false
// if it was already true.
func BuildCohortGroups(plan AnswerPlan, cohort *Cohort, facts []CanonicalFact) (groups []contractsv1.ContextFabricCohortGroup, ungrouped int, outcome CohortGroupingOutcome) {
	if plan.GroupKind == "" || cohort == nil || len(cohort.Members) == 0 {
		return nil, 0, CohortGroupingOutcome{}
	}
	assignments := groupAssignmentsByMember(facts)
	// Preserve the cohort's own member order inside each group, and order
	// the groups themselves by canonical id. Both are deterministic: a
	// grouped answer whose group order varied between two identical
	// requests would make every before/after comparison meaningless.
	order := make([]string, 0, len(cohort.Members))
	byGroup := make(map[string][]string, len(cohort.Members))
	labels := make(map[string]string, len(cohort.Members))
	kinds := make(map[string]SubjectKind, len(cohort.Members))
	for _, member := range cohort.Members {
		placed := assignments[SubjectMapKey(member.Subject)]
		if len(placed) == 0 {
			ungrouped++
			continue
		}
		// The kind check is per assignment and fails the WHOLE grouping, not
		// just this member: a grouped answer that silently kept the members
		// whose source happened to agree would present a partial group axis
		// as a complete one.
		for _, assignment := range placed {
			if assignment.kind != plan.GroupKind {
				return nil, 0, CohortGroupingOutcome{
					Refusal:     CohortGroupingRefusalGroupKindSourceMismatch,
					PlannedKind: plan.GroupKind,
					SourceKind:  assignment.kind,
				}
			}
		}
		// EVERY owning group, not the first one seen. A project genuinely
		// owned by two teams belongs under both, and dropping one would be
		// dropping a true ownership -- exactly what ValidateCohortGroups'
		// own doc comment says a validator must not force. (codex round 1,
		// finding 2: the contract allowed many-to-many and this loop kept
		// one assignment per subject, so the two disagreed and the contract
		// was the correct one.)
		for _, assignment := range placed {
			if _, known := byGroup[assignment.canonicalID]; !known {
				order = append(order, assignment.canonicalID)
				labels[assignment.canonicalID] = assignment.label
				kinds[assignment.canonicalID] = assignment.kind
			}
			byGroup[assignment.canonicalID] = append(byGroup[assignment.canonicalID], member.Subject.CanonicalID)
		}
	}
	if len(order) == 0 {
		// NOT ONE member could be placed. This used to return a zero-valued
		// outcome, which made the case indistinguishable from "grouping was
		// never attempted" for anyone reading the persisted artifact: the
		// only surviving trace was the caller clearing plan.GroupKind, and a
		// cleared group kind looks exactly like a plan that never had one.
		//
		// THIS CONDITION WAS ONCE `len(order) == 0 && ungrouped > 0`, and the
		// mutation battery is why it is not any more. The second conjunct was
		// written on the claim that it kept the token from being stamped over
		// an empty population "however the guard at the top is later edited",
		// and that deleting it would be caught by a fixture. Deleting it
		// killed NOTHING: reaching here at all means the loop above ran over a
		// non-empty cohort, and every member of it either landed in `order` or
		// incremented `ungrouped`, so `len(order) == 0` already implies
		// `ungrouped > 0`. The conjunct was unreachable as a discriminator and
		// therefore untested code dressed as a safety property -- an empty
		// cell in the battery is a finding, not a pass, and the honest fix for
		// a rule no fixture can isolate is to remove it as subsumed rather
		// than to keep it for the comfort of the comment beside it.
		//
		// The property it CLAIMED to hold is real and is held where it is
		// actually decidable: the guard at the top of this function returns a
		// zero-valued outcome for an empty or absent cohort, and
		// TestAnEmptyCohortIsNotARefusal pins that, so the token cannot be
		// stamped over a population of nobody.
		return nil, ungrouped, CohortGroupingOutcome{
			Refusal:     CohortGroupingRefusalNoMemberPlaced,
			PlannedKind: plan.GroupKind,
			// SourceKind stays zero: nothing disagreed, nothing was named.
			Ungrouped: ungrouped,
		}
	}
	sort.Strings(order)
	groups = buildGroupsFrom(cohort, order, byGroup, labels, kinds)
	return groups, ungrouped, CohortGroupingOutcome{}
}

// groupAssignmentsByMember reads the owning group out of each fact's declared
// tables, keyed by the fact's own subject.
//
// It reads DECLARED tables (FactValue.Table) and falls back to the sibling
// Rows the same producers still populate, because CHAOS-4633's migration is
// deliberately dual-write: a producer emits both, and reading only one of
// them would make this depend on which phase of that migration is deployed.
// buildGroupsFrom constructs the group entries from assignments the caller's
// kind guard has already admitted.
//
// It deliberately does NOT receive the plan. A group subject's Kind is the
// SOURCE's kind and nothing else, and the cheapest way to guarantee that is to
// put the plan's kind out of scope entirely -- a stamp cannot regress to a
// value it cannot name. Adversarial review observed that reverting the stamp to
// plan.GroupKind was an EQUIVALENT mutant while the guard stood, meaning the
// guard was carrying the whole property on its own. This makes source authority
// structural instead: the defect is now unexpressible here rather than merely
// unreachable.
func buildGroupsFrom(cohort *Cohort, order []string, byGroup map[string][]string, labels map[string]string, kinds map[string]SubjectKind) []contractsv1.ContextFabricCohortGroup {
	groups := make([]contractsv1.ContextFabricCohortGroup, 0, len(order))
	for _, canonicalID := range order {
		members := byGroup[canonicalID]
		label := labels[canonicalID]
		if label == "" {
			label = canonicalID
		}
		groups = append(groups, contractsv1.ContextFabricCohortGroup{
			Subject:            SubjectRef{Kind: kinds[canonicalID], CanonicalID: canonicalID, Label: label},
			MemberCanonicalIDs: members,
			// Total is the group's membership AS DISCOVERED. It is not a
			// claim about how many projects the team owns in the world:
			// nothing here read that, and asserting it would be inventing
			// a number. A later narrowing lowers the listed members and
			// leaves Total where it was, which is what makes the
			// truncation disclosure true.
			Total: len(members),
			// Complete as built INHERITS the pre-grouping cohort's own
			// discovery-level completeness (CHAOS-4733), never a bare
			// true. cohort.Members here is the set discovery actually
			// returned; if discovery capped it (cohort.Truncated) or a
			// truncated census left it incomplete (!cohort.Complete), an
			// undiscovered member could belong to ANY group -- there is no
			// way to know which -- so no group can honestly claim
			// completeness either. Narrowing (NarrowGroupedCohort) ANDs a
			// further drop onto this same field; it must start from an
			// honest value or "every group survives narrowing" would still
			// read Complete=true over a cohort that was never fully seen.
			Complete: cohort.Complete,
			// Truncated stays false here on purpose (CHAOS-4733): it is
			// this group's OWN presented-vs-Total signal (Truncated implies
			// Total > len(MemberCanonicalIDs), enforced by
			// ContextFabricCohortGroup.Validate), and nothing has trimmed
			// this group relative to its own Total yet -- Total was just
			// set to len(members) two lines up. A discovery-level cap that
			// happened BEFORE any group existed is not a fact about any
			// ONE group's presented-vs-total ratio, so it cannot be
			// expressed here without breaking that invariant; it is
			// carried instead as the cohort-level Truncated signal (see
			// ApplyGroupedCohortCompleteness) -- option (b) of CHAOS-4733's
			// acceptance criteria, not (a). NarrowGroupedCohort is the only
			// place that legitimately sets a group's Truncated true, and it
			// does so by actually shrinking MemberCanonicalIDs below Total.
			Truncated: false,
		})
	}
	return groups
}

func groupAssignmentsByMember(facts []CanonicalFact) map[string][]cohortGroupAssignment {
	assignments := make(map[string][]cohortGroupAssignment, len(facts))
	seen := make(map[string]map[string]struct{}, len(facts))
	for _, fact := range facts {
		key := SubjectMapKey(fact.Subject)
		for _, value := range fact.Fields {
			for _, assignment := range groupAssignmentsFromValue(value) {
				if _, known := seen[key]; !known {
					seen[key] = make(map[string]struct{}, 2)
				}
				if _, duplicate := seen[key][assignment.canonicalID]; duplicate {
					continue
				}
				seen[key][assignment.canonicalID] = struct{}{}
				assignments[key] = append(assignments[key], assignment)
			}
		}
	}
	// Sorted per member so the group order a member contributes is stable
	// across runs: the fact-field iteration above walks a Go map.
	for key := range assignments {
		sort.Slice(assignments[key], func(i, j int) bool {
			return assignments[key][i].canonicalID < assignments[key][j].canonicalID
		})
	}
	return assignments
}

// groupAssignmentsFromValue pulls EVERY group id and label out of one tabular
// fact value. A breakdown table legitimately carries one row per owning team,
// so returning only the first row's team is how finding 2's defect arose.
func groupAssignmentsFromValue(value FactValue) []cohortGroupAssignment {
	rows := value.Rows
	if value.Table != nil && len(value.Table.Rows) > 0 {
		rows = value.Table.Rows
	}
	found := make([]cohortGroupAssignment, 0, len(rows))
	for _, row := range rows {
		// health's breakdown is per-scope and only its team rows name a
		// team. A row that declares a scope at all must declare the team
		// scope, or its ids belong to something else entirely.
		if scope, declared := rowString(row, groupScopeColumn); declared && scope != groupScopeTeamKind {
			continue
		}
		canonicalID, ok := firstRowString(row, groupIDColumnCandidates)
		if !ok || canonicalID == "" {
			continue
		}
		label, _ := firstRowString(row, groupLabelColumnCandidates)
		// SubjectTeam, not the plan's kind: the scope filter above admitted
		// this row precisely because it declares itself a team row, and the
		// columns just read are team columns.
		found = append(found, cohortGroupAssignment{canonicalID: canonicalID, label: label, kind: SubjectTeam})
	}
	return found
}

func rowString(row FactValueRow, column string) (string, bool) {
	value, present := row.Fields[column]
	if !present || value.String == nil {
		return "", false
	}
	return *value.String, true
}

func firstRowString(row FactValueRow, columns []string) (string, bool) {
	for _, column := range columns {
		if value, found := rowString(row, column); found && value != "" {
			return value, true
		}
	}
	return "", false
}

// ApplyGroupedCohortCompleteness FOLDS the cohort-level booleans down to the
// conjunction/disjunction over the groups -- it narrows what is already
// there, it does not replace it.
//
// This is what stops an old reader going group-blind. Complete over the flat
// union cannot say "team A complete, team B truncated"; defining it as "every
// group complete" means a reader that ignores Groups gets a CONSERVATIVE
// answer -- never Complete: true over a partially-truncated union -- rather
// than a boolean that happened to describe only whichever group came first.
//
// CHAOS-4733: this reads cohort.Complete/cohort.Truncated as they stand ON
// ENTRY -- at the first call in a request, that is the PRE-GROUPING,
// discovery-level state DiscoveredCohort (or a truncated census) set, before
// any group existed -- and ANDs/ORs the group-derived values into them,
// rather than overwriting them outright. A discovery-level truncation is a
// fact about the WHOLE cohort before grouping, not a fact any one group's
// own Total/MemberCanonicalIDs ratio can carry (see
// ContextFabricCohortGroup.Total's doc comment: a group whose Total equals
// its member count cannot legally claim Truncated=true), so it is preserved
// here, at the cohort level, instead of being pushed into the groups --
// CHAOS-4733 acceptance criterion 3's option (b). Complete happens to
// reconcile with the group conjunction too, because BuildCohortGroups also
// seeds every group's own Complete from this same pre-grouping value; the OR
// on Truncated is what a pure group-only derivation could never express.
//
// Called a second time after stage-2/3 narrowing (over the NARROWED groups),
// this composes correctly for the same reason: it ANDs/ORs onto whatever the
// first call already produced, so a later, less-restrictive group state can
// never re-loosen an earlier, more-restrictive one -- only narrowing itself
// (a real trim) can tighten it further.
func ApplyGroupedCohortCompleteness(cohort *Cohort) {
	if cohort == nil || len(cohort.Groups) == 0 {
		return
	}
	groupComplete, groupTruncated := contractsv1.CohortCompletenessFromGroups(cohort.Groups)
	cohort.Complete = cohort.Complete && groupComplete
	cohort.Truncated = cohort.Truncated || groupTruncated
}

// NarrowGroupedCohort selects which members a grouped cohort's budget
// admits, exploiting overlap: group membership is many-to-many, and one
// shared member can cover several groups at once (CHAOS-4678). It calls
// contractsv1.SelectGroupCoverMembers, which is EXACT for group counts up to
// contractsv1.ContextFabricSetCoverGroupGuard and falls back to the
// pre-CHAOS-4678 largest-group-round-robin order beyond it.
//
// This is decision D2 -- MEMBER-FIRST -- ruled by the orchestrator on
// 2026-08-30: "'for each team' is the question's own words", so dropping a
// team answers a question that was not asked, while trimming members answers
// the asked question less completely. "Less completely" is a disclosure this
// contract already knows how to make, via per-group Truncated; "we silently
// answered about fewer teams" is not.
//
// Every group that the budget CAN cover -- accounting for sharing -- keeps
// at least one member; a group goes uncovered only when the budget is too
// small to cover it even at maximum overlap. It returns the members that
// remain, in the cohort's own order, the groups with their completeness
// updated, and the basis the selection actually used (for the caller's
// telemetry -- see contracts/v1's ContextFabricNarrowingBasis).
func NarrowGroupedCohort(cohort *Cohort, maxMembers int) (kept []CohortMember, groups []contractsv1.ContextFabricCohortGroup, narrowed bool, basis contractsv1.ContextFabricNarrowingBasis) {
	if cohort == nil || maxMembers <= 0 || len(cohort.Groups) == 0 {
		return nil, nil, false, ""
	}
	// DISTINCT MEMBERS, not memberships. Groups overlap -- a project owned by
	// two teams appears in both -- so summing group sizes over-counts, and
	// deciding on that sum made narrowing report "nothing to do" for a cohort
	// that could in fact be narrowed (codex round 1, finding 3). The budget
	// bounds the flattened member list, which charges each member once, so
	// the decision has to be taken on the same quantity.
	//
	// Ungrouped members -- ones BuildCohortGroups could not place -- are a
	// real case (its own doc comment: inventing a group for one, or
	// silently removing it, "would both be worse than saying so"), and they
	// are cohort members like any other: dropping them silently here would
	// be the same data-loss defect this function used to carry. They are
	// narrowed like a flat cohort's tail rather than protected like a
	// group: no per-group Truncated disclosure covers them, so they may
	// reach zero, whereas a group dropping to zero would be the silent
	// group loss decision D2 forbids.
	claimed := make(map[string]struct{}, len(cohort.Members))
	for _, group := range cohort.Groups {
		for _, id := range group.MemberCanonicalIDs {
			claimed[id] = struct{}{}
		}
	}
	var ungrouped []string
	for _, member := range cohort.Members {
		if _, held := claimed[member.Subject.CanonicalID]; !held {
			ungrouped = append(ungrouped, member.Subject.CanonicalID)
		}
	}
	distinct := len(claimed) + len(ungrouped)
	if distinct <= maxMembers {
		return nil, nil, false, ""
	}
	survivors, basis := contractsv1.SelectGroupCoverMembers(cohort.Groups, ungrouped, maxMembers)
	groups = make([]contractsv1.ContextFabricCohortGroup, 0, len(cohort.Groups))
	for _, group := range cohort.Groups {
		keptIDs := make([]string, 0, len(group.MemberCanonicalIDs))
		for _, id := range group.MemberCanonicalIDs {
			if _, ok := survivors[id]; ok {
				keptIDs = append(keptIDs, id)
			}
		}
		narrowedGroup := group
		narrowedGroup.MemberCanonicalIDs = keptIDs
		// Total stays where it was: it is the group's size BEFORE narrowing,
		// which is exactly what makes the truncation disclosure informative
		// rather than circular.
		narrowedGroup.Truncated = len(keptIDs) < group.Total
		narrowedGroup.Complete = !narrowedGroup.Truncated && group.Complete
		groups = append(groups, narrowedGroup)
	}
	kept = make([]CohortMember, 0, len(survivors))
	for _, member := range cohort.Members {
		if _, survived := survivors[member.Subject.CanonicalID]; !survived {
			continue
		}
		kept = append(kept, member)
	}
	// Rank is a DENSE 1..N sequence the cohort validator enforces across the
	// member list, so removing members from the middle requires renumbering.
	// AttentionRank is left alone: RankCohort owns it, and it is recomputed
	// when the narrowed cohort is re-ranked.
	for index := range kept {
		kept[index].Rank = index + 1
	}
	return kept, groups, len(kept) != len(cohort.Members), basis
}

// NarrowFlatCohort is the ungrouped counterpart of NarrowGroupedCohort: it
// keeps the first maxMembers members in the cohort's own order.
//
// That order is canonical-id-lexical for a discovered cohort, because
// DiscoveredCohort fills members from candidate nodes sorted on SubjectKey
// (CHAOS-4630's total-key sort). The basis is DECLARED as arbitrary-but-
// stable rather than dressed up as relevance -- attention rank is not
// available to a narrowing that must not re-rank, and "cohort discovery
// order" was not an order at all until CHAOS-4630 made it one.
func NarrowFlatCohort(cohort *Cohort, maxMembers int) (kept []CohortMember, narrowed bool) {
	if cohort == nil || maxMembers <= 0 || len(cohort.Members) <= maxMembers {
		return nil, false
	}
	kept = append([]CohortMember(nil), cohort.Members[:maxMembers]...)
	for index := range kept {
		kept[index].Rank = index + 1
	}
	return kept, true
}

// RetainFactsForCohort drops facts whose subject is no longer in the cohort.
//
// This is not a budget optimization, it is a CORRECTNESS step: synthesis
// mints ClaimedFacts from what it is given, and a fact about a member that
// narrowing removed would produce a claim about a subject the answer does not
// contain -- an ungrounded claim, which the evidence-closure validator would
// then reject, turning a narrowed answer into a failed one.
//
// Facts whose subject is NOT a cohort member are kept untouched: a cohort
// answer also carries organization- and subject-level facts that no member
// owns, and dropping those would remove evidence narrowing never asked about.
func RetainFactsForCohort(facts []CanonicalFact, cohort *Cohort, removed []CohortMember) []CanonicalFact {
	if len(removed) == 0 || len(facts) == 0 {
		return facts
	}
	dropped := make(map[string]struct{}, len(removed))
	for _, member := range removed {
		dropped[SubjectMapKey(member.Subject)] = struct{}{}
	}
	retained := make([]CanonicalFact, 0, len(facts))
	for _, fact := range facts {
		if _, gone := dropped[SubjectMapKey(fact.Subject)]; gone {
			continue
		}
		retained = append(retained, fact)
	}
	return retained
}

// RemovedCohortMembers reports which members `before` had that `after` does
// not, so the caller can drop their facts and count the narrowing.
func RemovedCohortMembers(before, after []CohortMember) []CohortMember {
	if len(before) == len(after) {
		return nil
	}
	kept := make(map[string]struct{}, len(after))
	for _, member := range after {
		kept[SubjectMapKey(member.Subject)] = struct{}{}
	}
	removed := make([]CohortMember, 0, len(before)-len(after))
	for _, member := range before {
		if _, survived := kept[SubjectMapKey(member.Subject)]; survived {
			continue
		}
		removed = append(removed, member)
	}
	return removed
}

// groupingRefusalDisclosure names, per vocabulary member, the sentence the
// READER is told, and reports whether there is one.
//
// AN ALLOW-LIST, NOT A DENY-LIST, and not by preference. The vocabulary is
// CLOSED, and the ruling recorded as D10 one package over says a
// classification over a closed vocabulary names each member's class rather
// than testing for the members it excludes: a deny-list (`!= None`) admits the
// NEXT member by default, silently, with whatever sentence happened to be
// written for its neighbour -- or with none, which is the defect this whole
// change exists to remove. The `default` arm therefore fails closed: an
// unknown member discloses NOTHING rather than something wrong, and the
// telemetry emitter's own `unclassified` fallback is what surfaces it.
//
// The pre-D10 shape of this function was `!= GroupKindSourceMismatch`, which
// was an allow-list of one only because the vocabulary had one disclosing
// member. Growing the vocabulary is exactly the event that turns that
// accident into a defect.
func groupingRefusalDisclosure(outcome CohortGroupingOutcome) (string, bool) {
	switch outcome.Refusal {
	case CohortGroupingRefusalGroupKindSourceMismatch:
		return contractsv1.ContextFabricGroupingRefusalLimitation(outcome.PlannedKind, outcome.SourceKind), true
	case CohortGroupingRefusalNoMemberPlaced:
		// One kind, not two: the source named none. See the constant's own
		// comment for why a count is not interpolated here.
		return contractsv1.ContextFabricGroupingUnplaceableLimitation(outcome.PlannedKind), true
	case CohortGroupingRefusalNone:
		return "", false
	default:
		return "", false
	}
}

// applyGroupingRefusalDisclosure states on the WIRE that a grouped question
// was answered ungrouped, and why.
//
// Telemetry alone is not disclosure: an operator reading logs is not the
// person reading the answer, and a reader who asked for a per-team breakdown
// and silently received a flat list has been told something false by omission.
// The refusal already logs its reason; this is the half the caller sees.
//
// Both kinds are closed-vocabulary subject kinds, so the sentence carries no
// model text and no corpus content -- the same rule every other disclosure
// composer on this path follows.
//
// TWO THINGS THIS GOT WRONG, both found by codex round 3 and both the same
// mistake: treating the disclosure as if writing it were the whole job.
//
//  1. The string was composed HERE with a local Sprintf, so nothing could
//     recognise it as service-authored. appendBoundedLimitations never
//     displaces a service disclosure -- it displaces the last MODEL-authored
//     caveat -- and it decides which is which by asking the contract. An
//     unregistered disclosure is, to that rule, a model caveat. With a full
//     limitation list and an unaffirmed committed subject, the
//     commit-affirmation composer that runs later therefore displaced THIS
//     sentence to make room for its own, and the served flat answer stated
//     nothing at all about having been answered on a different axis than the
//     one asked for. That defeats the ruling this function exists to
//     implement: the degrade is never silent. The string now comes from
//     contractsv1.ContextFabricGroupingRefusalLimitation, which is also what
//     the contract's recogniser parses.
//
//  2. The displacement count was discarded into `_`. A displaced model
//     caveat is gone from the stored answer and cannot be inferred
//     downstream -- a displaced list and a list that had room are the same
//     length and end the same way -- so LimitationsDisplaced is the only
//     record it existed. Every other composer on this path accounts it; this
//     one silently under-reported by exactly the caveat it dropped, which
//     also puts the result's own count at odds with the validator's
//     coherence rule.
func applyGroupingRefusalDisclosure(result *InvestigationResult, outcome CohortGroupingOutcome) {
	if result == nil {
		return
	}
	sentence, discloses := groupingRefusalDisclosure(outcome)
	if !discloses {
		return
	}
	// Through the BOUNDED appender, not a raw append: the limitations
	// collection has a contract cap, and this package's own closure test
	// rejects any limitations-destined write that bypasses it. Caught here by
	// that test rather than by a reviewer, which is the guard working.
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	result.Coverage.Partial = true
}
