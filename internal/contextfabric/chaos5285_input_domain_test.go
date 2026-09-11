package contextfabric

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE INPUT-DOMAIN TABLE.
//
// Every guard this change adds or modifies, crossed with every shape its
// fields can arrive in, executed in ONE pass. The table it prints is the
// evidence; the assertions are the contract.
//
// Three shapes from the standard list -- wrong container type, wrong scalar
// type, and fractional where integral -- are recorded as EXCLUDED BY THE TYPE
// SYSTEM rather than silently dropped. Every field below is a Go named type,
// a pointer, a slice or an int, so a caller cannot present a string where a
// slice belongs or a fraction where a count belongs: the compiler refuses
// before any guard runs. That is a real answer to the cell, and it is only
// true because these guards sit BEHIND the JSON boundary -- the wire-facing
// decode is a different surface with a different table.
const domainExcludedByTypeSystem = "n/a - excluded by the type system (named Go types behind the decode boundary)"

type domainRow struct {
	guard    string
	field    string
	shape    string
	observed string
	verdict  string
}

type domainTable struct {
	t    *testing.T
	rows []domainRow
}

func (d *domainTable) record(guard, field, shape, observed, verdict string) {
	d.rows = append(d.rows, domainRow{guard: guard, field: field, shape: shape, observed: observed, verdict: verdict})
}

// want asserts the cell's contract and records the outcome either way, so a
// failing cell still appears in the printed table rather than aborting it.
func (d *domainTable) want(guard, field, shape, observed, expected string) {
	verdict := "ok"
	if observed != expected {
		verdict = "MISMATCH want=" + expected
		d.t.Errorf("%s / %s / %s: observed %q, contract says %q", guard, field, shape, observed, expected)
	}
	d.record(guard, field, shape, observed, verdict)
}

func (d *domainTable) print() {
	sort.SliceStable(d.rows, func(i, j int) bool {
		if d.rows[i].guard != d.rows[j].guard {
			return d.rows[i].guard < d.rows[j].guard
		}
		return d.rows[i].field < d.rows[j].field
	})
	var b strings.Builder
	b.WriteString("\n| guard | field | shape | observed | verdict |\n|---|---|---|---|---|\n")
	for _, row := range d.rows {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n", row.guard, row.field, row.shape, row.observed, row.verdict))
	}
	d.t.Logf("INPUT-DOMAIN TABLE (%d cells)%s", len(d.rows), b.String())
}

func TestTheInputDomainOfEveryGuardThisChangeTouches(t *testing.T) {
	t.Parallel()
	table := &domainTable{t: t}

	domainI6(table)
	domainPlanSeam(table)
	domainAdmissionFilter(table)
	domainBound(table)
	domainMetadataConflict(table)
	domainRetention(table)
	domainCoverageFold(table)
	domainCombinedCap(table)
	domainInterpretationBoundary(table)
	domainTeamIdentity(table)
	domainGroupReadRequirements(table)
	domainAllowanceClamp(table)
	domainEmitterVocabularies(t, table)
	domainRequestDerivedLogInt(table)
	domainAuthorizationBatching(table)
	domainPlanSeamLine(t, table)
	domainSearchFallbackPolicy(table)
	domainGroupReadDisclosure(t, table)

	table.print()
	if len(table.rows) == 0 {
		t.Fatal("the table is empty, so this pass asserts nothing")
	}
}

// --- guard 1: the I6 frame gate -------------------------------------------

func domainI6(d *domainTable) {
	const guard = "checkI6 (frame gate)"
	outcome := func(expression SubjectExpression) string {
		failure, bad := checkI6(expression)
		if !bad {
			return "admitted"
		}
		return "refused:" + string(failure.Detail)
	}
	grouped := func(group, member SubjectKind) SubjectExpression {
		return SubjectExpression{Kind: SubjectExpressionGroupedMembers, Grouped: &GroupedSetExpression{GroupKind: group, MemberKind: member}}
	}

	d.want(guard, "SubjectExpression.Grouped", "absent (nil pointer on a grouped kind)",
		outcome(SubjectExpression{Kind: SubjectExpressionGroupedMembers}), "admitted")
	d.record(guard, "SubjectExpression.Grouped", "null", "same cell as absent in Go: a nil pointer IS the null", "ok")
	d.want(guard, "SubjectExpression.Kind", "out of vocabulary",
		outcome(SubjectExpression{Kind: SubjectExpressionKind("not_a_variant"), Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectTeam}}), "admitted")
	d.want(guard, "Grouped.GroupKind", "zero (empty string)",
		outcome(grouped("", SubjectTeam)), "refused:"+string(FrameFailureGroupKindUnset))
	d.want(guard, "Grouped.MemberKind", "zero (empty string)",
		outcome(grouped(SubjectTeam, "")), "refused:"+string(FrameFailureMemberKindUnset))
	d.want(guard, "Grouped.GroupKind", "out of vocabulary",
		outcome(grouped(SubjectKind("not_a_kind"), SubjectTeam)), "refused:"+string(FrameFailureGroupKindInvalid))
	d.want(guard, "Grouped.MemberKind", "out of vocabulary",
		outcome(grouped(SubjectTeam, SubjectKind("not_a_kind"))), "refused:"+string(FrameFailureMemberKindInvalid))
	d.want(guard, "Grouped.{Group,Member}Kind", "duplicate (equal kinds)",
		outcome(grouped(SubjectTeam, SubjectTeam)), "refused:"+string(FrameFailureGroupEqualsMember))
	d.want(guard, "Grouped.{Group,Member}Kind", "canonical (two distinct kinds)",
		outcome(grouped(SubjectTeam, SubjectProject)), "admitted")
	d.record(guard, "Grouped.{Group,Member}Kind", "boundary +/- 1",
		"n/a - the domain is a closed unordered vocabulary, not an interval; every ordered pair is swept by the 225-cell sweep", "ok")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional", domainExcludedByTypeSystem, "ok")
	d.record(guard, "Grouped.{Group,Member}Kind", "empty container",
		"n/a - both fields are scalars, not containers", "ok")
}

// --- guard 2: the plan seam ------------------------------------------------

func domainPlanSeam(d *domainTable) {
	const guard = "plan seam (planGroupAxisCollapsed)"
	// THE PRODUCTION PREDICATE, not a mirror of it. A mirror written beside
	// the guard agrees with whatever the author believed the guard did, and
	// this one did not: the seam compared the kinds bare, so ("", "") --
	// an axis-less plan over a kindless cohort -- was REFUSED as I6 while
	// the mirror said "served". The engine-level half of that cell is
	// TestAPlanWithNoGroupAxisIsNeverRefusedAsASelfGroup.
	collapses := func(group, member SubjectKind) string {
		if planGroupAxisCollapsed(group, member) {
			return "refused"
		}
		return "served"
	}
	d.want(guard, "plan.{Group,Member}Kind", "zero both (no group axis, kindless cohort)", collapses("", ""), "served")
	d.want(guard, "plan.GroupKind", "zero with a member kind present", collapses("", SubjectTeam), "served")
	d.want(guard, "plan.MemberKind", "zero (cohort kind unknown)", collapses(SubjectTeam, ""), "served")
	d.want(guard, "plan.{Group,Member}Kind", "duplicate (equal kinds)", collapses(SubjectTeam, SubjectTeam), "refused")
	d.want(guard, "plan.{Group,Member}Kind", "canonical (distinct kinds)", collapses(SubjectTeam, SubjectProject), "served")
	d.want(guard, "plan.{Group,Member}Kind", "out of vocabulary, equal",
		collapses(SubjectKind("not_a_kind"), SubjectKind("not_a_kind")), "refused")
	d.want(guard, "plan.{Group,Member}Kind", "out of vocabulary, distinct",
		collapses(SubjectKind("not_a_kind"), SubjectTeam), "served")
	d.want(guard, "plan.{Group,Member}Kind", "equal up to case (Team vs team)",
		collapses(SubjectKind("Team"), SubjectTeam), "served")
	d.record(guard, "plan.{Group,Member}Kind", "boundary +/- 1", "n/a - closed unordered vocabulary", "ok")
	d.record(guard, "all fields", "null / empty container / wrong container / wrong scalar / fractional", domainExcludedByTypeSystem+"; a Go string has no null distinct from zero", "ok")
}

