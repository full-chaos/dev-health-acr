package contextfabric

import (
	"context"
	"encoding/json"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// gapAMemberFact is one member fact carrying a team_breakdown row per team, with
// a distinct value and evidence id, so a rewrite of any of them is visible.
func gapAMemberFact(memberID, value string, teams ...string) CanonicalFact {
	rows := make([]FactValueRow, 0, len(teams))
	for _, team := range teams {
		rows = append(rows, FactValueRow{Fields: map[string]FactValue{
			"scope": StringFactValue("team"), "team_id": StringFactValue(team), "team_name": StringFactValue(team),
		}})
	}
	return CanonicalFact{
		Kind:           FactMetrics,
		Subject:        SubjectRef{Kind: SubjectProject, CanonicalID: memberID, Label: memberID},
		Fields:         map[string]FactValue{"team_breakdown": {Rows: rows}, "status": StringFactValue(value)},
		EvidenceRefIDs: []string{"ev_" + memberID},
		SourceState:    SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

// TestGroupingDoesNotRewriteMemberFactSubjects is CHAOS-5285 test-table row 8
// (GAP A), GREEN at the parent and after: grouping READS member facts to find
// each member's groups and must never write to them.
//
// The identity correction canonicalises the GROUP key it reads out of a
// member fact's team rows. A correction applied in place -- to the row, or to
// the fact's subject -- would change member evidence the answer cites, and
// nothing about the answer would look wrong. So the whole input is snapshotted
// DEEPLY (JSON, which walks every map and slice) before grouping and compared
// after.
//
// Deliberately NOT asserting the canonical group spelling: that belongs to
// the stage-1 identity pin, and this control must hold whatever the spelling.
func TestGroupingDoesNotRewriteMemberFactSubjects(t *testing.T) {
	t.Parallel()

	projects := []SubjectRef{projectRef("p1"), projectRef("p2"), projectRef("p3")}
	cohort := cohortWith(SubjectProject, projects, nil, true)
	facts := []CanonicalFact{
		gapAMemberFact("p1", "green", "t1"),
		gapAMemberFact("p2", "amber", "t1", "t2"),
		gapAMemberFact("p3", "red", "t2"),
	}
	before, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}

	groups, ungrouped, outcome := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort, facts)
	after, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("groups=%d ungrouped=%d refusal=%q", len(groups), ungrouped, outcome.Refusal)
	if string(before) != string(after) {
		t.Fatalf("grouping changed the member facts it read:\nbefore %s\nafter  %s", before, after)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 (t1, t2) -- the fixture must actually group, or immutability is vacuous", len(groups))
	}
	for _, group := range groups {
		for _, id := range group.MemberCanonicalIDs {
			if id != "p1" && id != "p2" && id != "p3" {
				t.Errorf("group %s lists member %q, not one of the project ids", group.Subject.CanonicalID, id)
			}
		}
	}

	// The member evidence ALONE certifies the members and not the groups.
	grouped := cohortWith(SubjectProject, projects, nil, true)
	grouped.Groups = groups
	memberReq := scopeRequirement(CompletionScopeEachMember, SubjectProject, SubjectRoleMember, CompletionQuantifierAtLeastOne, FactMetrics)
	groupReq := scopeRequirement(CompletionScopeEachGroup, SubjectTeam, SubjectRoleGroup, CompletionQuantifierAtLeastOne, FactMetrics)
	rows := evaluateCohort([]contractsv1.ContextFabricPlanRequirement{memberReq, groupReq},
		grouped, factCoverage(FactMetrics, SourceAvailable), CanonicalFactBundle{Facts: facts})
	assertRow(t, rowFor(t, rows, memberReq.Requirement),
		contractsv1.ContextFabricRequirementSatisfied, contractsv1.ContextFabricAnswerImpactNone, "", false, 3, 3)
	assertRow(t, rowFor(t, rows, groupReq.Requirement),
		contractsv1.ContextFabricRequirementNarrowed, contractsv1.ContextFabricAnswerImpactScope,
		contractsv1.ContextFabricCoverageDetailFactNarrowed, false, 0, 2)
}

// TestBothReadsLeaveTheMemberBundleAsItWasRead is row 9 (GAP A, wiring), RED
// where no second read exists: the member facts synthesis receives, after the
// group read and the merge, are exactly the facts the member read returned.
//
// The second read and its merge run while the first bundle is live. A merge
// that re-sliced, re-sorted into, or rewrote the first bundle's facts would
// hand synthesis member evidence that no provider returned.
func TestBothReadsLeaveTheMemberBundleAsItWasRead(t *testing.T) {
	groupReadSynthesisFacts = nil
	t.Cleanup(func() { groupReadSynthesisFacts = nil })

	var memberRead []byte
	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Facts = []CanonicalFact{groupKindFact("team_security", FactHealth), groupKindFact("team_platform", FactHealth)}
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		memberRead, _ = json.Marshal(bundle.Facts)
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if grouped := recorder.groupRootedRequests(SubjectTeam); len(grouped) != 1 {
		t.Fatalf("group-rooted requests = %v, want exactly one -- without the second read this row measures nothing", grouped)
	}
	var memberFacts []CanonicalFact
	groupFacts := 0
	for _, fact := range groupReadSynthesisFacts {
		if fact.Subject.Kind == SubjectTeam {
			groupFacts++
			continue
		}
		memberFacts = append(memberFacts, fact)
	}
	served, _ := json.Marshal(memberFacts)
	t.Logf("member facts read=%d served=%d group facts served=%d", len(groupReadMemberFacts()), len(memberFacts), groupFacts)
	if string(served) != string(memberRead) {
		t.Errorf("the member facts synthesis received differ from the member read's:\nread   %s\nserved %s", memberRead, served)
	}
	if groupFacts != 2 {
		t.Errorf("group facts reaching synthesis = %d, want 2 -- both reads' evidence must be present", groupFacts)
	}
}

// couplingRequirementDeriver declares the §(6) discriminator's rows: member
// and group `state` requirements over health and readiness, corroborated,
// plus a health-only member requirement.
type couplingRequirementDeriver struct{}

func (couplingRequirementDeriver) DeriveRequirements(QuestionFrame) []DerivedRequirement {
	return []DerivedRequirement{
		{RequirementCoordinate: RequirementCoordinate{Obligation: ObligationState, Role: SubjectRoleGroup, Subject: SubjectTeam},
			Kind: ObligationKindRead, FactKinds: []FactKind{FactHealth, FactReadiness}, Scope: CompletionScopeEachGroup, Quantifier: CompletionQuantifierCorroborated},
		{RequirementCoordinate: RequirementCoordinate{Obligation: ObligationState, Role: SubjectRoleMember, Subject: SubjectProject},
			Kind: ObligationKindRead, FactKinds: []FactKind{FactHealth, FactReadiness}, Scope: CompletionScopeEachMember, Quantifier: CompletionQuantifierCorroborated},
		{RequirementCoordinate: RequirementCoordinate{Obligation: ObligationHealth, Role: SubjectRoleMember, Subject: SubjectProject},
			Kind: ObligationKindRead, FactKinds: []FactKind{FactHealth}, Scope: CompletionScopeEachMember, Quantifier: CompletionQuantifierAtLeastOne},
	}
}

// couplingMemberFacts gives every member the grouping row plus health and
// readiness facts: complete member evidence for both member requirements.
func couplingMemberFacts() []CanonicalFact {
	facts := groupReadMemberFacts()
	for _, member := range []string{"project_a", "project_b"} {
		for _, kind := range []FactKind{FactHealth, FactReadiness} {
			facts = append(facts, CanonicalFact{
				Kind: kind, Subject: SubjectRef{Kind: SubjectProject, CanonicalID: member, Label: member},
				Fields: map[string]FactValue{"v": StringFactValue("ok")}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
			})
		}
	}
	return facts
}

// TestTheCouplingFoldPricesAFailedGroupKindDirectly is row 11, GREEN at the
// parent and after: the conservative coverage coupling the plan ACCEPTS,
// through the production merge and the production evaluator.
//
// Member evidence is complete. The group read reports health UNAVAILABLE.
// Both reads report under `canonical_fact:health`, and the fold keeps the
// worse state -- so the member `state` requirement reads narrowed/depth 1 of
// 2 and the health-only member requirement reads unavailable, although every
// member health fact is still there. That is the price, and it is pinned so a
// change to it is a decision rather than an accident.
func TestTheCouplingFoldPricesAFailedGroupKindDirectly(t *testing.T) {
	t.Parallel()

	member := emptyFactBundle()
	member.Facts = couplingMemberFacts()
	member.Coverage = factCoverage(FactMetrics, SourceAvailable, FactHealth, SourceAvailable, FactReadiness, SourceAvailable)
	group := emptyFactBundle()
	group.Facts = []CanonicalFact{groupKindFact("team_security", FactReadiness), groupKindFact("team_platform", FactReadiness)}
	group.Coverage = factCoverage(FactHealth, SourceUnavailable, FactReadiness, SourceAvailable)
	group.Coverage.Sources[0].Reason = "canonical fact capability is unavailable"
	if conflicted := mergeGroupBundle(&member, group, "org_1"); conflicted {
		t.Fatalf("fixture defect: the two bundles conflict")
	}

	projects := []SubjectRef{projectRef("project_a"), projectRef("project_b")}
	cohort := cohortWith(SubjectProject, projects, []SubjectRef{teamRef(TeamCanonicalID("team_security")), teamRef(TeamCanonicalID("team_platform"))}, true)
	published := PlanRequirementsFromDerived(couplingRequirementDeriver{}.DeriveRequirements(QuestionFrame{}))
	rows := evaluateCohort(published, cohort, member.Coverage, member)
	for _, row := range rows {
		t.Logf("row %s: %s/%s %d/%d cause=%s", row.Requirement, row.Outcome, row.Impact, row.Served, row.Declared, row.CauseCoverage)
	}
	assertRow(t, rowFor(t, rows, "state/member/project"),
		contractsv1.ContextFabricRequirementNarrowed, contractsv1.ContextFabricAnswerImpactDepth,
		contractsv1.ContextFabricCoverageDetailFactProviderReported, true, 1, 2)
	assertRow(t, rowFor(t, rows, "health/member/project"),
		contractsv1.ContextFabricRequirementUnavailable, contractsv1.ContextFabricAnswerImpactDimension,
		contractsv1.ContextFabricCoverageDetailFactProviderReported, true, 0, 1)
	// The member facts themselves are untouched by the fold.
	health := 0
	for _, fact := range member.Facts {
		if fact.Kind == FactHealth && fact.Subject.Kind == SubjectProject {
			health++
		}
	}
	if health != 2 {
		t.Errorf("member health facts after the fold = %d, want 2 -- the coupling prices coverage, it never removes evidence", health)
	}
}

// TestTheRealSecondReadReachesTheCouplingFold is row 12, RED where no second
// read exists: the same discriminator through the engine, so the group read's
// failed kind actually reaches the served document's rows.
func TestTheRealSecondReadReachesTheCouplingFold(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Facts = []CanonicalFact{groupKindFact("team_security", FactReadiness), groupKindFact("team_platform", FactReadiness)}
				bundle.Coverage = factCoverage(FactHealth, SourceUnavailable, FactReadiness, SourceAvailable)
				bundle.Coverage.Sources[0].Reason = "canonical fact capability is unavailable"
				return bundle
			}
		}
		bundle.Facts = couplingMemberFacts()
		bundle.Coverage = factCoverage(FactMetrics, SourceAvailable, FactHealth, SourceAvailable, FactReadiness, SourceAvailable)
		return bundle
	}}
	engine, request := groupReadEngineFixtureConfigured(t, &recordingTelemetry{}, recorder, []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}, nil, SubjectProject, nil, nil, func(config *groupReadFixtureConfig) { config.deriver = couplingRequirementDeriver{} })

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if grouped := recorder.groupRootedRequests(SubjectTeam); len(grouped) != 1 {
		t.Fatalf("group-rooted requests = %v, want exactly one", grouped)
	}
	byID := map[string]RequirementOutcomeRow{}
	for _, row := range result.Completeness.Outcomes {
		if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			byID[row.Requirement] = row
			t.Logf("row %s: %s/%s %d/%d cause=%s", row.Requirement, row.Outcome, row.Impact, row.Served, row.Declared, row.CauseCoverage)
		}
	}
	memberState, ok := byID["state/member/project"]
	if !ok || memberState.Outcome != contractsv1.ContextFabricRequirementNarrowed || memberState.Impact != contractsv1.ContextFabricAnswerImpactDepth ||
		memberState.Served != 1 || memberState.Declared != 2 || memberState.CauseCoverage != contractsv1.ContextFabricCoverageDetailFactProviderReported {
		t.Errorf("member state row = %+v, want narrowed/depth 1/2 fact_provider_reported -- the group read's failed health must reach the fold, as the direct control prices it", memberState)
	}
	memberHealth, ok := byID["health/member/project"]
	if !ok || memberHealth.Outcome != contractsv1.ContextFabricRequirementUnavailable || memberHealth.Served != 0 || memberHealth.Declared != 1 {
		t.Errorf("member health row = %+v, want unavailable 0/1", memberHealth)
	}
}
