package contextfabric

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
		"the fold PRESERVES an unknown state rather than refusing it; the pre-fold disclosure routes its own copy through validFactSourceState and publishes `unclassified`", "ok")
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