// --- guard 3: the admission filter ----------------------------------------

func domainAdmissionFilter(d *domainTable) {
	const guard = "group admission filter"
	proposed := []contractsv1.ContextFabricCohortGroup{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_a"), Label: "A"}, MemberCanonicalIDs: []string{"m"}, Total: 1, Complete: true},
	}
	// THE PRODUCTION RULE, called directly -- the same function
	// authorizeCohortGroups returns through.
	admit := func(committed []SubjectRef) string {
		out := make([]string, 0, len(committed))
		for _, subject := range admitResolvedGroups(proposed, committed) {
			out = append(out, subject.CanonicalID)
		}
		if len(out) == 0 {
			return "admitted:none"
		}
		return "admitted:" + strings.Join(out, ",")
	}
	team := func(id string) SubjectRef {
		return SubjectRef{Kind: SubjectTeam, CanonicalID: id, Label: id}
	}

	d.want(guard, "resolution.Committed", "empty container", admit(nil), "admitted:none")
	d.want(guard, "Committed[].CanonicalID", "zero (empty string)", admit([]SubjectRef{team("")}), "admitted:none")
	d.want(guard, "Committed[].CanonicalID", "out of vocabulary (never proposed)", admit([]SubjectRef{team("team:not_proposed")}), "admitted:none")
	d.want(guard, "Committed[].CanonicalID", "canonical (proposed)", admit([]SubjectRef{team(TeamCanonicalID("team_a"))}), "admitted:"+TeamCanonicalID("team_a"))
	d.want(guard, "Committed[].CanonicalID", "duplicate", admit([]SubjectRef{team(TeamCanonicalID("team_a")), team(TeamCanonicalID("team_a"))}), "admitted:"+TeamCanonicalID("team_a"))
	d.want(guard, "Committed[].Kind", "wrong kind, right id",
		admit([]SubjectRef{{Kind: SubjectProject, CanonicalID: TeamCanonicalID("team_a"), Label: "A"}}), "admitted:none")
	d.want(guard, "Committed[].Kind", "zero (empty kind)",
		admit([]SubjectRef{{Kind: "", CanonicalID: TeamCanonicalID("team_a"), Label: "A"}}), "admitted:none")
	d.want(guard, "resolution.Committed", "null (nil slice)", admit(nil), "admitted:none")
	d.want(guard, "Committed[].CanonicalID", "raw key of a proposed group (namespace missing)", admit([]SubjectRef{team("team_a")}), "admitted:none")
	d.want(guard, "Committed[].CanonicalID", "case variant of a proposed identity", admit([]SubjectRef{team("TEAM:team_a")}), "admitted:none")
	d.want(guard, "proposed groups", "empty container", func() string {
		if got := admitResolvedGroups(nil, []SubjectRef{team(TeamCanonicalID("team_a"))}); len(got) != 0 {
			return fmt.Sprintf("admitted:%d", len(got))
		}
		return "admitted:none"
	}(), "admitted:none")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional / boundary", domainExcludedByTypeSystem, "ok")
}

// --- guard 4: the contract bound ------------------------------------------

func domainBound(d *domainTable) {
	const guard = "group contract bound"
	bound := contractsv1.ContextFabricCohortGroupsMaxCount
	// THE PRODUCTION PREDICATE readAdmittedGroupFacts decides with.
	over := func(count int) string {
		if groupListOverContractBound(count) {
			return "refused"
		}
		return "served"
	}
	for _, cell := range []struct {
		shape string
		count int
		want  string
	}{
		{"zero", 0, "served"},
		{"one", 1, "served"},
		{"boundary - 1", bound - 1, "served"},
		{"boundary", bound, "served"},
		{"boundary + 1", bound + 1, "refused"},
	} {
		d.want(guard, "len(cohort.Groups)", cell.shape, over(cell.count), cell.want)
	}
	d.record(guard, "len(cohort.Groups)", "negative", "n/a - len() is non-negative by construction", "ok")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional / out of vocabulary", domainExcludedByTypeSystem, "ok")
}

// --- guard 5: the metadata-conflict check ---------------------------------

func domainMetadataConflict(d *domainTable) {
	const guard = "metadata conflict (mergeGroupBundle)"
	run := func(into, group CanonicalFactBundle) string {
		if mergeGroupBundle(&into, group, "org_1") {
			return "conflicted"
		}
		return fmt.Sprintf("merged:versions=%d,watermarks=%d", len(into.Versions), len(into.Watermarks))
	}
	base := func(versions, watermarks map[FactKind]string) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Versions, bundle.Watermarks = versions, watermarks
		return bundle
	}

	d.want(guard, "group.Versions", "empty container",
		run(base(map[FactKind]string{FactHealth: "v1"}, nil), base(map[FactKind]string{}, nil)), "merged:versions=1,watermarks=0")
	d.want(guard, "group.Versions", "null (nil map)",
		run(base(map[FactKind]string{FactHealth: "v1"}, nil), base(nil, nil)), "merged:versions=1,watermarks=0")
	d.want(guard, "into.Versions", "null (nil map on the receiver)",
		run(base(nil, nil), base(map[FactKind]string{FactHealth: "v1"}, nil)), "merged:versions=1,watermarks=0")
	d.want(guard, "group.Versions[kind]", "duplicate kind, equal value",
		run(base(map[FactKind]string{FactHealth: "v1"}, nil), base(map[FactKind]string{FactHealth: "v1"}, nil)), "merged:versions=1,watermarks=0")
	d.want(guard, "group.Versions[kind]", "duplicate kind, DIFFERENT value",
		run(base(map[FactKind]string{FactHealth: "v1"}, nil), base(map[FactKind]string{FactHealth: "v2"}, nil)), "conflicted")
	d.want(guard, "group.Versions[kind]", "zero (empty string) against a set value",
		run(base(map[FactKind]string{FactHealth: "v1"}, nil), base(map[FactKind]string{FactHealth: ""}, nil)), "conflicted")
	d.want(guard, "group.Watermarks[kind]", "duplicate kind, DIFFERENT value",
		run(base(nil, map[FactKind]string{FactHealth: "w1"}), base(nil, map[FactKind]string{FactHealth: "w2"})), "conflicted")
	d.want(guard, "group.Versions[kind]", "canonical (a kind the turn does not have)",
		run(base(map[FactKind]string{FactMetrics: "m1"}, nil), base(map[FactKind]string{FactHealth: "v1"}, nil)), "merged:versions=2,watermarks=0")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional / boundary", domainExcludedByTypeSystem, "ok")
}

// --- guard 6: the retention rule ------------------------------------------

