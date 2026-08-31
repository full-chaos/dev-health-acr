package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4636 — codex round 1 findings, re-run by this lane before being
// ledgered, then pinned.
//
// Findings 2, 3 and 4 share ONE root cause and are worth reading together: the
// contract deliberately allows a member in more than one group (ownership is a
// relation — `team_project_ownership` orders by `source`, so native and manual
// ownership can both be current), and I documented that in
// `ValidateCohortGroups`... then wrote all three algorithms as if membership
// were a partition. The contract and the implementation disagreed, and the
// contract was the correct one.

func multiTeamFact(memberID string, teams ...string) []CanonicalFact {
	str := func(v string) FactValue { s := v; return FactValue{String: &s} }
	facts := make([]CanonicalFact, 0, len(teams))
	for _, team := range teams {
		facts = append(facts, CanonicalFact{
			Kind:    FactMetrics,
			Subject: SubjectRef{Kind: SubjectProject, CanonicalID: memberID, Label: memberID},
			Fields: map[string]FactValue{
				"team_breakdown": {Rows: []FactValueRow{{Fields: map[string]FactValue{
					"team_id": str(team), "team_name": str(team),
				}}}},
			},
			SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
		})
	}
	return facts
}

// Codex finding 2 (P2): a project owned by two teams must be listed under
// BOTH. Storing one assignment per subject silently drops a true ownership --
// which is the exact thing `ValidateCohortGroups`' own doc comment says a
// validator must not force the engine to do.
func TestBuildCohortGroupsListsAMemberUnderEveryOwningTeam(t *testing.T) {
	t.Parallel()
	facts := multiTeamFact("project_shared", "team_a", "team_b")
	groups, ungrouped := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, planFixtureCohort("project_shared"), facts)
	if ungrouped != 0 {
		t.Fatalf("ungrouped = %d, want 0", ungrouped)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 -- a project owned by team_a AND team_b belongs under both", len(groups))
	}
	for _, group := range groups {
		if len(group.MemberCanonicalIDs) != 1 || group.MemberCanonicalIDs[0] != "project_shared" {
			t.Fatalf("group %q members = %#v", group.Subject.CanonicalID, group.MemberCanonicalIDs)
		}
	}
	// The flattened list still charges the member ONCE -- membership is
	// many-to-many, identity is not.
	if err := contractsv1.ValidateCohortGroups(groups, planFixtureCohort("project_shared").Members); err != nil {
		t.Fatalf("ValidateCohortGroups() = %v", err)
	}
}

// Codex finding 3 (P2): narrowing counted MEMBERSHIPS, not distinct members.
// With team_a={a,b} and team_b={b,c}, three memberships cover only three
// distinct members, and a two-member budget is satisfiable while keeping both
// groups -- but the old arithmetic decided nothing could be narrowed and
// returned all three, producing an avoidable refusal.
func TestNarrowGroupedCohortCountsDistinctMembersNotMemberships(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a", "b", "c")
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "team_a"}, MemberCanonicalIDs: []string{"a", "b"}, Complete: true, Total: 2},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_b", Label: "team_b"}, MemberCanonicalIDs: []string{"b", "c"}, Complete: true, Total: 2},
	}
	kept, groups, narrowed := NarrowGroupedCohort(cohort, 2)
	if !narrowed {
		t.Fatal("narrowed = false, but two distinct members can be retained while keeping both groups")
	}
	if len(kept) != 2 {
		t.Fatalf("kept %d distinct members, want 2", len(kept))
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want both to survive (decision D2)", len(groups))
	}
	for _, group := range groups {
		if len(group.MemberCanonicalIDs) == 0 {
			t.Fatalf("group %q lost every member", group.Subject.CanonicalID)
		}
	}
}

// Codex finding 5 (P3): a measured FIT is a decision, and the event's own doc
// comment calls it "one narrowing decision, or one measured fit". An outcome
// that emits nothing cannot be counted, so "how often does an answer fit
// first time" was unanswerable from the artifacts.
func TestStage3EmitsADecisionEventWhenTheAnswerFits(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := budgetStageEngine(t, budgetStageCohort(3), 1, budgetStageOptions(30, time.Second), &calls, telemetry)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	var fit *PlanNarrowingEvent
	for index := range telemetry.planNarrowings {
		if telemetry.planNarrowings[index].Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult {
			fit = &telemetry.planNarrowings[index]
		}
	}
	if fit == nil {
		t.Fatal("a fitting answer emitted no assembled_result decision event; the fit outcome is uncountable")
	}
	if fit.Overrun != contractsv1.ContextFabricBudgetFits {
		t.Fatalf("Overrun = %q, want %q", fit.Overrun, contractsv1.ContextFabricBudgetFits)
	}
	if fit.MeasuredItems == 0 && fit.MeasuredBytes == 0 {
		t.Fatal("the fit event carries no measurement, which is the only thing that makes it evidence")
	}
	if fit.RefusalPlanned || fit.RetryAttempted {
		t.Fatalf("a fit reported refusal/retry: %+v", fit)
	}
}

// Codex finding 1 (P1): stage 3 measured a PRE-FINAL result. The plan, the
// render shapes and the completeness block are all stamped AFTER it, and the
// route marshals that final shape -- so the engine could accept a result the
// route then 413s on bytes. This is precisely the engine/route gate agreement
// the shared measurement exists to guarantee, so it is the most serious of the
// five.
//
// The fixture drives the byte axis, not the item axis: the plan and render
// shapes add BYTES, never items.
func TestStage3MeasuresTheFinalServedShapeNotAPreFinalOne(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := budgetStageEngine(t, budgetStageCohort(4), 2, budgetStageOptions(100, time.Second), &calls, telemetry)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.AnswerPlan == nil {
		t.Fatal("served result has no plan")
	}
	served, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	var measuredBytes int64
	var found bool
	for _, event := range telemetry.planNarrowings {
		if event.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult {
			measuredBytes, found = event.MeasuredBytes, true
		}
	}
	if !found {
		t.Fatal("stage 3 emitted no assembled_result event, so what it measured cannot be checked")
	}
	// THE ASSERTION. What the engine measured must be the size of the
	// document the route will marshal. Before the fix, stage 3 ran before the
	// plan, the render shapes and the completeness block were stamped, so
	// this number was strictly smaller than the served size and the engine
	// could accept a result the route then 413'd on bytes.
	if measuredBytes != served.Bytes {
		t.Fatalf("stage 3 measured %d bytes but the served result is %d bytes (delta %d) -- the engine and the route are gating different documents",
			measuredBytes, served.Bytes, served.Bytes-measuredBytes)
	}
}
