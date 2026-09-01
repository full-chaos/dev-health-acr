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
func BuildCohortGroups(plan AnswerPlan, cohort *Cohort, facts []CanonicalFact) (groups []contractsv1.ContextFabricCohortGroup, ungrouped int) {
	if plan.GroupKind == "" || cohort == nil || len(cohort.Members) == 0 {
		return nil, 0
	}
	assignments := groupAssignmentsByMember(facts)
	// Preserve the cohort's own member order inside each group, and order
	// the groups themselves by canonical id. Both are deterministic: a
	// grouped answer whose group order varied between two identical
	// requests would make every before/after comparison meaningless.
	order := make([]string, 0, len(cohort.Members))
	byGroup := make(map[string][]string, len(cohort.Members))
	labels := make(map[string]string, len(cohort.Members))
	for _, member := range cohort.Members {
		placed := assignments[SubjectMapKey(member.Subject)]
		if len(placed) == 0 {
			ungrouped++
			continue
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
			}
			byGroup[assignment.canonicalID] = append(byGroup[assignment.canonicalID], member.Subject.CanonicalID)
		}
	}
	if len(order) == 0 {
		return nil, ungrouped
	}
	sort.Strings(order)
	groups = make([]contractsv1.ContextFabricCohortGroup, 0, len(order))
	for _, canonicalID := range order {
		members := byGroup[canonicalID]
		label := labels[canonicalID]
		if label == "" {
			label = canonicalID
		}
		groups = append(groups, contractsv1.ContextFabricCohortGroup{
			Subject:            SubjectRef{Kind: plan.GroupKind, CanonicalID: canonicalID, Label: label},
			MemberCanonicalIDs: members,
			// Total is the group's membership AS DISCOVERED. It is not a
			// claim about how many projects the team owns in the world:
			// nothing here read that, and asserting it would be inventing
			// a number. A later narrowing lowers the listed members and
			// leaves Total where it was, which is what makes the
			// truncation disclosure true.
			Total: len(members),
			// Complete as built: every member this group could place is
			// listed. Narrowing is what flips this, and it flips it
			// explicitly.
			Complete:  true,
			Truncated: false,
		})
	}
	return groups, ungrouped
}

// groupAssignmentsByMember reads the owning group out of each fact's declared
// tables, keyed by the fact's own subject.
//
// It reads DECLARED tables (FactValue.Table) and falls back to the sibling
// Rows the same producers still populate, because CHAOS-4633's migration is
// deliberately dual-write: a producer emits both, and reading only one of
// them would make this depend on which phase of that migration is deployed.
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
		found = append(found, cohortGroupAssignment{canonicalID: canonicalID, label: label})
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

// ApplyGroupedCohortCompleteness rewrites the cohort-level booleans as the
// conjunction over the groups.
//
// This is what stops an old reader going group-blind. Complete over the flat
// union cannot say "team A complete, team B truncated"; defining it as "every
// group complete" means a reader that ignores Groups gets a CONSERVATIVE
// answer -- never Complete: true over a partially-truncated union -- rather
// than a boolean that happened to describe only whichever group came first.
func ApplyGroupedCohortCompleteness(cohort *Cohort) {
	if cohort == nil || len(cohort.Groups) == 0 {
		return
	}
	cohort.Complete, cohort.Truncated = contractsv1.CohortCompletenessFromGroups(cohort.Groups)
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