func domainRetention(d *domainTable) {
	const guard = "retention (RetainFactsForCohort)"
	member := SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "a"}
	gone := []CohortMember{{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "b"}, Rank: 2, InclusionReasons: []string{"m"}}}
	facts := []CanonicalFact{
		{Kind: FactMetrics, Subject: member, Fields: map[string]FactValue{}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"},
		groupFact("team_a"),
	}
	grouped := &Cohort{Kind: SubjectProject, Rationale: "r", Complete: true,
		Members: []CohortMember{{Subject: member, Rank: 1, InclusionReasons: []string{"m"}}},
		Groups: []contractsv1.ContextFabricCohortGroup{
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_a"), Label: "A"}, MemberCanonicalIDs: []string{"project_a"}, Total: 1, Complete: true},
		}}
	flat := &Cohort{Kind: SubjectProject, Rationale: "r", Complete: true,
		Members: []CohortMember{{Subject: member, Rank: 1, InclusionReasons: []string{"m"}}}}

	run := func(cohort *Cohort, removed []CohortMember, in []CanonicalFact) string {
		out, decision := RetainFactsForCohortWithDecision(in, cohort, removed)
		return fmt.Sprintf("kept=%d dropped_members=%d dropped_groups=%d", len(out), decision.DroppedMembers, decision.DroppedGroups)
	}

	d.want(guard, "removed", "empty container", run(grouped, nil, facts), "kept=2 dropped_members=0 dropped_groups=0")
	d.want(guard, "facts", "empty container", run(grouped, gone, nil), "kept=0 dropped_members=0 dropped_groups=0")
	rule := func(cohort *Cohort) string {
		_, decision := RetainFactsForCohortWithDecision(facts, cohort, gone)
		return fmt.Sprintf("group_rule_applied=%v", decision.GroupRuleApplied)
	}
	d.want(guard, "cohort", "null (nil pointer)", run(nil, gone, facts), "kept=2 dropped_members=0 dropped_groups=0")
	// The nil and empty-group shapes keep the pre-group behaviour because the
	// rule cannot run without a group list -- and they SAY SO, so a third
	// caller cannot restore the row-14 defect silently.
	d.want(guard, "cohort", "null (nil pointer) - does the group rule run?", rule(nil), "group_rule_applied=false")
	d.want(guard, "cohort.Groups", "empty container - does the group rule run?", rule(flat), "group_rule_applied=false")
	d.want(guard, "cohort.Groups", "canonical - does the group rule run?", rule(grouped), "group_rule_applied=true")
	d.want(guard, "cohort.Groups", "empty container (flat cohort)", run(flat, gone, facts), "kept=2 dropped_members=0 dropped_groups=0")
	d.want(guard, "cohort.Groups", "canonical (group still present)", run(grouped, gone, facts), "kept=2 dropped_members=0 dropped_groups=0")
	d.want(guard, "cohort.Groups", "group absent from the answer",
		run(&Cohort{Kind: SubjectProject, Rationale: "r", Complete: true,
			Members: grouped.Members,
			Groups: []contractsv1.ContextFabricCohortGroup{
				{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_other"), Label: "O"}, MemberCanonicalIDs: []string{"project_a"}, Total: 1, Complete: true},
			}}, gone, facts),
		"kept=1 dropped_members=0 dropped_groups=1")
	d.want(guard, "removed[].Subject", "duplicate removals",
		run(grouped, append(append([]CohortMember{}, gone...), gone...), facts), "kept=2 dropped_members=0 dropped_groups=0")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional / boundary / out of vocabulary", domainExcludedByTypeSystem, "ok")
}

// --- guard 7: MergeCoverage's fold ----------------------------------------

func domainCoverageFold(d *domainTable) {
	const guard = "MergeCoverage fold"
	fold := func(groups ...Coverage) string {
		merged := MergeCoverage("org_1", groups...)
		parts := make([]string, 0, len(merged.Sources))
		for _, source := range merged.Sources {
			parts = append(parts, source.Source+"="+string(source.State))
		}
		sort.Strings(parts)
		return strings.Join(parts, ",")
	}
	source := func(name string, state SourceState) Coverage {
		return Coverage{Sources: []SourceObservation{{Source: name, State: state}}, DegradedReasons: []string{}}
	}

	d.want(guard, "coverage groups", "empty container (no arguments)", fold(), "")
	d.want(guard, "Coverage.Sources", "null (nil slice)", fold(Coverage{}), "")
	d.want(guard, "Coverage.Sources", "empty container", fold(Coverage{Sources: []SourceObservation{}}), "")
	d.want(guard, "SourceObservation.Source", "canonical, one group", fold(source("canonical_fact:health", SourceAvailable)), "canonical_fact:health=available")
	d.want(guard, "SourceObservation.Source", "duplicate name, worse state second",
		fold(source("canonical_fact:health", SourceAvailable), source("canonical_fact:health", SourceNoData)), "canonical_fact:health=no_data")
	d.want(guard, "SourceObservation.Source", "duplicate name, worse state FIRST",
		fold(source("canonical_fact:health", SourceNoData), source("canonical_fact:health", SourceAvailable)), "canonical_fact:health=no_data")
	d.want(guard, "SourceObservation.State", "out of vocabulary",
		fold(source("canonical_fact:health", SourceState("not_a_state"))), "canonical_fact:health=not_a_state")
	d.record(guard, "SourceObservation.State", "out-of-vocabulary NOTE",
		"the fold PRESERVES an unknown state rather than refusing it; the pre-fold disclosure routes its own copy through contractsv1.ValidContextFabricSourceState and publishes `unclassified`", "ok")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional / boundary", domainExcludedByTypeSystem, "ok")
}

// TestAnOutOfVocabularyExpressionKindIsRefusedSomewhere closes the one cell in
// the table above that checkI6 ADMITS.
//
// checkI6 returns false at its first line for any expression that is not
// `grouped_members`, so a frame whose expression kind is garbage while its
// grouped payload is a self-group passes I6 untouched. That is correct
// division of labour only if some OTHER invariant refuses it -- and a table
// that recorded "admitted" and stopped would have documented a hole as a
// feature.
//
// This asserts the whole validator refuses it, and names which invariant did,
// so the division of labour is written down rather than assumed.
func TestAnOutOfVocabularyExpressionKindIsRefusedSomewhere(t *testing.T) {
	t.Parallel()

	frame := QuestionFrame{
		Version: QuestionFrameVersion,
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionKind("not_a_variant"),
			Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectTeam},
		},
		Obligations: []AnswerObligation{ObligationState},
		Goals:       []InvestigationGoal{GoalAssessState},
		Temporal:    TemporalIntentCurrent,
	}
	result := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
	t.Logf("out-of-vocabulary expression kind -> outcome=%q invariant=%q detail=%q",
		result.Outcome, result.Failure.Invariant, result.Failure.Detail)
	if result.Outcome == FrameValidationOutcomeValid {
		t.Fatalf("a frame whose subject-expression kind is outside the closed vocabulary VALIDATED -- checkI6 admits it by design, so if nothing else refuses it the illegal grouped payload it carries reaches the plan unexamined")
	}
	if result.Failure.Invariant == FrameInvariantI6 {
		t.Errorf("the refusal is attributed to i6, but checkI6 returns false for a non-grouped expression kind -- the attribution and the code disagree")
	}
	if gate := DecideFrameGate(result, true); !gate.Refuses() {
		t.Errorf("the gate outcome %q does not refuse an out-of-vocabulary expression kind", gate.Outcome)
	}
}

// --- guard 8: the combined per-bundle cap ---------------------------------

func domainCombinedCap(d *domainTable) {
	const guard = "combined cap (boundGroupFactsToRemainingCapacity)"
	limit := maxCanonicalFactsPerBundle
	three := func() []CanonicalFact {
		return []CanonicalFact{groupKindFact("team_b", FactWorkload), groupKindFact("team_a", FactHealth), groupKindFact("team_c", FactHealth)}
	}
	// run calls the PRODUCTION function and reports what it kept, what it
	// omitted, and which kinds it disclosed as truncated.
	run := func(turn int, facts []CanonicalFact) string {
		group := emptyFactBundle()
		group.Facts = facts
		omitted := boundGroupFactsToRemainingCapacity(&group, turn)
		kept := make([]string, 0, len(group.Facts))
		for _, fact := range group.Facts {
			kept = append(kept, string(fact.Kind)+"@"+fact.Subject.CanonicalID)
		}
		truncated := make([]string, 0, 2)
		for _, source := range group.Coverage.Sources {
			if source.State == SourceTruncated {
				truncated = append(truncated, source.Source)
			}
		}
		return fmt.Sprintf("omitted=%d kept=[%s] truncated=[%s]", omitted, strings.Join(kept, " "), strings.Join(truncated, " "))
	}
	keptAll := "omitted=0 kept=[workload@team:team_b health@team:team_a health@team:team_c] truncated=[]"

	d.want(guard, "turnFacts", "zero", run(0, three()), keptAll)
	d.want(guard, "turnFacts", "boundary - 1 (one slot left)", run(limit-1, three()),
		"omitted=2 kept=[health@team:team_a] truncated=[canonical_fact:health canonical_fact:workload]")
	d.want(guard, "turnFacts", "boundary (member read at the cap)", run(limit, three()),
		"omitted=3 kept=[] truncated=[canonical_fact:health canonical_fact:workload]")
	d.want(guard, "turnFacts", "boundary + 1 (member read over the cap)", run(limit+1, three()),
		"omitted=3 kept=[] truncated=[canonical_fact:health canonical_fact:workload]")
	d.want(guard, "turnFacts", "negative (unreachable: the caller passes len())", run(-1, three()), keptAll)
	d.want(guard, "group.Facts", "null (nil slice) at the cap", run(limit, nil), "omitted=0 kept=[] truncated=[]")
	d.want(guard, "group.Facts", "empty container at the cap", run(limit, []CanonicalFact{}), "omitted=0 kept=[] truncated=[]")
	d.want(guard, "len(group.Facts)", "exactly the remaining capacity", run(limit-3, three()), keptAll)
	d.want(guard, "len(group.Facts)", "remaining capacity + 1", run(limit-2, three()),
		"omitted=1 kept=[health@team:team_a health@team:team_c] truncated=[canonical_fact:workload]")
	d.want(guard, "group.Facts[]", "duplicate facts across the boundary", run(limit-1, []CanonicalFact{
		groupKindFact("team_a", FactHealth), groupKindFact("team_a", FactHealth), groupKindFact("team_a", FactHealth),
	}), "omitted=2 kept=[health@team:team_a] truncated=[canonical_fact:health]")
	d.want(guard, "group.Facts[].Kind", "out of vocabulary (sorts after every known kind, so it yields first)",
		run(limit-1, []CanonicalFact{
			{Kind: FactKind("not_a_kind"), Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_a"), Label: "a"}, Fields: map[string]FactValue{}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"},
			groupKindFact("team_b", FactHealth),
		}), "omitted=1 kept=[health@team:team_b] truncated=[canonical_fact:not_a_kind]")
	reversed := three()
	reversed[0], reversed[2] = reversed[2], reversed[0]
	d.want(guard, "group.Facts order", "canonical (provider order reversed)", run(limit-2, reversed),
		"omitted=1 kept=[health@team:team_a health@team:team_c] truncated=[canonical_fact:workload]")
	// The disclosure must survive the merge with its structured detail: a
	// degraded reason without its paired detail makes MergeCoverage drop
	// every detail of the turn (fail-open), which would be a disclosure
	// that erased others.
	paired := func() string {
		group := emptyFactBundle()
		group.Facts = three()
		boundGroupFactsToRemainingCapacity(&group, limit-1)
		merged := MergeCoverage("org_1", Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, group.Coverage)
		return fmt.Sprintf("degraded_reasons=%d details=%d", len(merged.DegradedReasons), len(merged.Details))
	}
	d.want(guard, "group.Coverage", "disclosure reconciles through MergeCoverage", paired(), "degraded_reasons=2 details=2")
	d.want(guard, "group.Watermarks", "null (nil map)", func() string {
		group := CanonicalFactBundle{Facts: three()}
		return fmt.Sprintf("omitted=%d", boundGroupFactsToRemainingCapacity(&group, limit-1))
	}(), "omitted=2")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional", domainExcludedByTypeSystem, "ok")
}

// --- guard 9: the interpretation boundary ---------------------------------

func domainInterpretationBoundary(d *domainTable) {
	const guard = "interpretation boundary (InterpretationBoundaryFrom)"
	// THE PRODUCTION CHAIN: the frame goes through validateProposedFrame (the
	// frame's invariants, then the requested-axis check) and DecideFrameGate
	// exactly as resolveFrame sends it, and the boundary is read off that
	// gate -- never off a gate literal this table chose.
	run := func(receipt ModelExecutionReceipt, expression SubjectExpression) string {
		frame := boundaryFrame(expression)
		gate := DecideFrameGate(validateProposedFrame(receipt, frame, ShapeDiscoveredCohort), true)
		b := InterpretationBoundaryFrom(receipt, frame, gate)
		return fmt.Sprintf("hint=%s member_hint=%s group=%s member=%s axis=%s",
			b.RequestedGroupHint, b.RequestedMemberHint, b.ProposedGroupKind, b.ProposedMemberKind, observableGroupAxis(b.GroupAxis))
	}
	hint := func(group SubjectKind, unrecognized bool) ModelExecutionReceipt {
		return ModelExecutionReceipt{GroupKind: group, GroupKindUnrecognized: unrecognized}
	}
	team, project := contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject
	flat := discoveredExpression(project)

	d.want(guard, "receipt.GroupKind", "absent", run(hint("", false), flat),
		"hint=absent member_hint=absent group=not_applicable member=project axis=not_requested")
	d.want(guard, "receipt.GroupKind", "zero with the unrecognized flag", run(hint("", true), flat),
		"hint=unrecognized member_hint=absent group=not_applicable member=project axis=refused")
	d.want(guard, "receipt.GroupKind", "out of vocabulary (never written verbatim)", run(hint("not_a_kind", false), flat),
		"hint=unclassified member_hint=absent group=not_applicable member=project axis=refused")
	d.want(guard, "receipt.GroupKind", "case variant (Team)", run(hint("Team", false), flat),
		"hint=unclassified member_hint=absent group=not_applicable member=project axis=refused")
	d.want(guard, "receipt.GroupKind", "canonical, frame dropped the grouping (refused under i6, round 2 P1-1)", run(hint(team, false), discoveredExpression(team)),
		"hint=team member_hint=absent group=not_applicable member=team axis=refused")
	gateOf := func(receipt ModelExecutionReceipt, expression SubjectExpression) string {
		result := validateProposedFrame(receipt, boundaryFrame(expression), ShapeDiscoveredCohort)
		return fmt.Sprintf("gate=%s detail=%s", DecideFrameGate(result, true).Observable(), result.Failure.Detail)
	}
	d.want(guard, "receipt.GroupKind x frame", "hint set, frame flat: the requested axis is refused with its own detail", gateOf(hint(team, false), discoveredExpression(team)),
		"gate=rejected:i6 detail=requested_group_axis_not_expressed")
	d.want(guard, "receipt.GroupKind x frame", "hint unrecognized, frame flat", gateOf(hint("", true), flat),
		"gate=rejected:i6 detail=requested_group_axis_not_expressed")
	d.want(guard, "receipt.GroupKind x frame", "hint set, frame grouped legally", gateOf(hint(team, false), groupedExpression(project, team)),
		"gate=passed detail=")
	d.want(guard, "receipt.GroupKind x frame", "hint absent, frame flat (the ordinary question)", gateOf(hint("", false), flat),
		"gate=passed detail=")
	d.want(guard, "receipt.GroupKind x frame", "hint set, frame invalid on its own (its own invariant wins)", gateOf(hint(team, false), SubjectExpression{}),
		"gate=rejected:i1 detail=kind_unset")
	d.want(guard, "receipt.RequestedSubjectKind", "canonical", run(ModelExecutionReceipt{RequestedSubjectKind: project}, flat),
		"hint=absent member_hint=project group=not_applicable member=project axis=not_requested")
	d.want(guard, "receipt.RequestedSubjectKind", "zero with the unrecognized flag", run(ModelExecutionReceipt{RequestedSubjectKindUnrecognized: true}, flat),
		"hint=absent member_hint=unrecognized group=not_applicable member=project axis=not_requested")
	d.want(guard, "SubjectExpression.Kind", "zero (no variant)", run(hint(team, false), SubjectExpression{}),
		"hint=team member_hint=absent group=not_applicable member=not_applicable axis=refused")
	d.want(guard, "SubjectExpression.Kind", "out of vocabulary", run(hint("", false), SubjectExpression{Kind: SubjectExpressionKind("not_a_variant")}),
		"hint=absent member_hint=absent group=not_applicable member=not_applicable axis=not_requested")
	d.want(guard, "SubjectExpression.Grouped", "null on a grouped kind", run(hint(team, false), SubjectExpression{Kind: SubjectExpressionGroupedMembers}),
		"hint=team member_hint=absent group=unset member=unset axis=refused")
	d.want(guard, "Grouped.MemberKind", "zero", run(hint(team, false), groupedExpression("", team)),
		"hint=team member_hint=absent group=team member=unset axis=refused")
	d.want(guard, "Grouped.GroupKind", "zero with the frame's unrecognized flag",
		run(ModelExecutionReceipt{GroupKind: team, FrameGroupKindUnrecognized: true}, groupedExpression(project, "")),
		"hint=team member_hint=absent group=unrecognized member=project axis=refused")
	d.want(guard, "Grouped.{Group,Member}Kind", "duplicate (a kind grouped by itself)", run(hint(team, false), groupedExpression(team, team)),
		"hint=team member_hint=absent group=team member=team axis=refused")
	d.want(guard, "Grouped.{Group,Member}Kind", "canonical (projects by team)", run(hint(team, false), groupedExpression(project, team)),
		"hint=team member_hint=absent group=team member=project axis=kept")
	d.want(guard, "Grouped.{Group,Member}Kind", "canonical, no hint", run(hint("", false), groupedExpression(project, team)),
		"hint=absent member_hint=absent group=team member=project axis=kept")
	d.want(guard, "SubjectExpression variant", "explicit_set (no member slot)", run(hint("", false), SubjectExpression{Kind: SubjectExpressionExplicitSet}),
		"hint=absent member_hint=absent group=not_applicable member=not_applicable axis=not_requested")
	d.want(guard, "SubjectExpression variant", "organization_scope with a null member kind", run(hint("", false), orgExpression(nil)),
		"hint=absent member_hint=absent group=not_applicable member=unset axis=not_requested")
	d.want(guard, "SubjectExpression variant", "named_subject with a declared kind", run(hint("", false), namedExpression(project)),
		"hint=absent member_hint=absent group=not_applicable member=project axis=not_requested")
	d.want(guard, "group_axis (emitted)", "zero (an event built without a boundary)", observableGroupAxis(""), "unset")
	d.want(guard, "group_axis (emitted)", "out of vocabulary", observableGroupAxis(GroupAxisDecision("not_a_decision")), "unclassified")
	d.record(guard, "all kind fields", "boundary +/- 1", "n/a - closed unordered vocabulary", "ok")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional", domainExcludedByTypeSystem+"; the receipt and frame are already decoded by sanitizeFrameOutput", "ok")
}

// --- the decode path ------------------------------------------------------

// --- guard 10: the canonical team identity (stage 1) ----------------------

func domainTeamIdentity(d *domainTable) {
	const mint = "team identity (TeamCanonicalID)"
	const parse = "team identity (TeamRawKey)"
	raw := func(canonical string) string {
		key, ok := TeamRawKey(canonical)
		return fmt.Sprintf("key=%q ok=%v", key, ok)
	}
	d.want(mint, "rawKey", "zero (empty key mints no identity)", TeamCanonicalID(""), "")
	d.want(mint, "rawKey", "canonical raw key", TeamCanonicalID("AUTH"), "team:AUTH")
	// A raw key that already carries the prefix is a DIFFERENT row of
	// teams.id from the bare key, so it is prefixed like any other: the mint
	// is injective, not idempotent (r1 P1-2).
	d.want(mint, "rawKey", "raw key spelled like a canonical id (prefixed, not passed through)", TeamCanonicalID("team:AUTH"), "team:team:AUTH")
	d.want(mint, "rawKey", "duplicate prefix already present", TeamCanonicalID("team:team:x"), "team:team:team:x")
	d.want(mint, "rawKey", "whitespace only", TeamCanonicalID(" "), "team: ")
	d.want(mint, "rawKey", "unicode", TeamCanonicalID("équipe-ß"), "team:équipe-ß")
	d.want(mint, "rawKey", "injective over the bare / prefixed / double-prefixed trio", func() string {
		seen := map[string]string{}
		for _, key := range []string{"x", "team:x", "team:team:x"} {
			id := TeamCanonicalID(key)
			if other, taken := seen[id]; taken {
				return fmt.Sprintf("%q and %q both mint %q", other, key, id)
			}
			seen[id] = key
		}
		return "ok"
	}(), "ok")
	d.want(mint, "rawKey", "prefix only (a raw key spelled \"team:\")", TeamCanonicalID("team:"), "team:team:")
	d.want(mint, "rawKey", "provider-qualified key (stored gl:full.chaos)", TeamCanonicalID("gl:full.chaos"), "team:gl:full.chaos")
	d.want(mint, "rawKey", "case variant of the prefix (identifiers are case-sensitive)", TeamCanonicalID("TEAM:AUTH"), "team:TEAM:AUTH")
	d.want(mint, "rawKey", "surrounding whitespace (kept verbatim; the reader queries the same bytes)", TeamCanonicalID(" AUTH"), "team: AUTH")
	d.want(parse, "canonicalID", "zero", raw(""), `key="" ok=false`)
	d.want(parse, "canonicalID", "raw key without the prefix", raw("AUTH"), `key="" ok=false`)
	d.want(parse, "canonicalID", "prefix only", raw("team:"), `key="" ok=false`)
	d.want(parse, "canonicalID", "canonical", raw("team:AUTH"), `key="AUTH" ok=true`)
	d.want(parse, "canonicalID", "case variant of the prefix", raw("TEAM:AUTH"), `key="" ok=false`)
	d.want(parse, "canonicalID", "duplicate prefix", raw("team:team:x"), `key="team:x" ok=true`)
	d.want(parse, "round trip", "every minted identity parses back to its key", func() string {
		for _, key := range []string{"AUTH", "gl:full.chaos", "gh:ops-team", " AUTH", "team:", "team:AUTH", "team:team:x", " ", "équipe-ß"} {
			if back, ok := TeamRawKey(TeamCanonicalID(key)); !ok || back != key {
				return fmt.Sprintf("broken at %q -> %q ok=%v", key, back, ok)
			}
		}
		return "ok"
	}(), "ok")
	d.record(mint, "all fields", "null / container / wrong scalar / fractional / boundary", domainExcludedByTypeSystem+"; a Go string has no null distinct from zero", "ok")
}

// --- guard 11: the group read's requirement selection ---------------------

func domainGroupReadRequirements(d *domainTable) {
	const guard = "group read requirements (groupReadRequirements)"
	row := func(scope CompletionScope, kind AnswerObligationKind, kinds ...string) contractsv1.ContextFabricPlanRequirement {
		factKinds := make([]contractsv1.ContextFabricFactKind, 0, len(kinds))
		for _, k := range kinds {
			factKinds = append(factKinds, contractsv1.ContextFabricFactKind(k))
		}
		return contractsv1.ContextFabricPlanRequirement{Scope: string(scope), Kind: string(kind), FactKinds: factKinds}
	}
	run := func(rows ...contractsv1.ContextFabricPlanRequirement) string {
		out := make([]string, 0, 6)
		for _, requirement := range groupReadRequirements(AnswerPlan{Requirements: rows}) {
			out = append(out, string(requirement.Kind))
		}
		return "[" + strings.Join(out, " ") + "]"
	}
	d.want(guard, "plan.Requirements", "null (nil slice)", run(), "[]")
	d.want(guard, "plan.Requirements", "only a member row", run(row(CompletionScopeEachMember, ObligationKindRead, "metrics")), "[]")
	d.want(guard, "row.Kind", "each_group but computed", run(row(CompletionScopeEachGroup, ObligationKindComputed, "health")), "[]")
	d.want(guard, "row.Scope", "out of vocabulary", run(row(CompletionScope("not_a_scope"), ObligationKindRead, "health")), "[]")
	d.want(guard, "row.FactKinds", "empty container", run(row(CompletionScopeEachGroup, ObligationKindRead)), "[]")
	d.want(guard, "row.FactKinds[]", "zero (empty kind skipped)", run(row(CompletionScopeEachGroup, ObligationKindRead, "", "health")), "[health]")
	d.want(guard, "row.FactKinds[]", "duplicate within and across rows",
		run(row(CompletionScopeEachGroup, ObligationKindRead, "health", "health"), row(CompletionScopeEachGroup, ObligationKindRead, "workload", "health")),
		"[health workload]")
	d.want(guard, "row.FactKinds[]", "out of vocabulary (passed through; the registry builds no query for an unregistered kind and reports it unconfigured)",
		run(row(CompletionScopeEachGroup, ObligationKindRead, "not_a_kind")), "[not_a_kind]")
	d.want(guard, "plan.Requirements", "canonical (qa-grouped-clean's rows)",
		run(row(CompletionScopeEachGroup, ObligationKindRead, "flow", "health", "investment", "landscape", "readiness", "workload"),
			row(CompletionScopeEachMember, ObligationKindRead, "metrics")),
		"[flow health investment landscape readiness workload]")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional / boundary", domainExcludedByTypeSystem, "ok")
}

// --- guard 12: the member-allowance clamp predicate -----------------------

func domainAllowanceClamp(d *domainTable) {
	const guard = "allowance clamp (cohortMemberAllowanceClamped)"
	clamped := func(maxItems, headroom int) string {
		return fmt.Sprintf("clamped=%v", cohortMemberAllowanceClamped(contractsv1.ContextFabricAnswerPlanBudget{MaxItems: maxItems, SynthesisHeadroom: headroom}))
	}
	d.want(guard, "MaxItems", "zero (no budget: nothing was clamped)", clamped(0, 20), "clamped=false")
	d.want(guard, "MaxItems", "negative", clamped(-1, 20), "clamped=false")
	d.want(guard, "MaxItems", "one", clamped(1, 20), "clamped=true")
	d.want(guard, "MaxItems - headroom", "boundary - 1 (MaxItems = headroom - 1)", clamped(19, 20), "clamped=true")
	d.want(guard, "MaxItems - headroom", "boundary (MaxItems = headroom: subtraction is 0)", clamped(20, 20), "clamped=true")
	d.want(guard, "MaxItems - headroom", "boundary + 1 (subtraction is exactly 1: computed, not clamped)", clamped(21, 20), "clamped=false")
	d.want(guard, "SynthesisHeadroom", "zero", clamped(1, 0), "clamped=false")
	d.want(guard, "SynthesisHeadroom", "negative", clamped(1, -5), "clamped=false")
	d.want(guard, "budget", "canonical (the rig's 30 items, headroom 20)", clamped(30, 20), "clamped=false")
	d.record(guard, "all fields", "null / container / wrong scalar / fractional", domainExcludedByTypeSystem, "ok")
}

// --- guard 13: the emitters' closed-vocabulary checks ---------------------

// domainEmitterVocabularies drives each new emitter through the REAL slog JSON
// handler with a value outside its vocabulary and reads the field back: the
// line must carry a named unknown, never the free text it was handed.
func domainEmitterVocabularies(t *testing.T, d *domainTable) {
	const guard = "emitter vocabularies (Record* membership checks)"
	principal := storage.Principal{OrgID: "org_1"}
	field := func(emit func(SlogEngineTelemetry), key string) string {
		records := captureSlogJSON(t, func(logger *slog.Logger) { emit(NewSlogEngineTelemetry(logger)) })
		if len(records) != 1 {
			return fmt.Sprintf("records=%d", len(records))
		}
		return fmt.Sprintf("%v", records[0][key])
	}
	ctx := context.Background()
	d.want(guard, "CohortGroupReadEvent.Refusal", "out of vocabulary", field(func(tel SlogEngineTelemetry) {
		tel.RecordCohortGroupRead(ctx, principal, CohortGroupReadEvent{Refusal: GroupReadRefusal("free text")})
	}, "group_read_refusal"), "unclassified")
	d.want(guard, "CohortGroupReadEvent.Refusal", "zero (no refusal)", field(func(tel SlogEngineTelemetry) {
		tel.RecordCohortGroupRead(ctx, principal, CohortGroupReadEvent{})
	}, "group_read_refusal"), "")
	d.want(guard, "CohortGroupReadEvent.Refusal", "canonical", field(func(tel SlogEngineTelemetry) {
		tel.RecordCohortGroupRead(ctx, principal, CohortGroupReadEvent{Refusal: GroupReadRefusalReadFailed})
	}, "group_read_refusal"), "read_failed")
	d.want(guard, "GroupReadCoverageStateEvent.Read", "out of vocabulary", field(func(tel SlogEngineTelemetry) {
		tel.RecordGroupReadCoverageState(ctx, principal, GroupReadCoverageStateEvent{Read: GroupReadArm("free text"), State: SourceAvailable})
	}, "read"), "unclassified")
	d.want(guard, "GroupReadCoverageStateEvent.Read", "zero", field(func(tel SlogEngineTelemetry) {
		tel.RecordGroupReadCoverageState(ctx, principal, GroupReadCoverageStateEvent{State: SourceAvailable})
	}, "read"), "unclassified")
	d.want(guard, "GroupReadCoverageStateEvent.State", "out of vocabulary", field(func(tel SlogEngineTelemetry) {
		tel.RecordGroupReadCoverageState(ctx, principal, GroupReadCoverageStateEvent{Read: GroupReadArmGroup, State: SourceState("free text")})
	}, "source_state"), "unclassified")
	d.want(guard, "GroupReadCoverageStateEvent.State", "canonical", field(func(tel SlogEngineTelemetry) {
		tel.RecordGroupReadCoverageState(ctx, principal, GroupReadCoverageStateEvent{Read: GroupReadArmMember, State: SourceTruncated})
	}, "source_state"), "truncated")
	d.want(guard, "GroupReadCoverageStateEvent.State", "planner verdict (pruned: published, not provider-legal)", field(func(tel SlogEngineTelemetry) {
		tel.RecordGroupReadCoverageState(ctx, principal, GroupReadCoverageStateEvent{Read: GroupReadArmMember, State: SourcePruned})
	}, "source_state"), "pruned")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional / boundary", domainExcludedByTypeSystem, "ok")
}

// --- guard 14: the log barrier for request-derived integers ---------------

func domainRequestDerivedLogInt(d *domainTable) {
	const guard = "log barrier (requestDerivedLogInt)"
	cell := func(value int) string { return strconv.Itoa(requestDerivedLogInt(value)) }
	maxInt, minInt := int(^uint(0)>>1), -int(^uint(0)>>1)-1
	d.want(guard, "value", "zero", cell(0), "0")
	d.want(guard, "value", "canonical (the rig's allowance)", cell(10), "10")
	d.want(guard, "value", "boundary - 1 of zero (negative)", cell(-1), "-1")
	d.want(guard, "value", "boundary + 1 of zero", cell(1), "1")
	d.want(guard, "value", "max int", cell(maxInt), strconv.Itoa(maxInt))
	d.want(guard, "value", "max int - 1", cell(maxInt-1), strconv.Itoa(maxInt-1))
	d.want(guard, "value", "min int", cell(minInt), strconv.Itoa(minInt))
	d.want(guard, "value", "min int + 1", cell(minInt+1), strconv.Itoa(minInt+1))
	d.record(guard, "value", "null / container / wrong scalar / fractional / out of vocabulary / duplicate", domainExcludedByTypeSystem+"; the parameter is a Go int", "ok")
}

// --- guard 15: group authorization batching (r1 P1-1) ---------------------

// domainAuthorizationBatching drives the REAL authorizeCohortGroups over the
// whole range of group-list sizes, against the double that models the
// resolver's candidate cap. Every cell reports what the production function
// did: how many calls it made, the largest call, and what it admitted.
func domainAuthorizationBatching(d *domainTable) {
	const guard = "group authorization batching (authorizeCohortGroups)"
	size := groupAuthorizationBatchSize()
	groups := func(count int, id func(int) string) []contractsv1.ContextFabricCohortGroup {
		out := make([]contractsv1.ContextFabricCohortGroup, 0, count)
		for index := 0; index < count; index++ {
			out = append(out, contractsv1.ContextFabricCohortGroup{
				Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: id(index), Label: id(index)},
			})
		}
		return out
	}
	distinct := func(index int) string { return TeamCanonicalID(fmt.Sprintf("team_%03d", index)) }
	run := func(list []contractsv1.ContextFabricCohortGroup, denied map[string]struct{}, failOnCall int) string {
		graph := &batchingProbeGraph{groupAuthorizingGraph: groupAuthorizingGraph{denied: denied}, failOnCall: failOnCall}
		engine := &Engine{graph: graph}
		admitted, batches, err := engine.authorizeCohortGroups(context.Background(), storage.Principal{OrgID: "org_1"}, InvestigationRequest{}, InterpretedQuestion{}, ResolvedGraphBinding{}, list)
		largest := 0
		for _, hints := range graph.hinted {
			largest = max(largest, len(hints))
		}
		if err != nil {
			return fmt.Sprintf("error after batches=%d (fail closed)", batches)
		}
		return fmt.Sprintf("batches=%d largest=%d admitted=%d", batches, largest, len(admitted))
	}
	d.want(guard, "groups", "zero (empty list: no call)", run(nil, nil, 0), "batches=0 largest=0 admitted=0")
	d.want(guard, "groups", "one", run(groups(1, distinct), nil, 0), "batches=1 largest=1 admitted=1")
	d.want(guard, "groups", "boundary - 1 of the batch size", run(groups(size-1, distinct), nil, 0), fmt.Sprintf("batches=1 largest=%d admitted=%d", size-1, size-1))
	d.want(guard, "groups", "boundary (exactly one batch)", run(groups(size, distinct), nil, 0), fmt.Sprintf("batches=1 largest=%d admitted=%d", size, size))
	d.want(guard, "groups", "boundary + 1 (the reviewer's 51)", run(groups(size+1, distinct), nil, 0), fmt.Sprintf("batches=2 largest=%d admitted=%d", size, size+1))
	d.want(guard, "groups", "two full batches", run(groups(2*size, distinct), nil, 0), fmt.Sprintf("batches=2 largest=%d admitted=%d", size, 2*size))
	d.want(guard, "groups", "contract bound (250)", run(groups(contractsv1.ContextFabricCohortGroupsMaxCount, distinct), nil, 0),
		fmt.Sprintf("batches=%d largest=%d admitted=%d", (contractsv1.ContextFabricCohortGroupsMaxCount+size-1)/size, size, contractsv1.ContextFabricCohortGroupsMaxCount))
	d.record(guard, "groups", "contract bound + 1 (251)", "n/a - refused before authorization by groupListOverContractBound (guard 4)", "ok")
	d.want(guard, "groups[]", "duplicate identity across the batch boundary (admitted once)",
		run(groups(size+1, func(index int) string {
			if index == size {
				return distinct(0)
			}
			return distinct(index)
		}), nil, 0), fmt.Sprintf("batches=2 largest=%d admitted=%d", size, size))
	d.want(guard, "groups[]", "a group denied in the SECOND batch (counted, not admitted)",
		run(groups(size+1, distinct), map[string]struct{}{distinct(size): {}}, 0), fmt.Sprintf("batches=2 largest=%d admitted=%d", size, size))
	d.want(guard, "resolver", "the second batch errors (whole step fails closed)", run(groups(size+1, distinct), nil, 2), "error after batches=2 (fail closed)")
	d.want(guard, "resolver", "the first batch errors", run(groups(size+1, distinct), nil, 1), "error after batches=1 (fail closed)")
	d.record(guard, "all fields", "null / wrong container / wrong scalar / fractional", domainExcludedByTypeSystem, "ok")
}

// batchingProbeGraph is the capped authorization double with one addition: it
// can fail on the Nth hinted call, so the domain can show a failure in a LATER
// batch fails the whole step closed.
type batchingProbeGraph struct {
	groupAuthorizingGraph
	failOnCall int
	calls      int
}

func (g *batchingProbeGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeAnchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.calls++
	if g.failOnCall > 0 && g.calls == g.failOnCall {
		g.hinted = append(g.hinted, request.RequestedScope.SubjectHints)
		return SubjectResolution{}, StructureOfferMaterial{}, nil, nil, fmt.Errorf("authorizer unavailable on call %d", g.calls)
	}
	return g.groupAuthorizingGraph.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, scopeAnchorKind)
}

// --- guard 16: the plan-seam I6 line (r1 P2-4) ----------------------------

func domainPlanSeamLine(t *testing.T, d *domainTable) {
	const guard = "plan-seam I6 line (RecordPlanGroupAxisCollapsed)"
	principal := storage.Principal{OrgID: "org_1"}
	emit := func(event PlanGroupAxisCollapsedEvent, keys ...string) string {
		records := captureSlogJSONAtProductionLevel(t, func(logger *slog.Logger) {
			NewSlogEngineTelemetry(logger).RecordPlanGroupAxisCollapsed(context.Background(), principal, event)
		})
		if len(records) != 1 {
			return fmt.Sprintf("records=%d", len(records))
		}
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", key, records[0][key]))
		}
		return strings.Join(parts, " ")
	}
	i6 := FrameValidationFailure{Invariant: FrameInvariantI6, Phase: FrameValidationPhaseA1, Detail: FrameFailureGroupEqualsMember}
	rejected := DecideFrameGate(FrameValidationResult{Outcome: FrameValidationOutcomeRefusedInvalid, Failure: i6}, true)
	d.want(guard, "event", "canonical (the plan seam's own event)",
		emit(PlanGroupAxisCollapsedEvent{GroupKind: SubjectTeam, MemberKind: SubjectTeam, Failure: i6, Gate: rejected}, "level", "failed_invariant", "frame_gate", "refusal_basis"),
		"level=INFO failed_invariant=i6 frame_gate=rejected:i6 refusal_basis=frame_invariant_violated")
	d.want(guard, "Failure.Invariant", "out of vocabulary (named unknown, never free text)",
		emit(PlanGroupAxisCollapsedEvent{Failure: FrameValidationFailure{Invariant: FrameInvariant("free text")}, Gate: rejected}, "failed_invariant"), "failed_invariant=unclassified")
	d.want(guard, "Failure.Invariant", "zero", emit(PlanGroupAxisCollapsedEvent{Gate: rejected}, "failed_invariant"), "failed_invariant=unclassified")
	d.want(guard, "Gate", "zero (not evaluated: never reads as a refusal)", emit(PlanGroupAxisCollapsedEvent{Failure: i6}, "frame_gate", "refusal_basis"), "frame_gate=not_evaluated refusal_basis=")
	d.want(guard, "GroupKind/MemberKind", "zero (outside the published vocabulary: named unknown)", emit(PlanGroupAxisCollapsedEvent{Failure: i6, Gate: rejected}, "group_kind", "member_kind"), "group_kind=unclassified member_kind=unclassified")
	d.want(guard, "GroupKind", "out of vocabulary (model text never reaches the line)", emit(PlanGroupAxisCollapsedEvent{GroupKind: SubjectKind("free text"), MemberKind: SubjectTeam, Failure: i6, Gate: rejected}, "group_kind", "member_kind"), "group_kind=unclassified member_kind=team")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional / boundary", domainExcludedByTypeSystem, "ok")
}

// --- guard 17: the hint-source search-fallback policy ---------------------

// domainSearchFallbackPolicy reads the policy the resolver consults for a hint
// set that resolved nothing, for every source that can reach it. The resolver's
// set predicate over these values is tabled in graphrank
// (TestTheNoFallbackPredicateOverItsWholeDomain), which this package cannot
// import without a cycle.
func domainSearchFallbackPolicy(d *domainTable) {
	const guard = "search fallback (hintsource.SearchFallback)"
	permitted := func(source string) string {
		return fmt.Sprintf("permitted=%v", hintsource.Lookup(source).SearchFallback.Permitted())
	}
	d.want(guard, "Source", "the group authorization (this change's caller)", permitted(string(hintsource.CohortGroupAuthorization)), "permitted=false")
	d.want(guard, "Source", "the answer-reuse recheck (the sibling)", permitted(string(hintsource.AnswerReuseAuthorizationRecheck)), "permitted=false")
	d.want(guard, "Source", "a prior-subject receipt (a conversational reference a search can answer)", permitted(string(hintsource.PriorSubjectReceipt)), "permitted=true")
	d.want(guard, "Source", "canonical caller source", permitted("workbench"), "permitted=true")
	d.want(guard, "Source", "zero (empty string: unenumerated, caller-authored)", permitted(""), "permitted=true")
	d.want(guard, "Source", "out of vocabulary near-miss", permitted("cohort_group_authorization_x"), "permitted=true")
	d.want(guard, "Source", "case variant (identifiers are case-sensitive)", permitted("COHORT_GROUP_AUTHORIZATION"), "permitted=true")
	d.record(guard, "Source", "null / container / wrong scalar / fractional / boundary", domainExcludedByTypeSystem+"; the parameter is a Go string", "ok")
}

// --- guard 18: the served group-read disclosure (r2 P1-3) ------------------

// domainGroupReadDisclosure drives the REAL decision over every refusal and
// every served shape, the admitted-group count it reads, the composer it
// reaches, and the emitter's vocabulary check for the line's new key.
func domainGroupReadDisclosure(t *testing.T, d *domainTable) {
	const guard = "group-read disclosure (groupReadDisclosureFor)"
	two := []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:a"}, {Kind: SubjectTeam, CanonicalID: "team:b"}}
	decide := func(outcome groupReadOutcome, withFacts int) string {
		return string(groupReadDisclosureFor(outcome, withFacts))
	}
	for _, refusal := range []GroupReadRefusal{GroupReadRefusalNoGroupAdmitted, GroupReadRefusalAuthorizationUnavailable, GroupReadRefusalReadFailed, GroupReadRefusalMetadataConflict} {
		d.want(guard, "outcome.Reason", string(refusal)+" (every group unread)", decide(groupReadOutcome{Refused: true, Reason: refusal}, 0), "unread")
	}
	d.want(guard, "outcome.Reason", "over_contract_bound", decide(groupReadOutcome{Refused: true, Reason: GroupReadRefusalOverContractBound, Proposed: 251}, 0), "over_bound")
	d.want(guard, "outcome.Reason", "no_read_requirement (nothing owed)", decide(groupReadOutcome{Refused: true, Reason: GroupReadRefusalNoReadRequirement}, 0), "none")
	d.want(guard, "outcome.Reason", "out of vocabulary (discloses: silence is the failure)", decide(groupReadOutcome{Refused: true, Reason: GroupReadRefusal("free text")}, 0), "unread")
	d.want(guard, "served", "every admitted group read, none denied", decide(groupReadOutcome{Admitted: two}, 2), "none")
	d.want(guard, "served", "one denied", decide(groupReadOutcome{Admitted: two[:1], Denied: 1}, 1), "unread")
	d.want(guard, "served", "one admitted group with no facts (missing)", decide(groupReadOutcome{Admitted: two}, 1), "unread")
	d.want(guard, "served", "zero admitted groups read", decide(groupReadOutcome{Admitted: two}, 0), "unread")
	d.want(guard, "served", "zero proposed (unreachable: the stage is not entered)", decide(groupReadOutcome{}, 0), "none")
	count := func(admitted []SubjectRef, facts ...CanonicalFact) string {
		return fmt.Sprintf("with_facts=%d", admittedGroupsWithFacts(admitted, facts))
	}
	fact := func(id string) CanonicalFact {
		return CanonicalFact{Kind: FactHealth, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: id}}
	}
	d.want(guard, "admittedGroupsWithFacts", "null facts", count(two), "with_facts=0")
	d.want(guard, "admittedGroupsWithFacts", "duplicate facts for one group", count(two, fact("team:a"), fact("team:a")), "with_facts=1")
	d.want(guard, "admittedGroupsWithFacts", "a fact for an unadmitted group", count(two, fact("team:z")), "with_facts=0")
	d.want(guard, "admittedGroupsWithFacts", "canonical (both groups)", count(two, fact("team:a"), fact("team:b")), "with_facts=2")
	d.want(guard, "admittedGroupsWithFacts", "null admitted", count(nil, fact("team:a")), "with_facts=0")
	apply := func(disclosure GroupReadDisclosure, kind SubjectKind) string {
		result := InvestigationResult{}
		applyGroupReadDisclosure(&result, disclosure, kind)
		return fmt.Sprintf("partial=%v limitations=%d", result.Coverage.Partial, len(result.Limitations))
	}
	d.want(guard, "applyGroupReadDisclosure", "none", apply(GroupReadDisclosureNone, SubjectTeam), "partial=false limitations=0")
	d.want(guard, "applyGroupReadDisclosure", "unread", apply(GroupReadDisclosureUnread, SubjectTeam), "partial=true limitations=1")
	d.want(guard, "applyGroupReadDisclosure", "over_bound", apply(GroupReadDisclosureOverBound, SubjectTeam), "partial=true limitations=1")
	d.want(guard, "applyGroupReadDisclosure", "zero group kind (no axis to name)", apply(GroupReadDisclosureUnread, ""), "partial=false limitations=0")
	d.want(guard, "applyGroupReadDisclosure", "out-of-vocabulary disclosure token", apply(GroupReadDisclosure("free text"), SubjectTeam), "partial=false limitations=0")
	// A list already at the contract's limitation cap: the disclosure takes a
	// model caveat's place and the loss is counted, never dropped silently.
	full := func(disclosure GroupReadDisclosure) string {
		result := InvestigationResult{}
		for i := 0; i < contractsv1.ContextFabricLimitationsMaxCount; i++ {
			result.Limitations = append(result.Limitations, fmt.Sprintf("model caveat %d.", i))
		}
		applyGroupReadDisclosure(&result, disclosure, SubjectTeam)
		return fmt.Sprintf("limitations=%d displaced=%d disclosed=%v", len(result.Limitations), result.LimitationsDisplaced, hasGroupReadDisclosure(result.Limitations))
	}
	d.want(guard, "applyGroupReadDisclosure", "unread onto a full list", full(GroupReadDisclosureUnread), fmt.Sprintf("limitations=%d displaced=1 disclosed=true", contractsv1.ContextFabricLimitationsMaxCount))
	// Each token reaches its OWN composer: the exact sentence, not a count.
	sentence := func(disclosure GroupReadDisclosure) string {
		result := InvestigationResult{}
		applyGroupReadDisclosure(&result, disclosure, SubjectTeam)
		return strings.Join(result.Limitations, "|")
	}
	d.want(guard, "applyGroupReadDisclosure", "unread (its sentence)", sentence(GroupReadDisclosureUnread), contractsv1.ContextFabricGroupReadUnreadLimitation(contractsv1.ContextFabricSubjectTeam))
	d.want(guard, "applyGroupReadDisclosure", "over_bound (its sentence)", sentence(GroupReadDisclosureOverBound), contractsv1.ContextFabricGroupListOverBoundLimitation(contractsv1.ContextFabricSubjectTeam))
	principal := storage.Principal{OrgID: "org_1"}
	key := func(disclosure GroupReadDisclosure) string {
		records := captureSlogJSON(t, func(logger *slog.Logger) {
			NewSlogEngineTelemetry(logger).RecordCohortGroupRead(context.Background(), principal, CohortGroupReadEvent{Disclosure: disclosure})
		})
		if len(records) != 1 {
			return fmt.Sprintf("records=%d", len(records))
		}
		return fmt.Sprintf("%v", records[0]["group_read_disclosure"])
	}
	d.want(guard, "CohortGroupReadEvent.Disclosure", "canonical", key(GroupReadDisclosureUnread), "unread")
	d.want(guard, "CohortGroupReadEvent.Disclosure", "out of vocabulary (named unknown)", key(GroupReadDisclosure("free text")), "unclassified")
	d.record(guard, "all fields", "wrong container / wrong scalar / fractional", domainExcludedByTypeSystem, "ok")
}
