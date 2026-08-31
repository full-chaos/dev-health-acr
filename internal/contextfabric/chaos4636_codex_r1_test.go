package contextfabric

import (
	"context"
	"errors"
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

// ── codex round 2 ────────────────────────────────────────────────────────────

// failingRetrySynthesizer succeeds on the first call and fails on the second,
// which is the shape of a transient upstream fault landing on the retry.
type failingRetrySynthesizer struct {
	calls    *int
	first    AnswerSynthesizer
	failWith error
}

func (f failingRetrySynthesizer) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	if *f.calls >= 1 {
		*f.calls++
		return InvestigationResult{}, f.failWith
	}
	return f.first.Synthesize(ctx, principal, input)
}

// Codex round 2, finding 2 (P2): a retry synthesis failure was swallowed and
// reclassified as a deterministic budget refusal, so a transient upstream
// fault reached the caller as "ask a narrower question" — a non-retryable
// answer to a retryable problem, with nothing in the telemetry naming what
// actually happened. Rewording the question would not have helped.
func TestStage3PropagatesARetrySynthesisFailureRatherThanCallingItABudgetRefusal(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// 6 members x 3 claims = 18 claims + 6 members = 24 items over a 12-item
	// budget, so a retry is required; the retry then fails.
	base := budgetStageEngine(t, budgetStageCohort(6), 3, budgetStageOptions(12, time.Second), &calls, telemetry)
	upstream := errors.New("model runtime unavailable")
	base.synthesizer = failingRetrySynthesizer{calls: &calls, first: base.synthesizer, failWith: upstream}

	_, err := base.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err == nil {
		t.Fatal("Investigate() returned no error when the retry synthesis failed")
	}
	if errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("a failed retry was reported as a budget refusal: %v -- the caller would be told to ask a narrower question for a transient upstream fault", err)
	}
	if !errors.Is(err, upstream) {
		t.Fatalf("error = %v, want the retry's own upstream error to survive", err)
	}
	// And the over-budget measurement is not lost: it is on the event, with
	// the retry's failure distinguished from "the retry ran and did not fit".
	var failed *PlanNarrowingEvent
	for index := range telemetry.planNarrowings {
		if telemetry.planNarrowings[index].RetryFailed {
			failed = &telemetry.planNarrowings[index]
		}
	}
	if failed == nil {
		t.Fatal("no event recorded the retry FAILING; a swallowed retry error with no telemetry is the silent-failure class")
	}
	if failed.MeasuredItems == 0 {
		t.Fatal("the retry-failure event lost the over-budget measurement that caused the retry")
	}
	if failed.RetryFit {
		t.Fatal("a failed retry reported RetryFit")
	}
}

// Codex round 2, finding 1 (P2): the retry ran the assembly twice, and the
// commit-affirmation retraction was emitted from INSIDE it — so one served
// investigation emitted two retraction events, corrupting the decision-basis
// counter. The `Retry` flag existed but was never wired to anything.
//
// The fix is to defer rather than label: the first pass's answer is discarded,
// so its retraction never happened from the caller's point of view.
//
// The fixture must actually PRODUCE a retraction, and the test asserts that it
// did — my first version of this test committed no subject, so no retraction
// could occur, the count was 0 either way, and it passed against the very
// defect it claimed to pin. Caught by mutating the fix back.
func TestRetryEmitsCommitAffirmationTelemetryOnceForTheServedAnswer(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// A committed subject with NO canonical fact behind it is what the
	// affirmation gate retracts (commitSubjectAffirmed requires a canonical
	// fact attributable to the subject; this fixture's fact reader returns
	// none).
	committed := SubjectRef{Kind: SubjectProject, CanonicalID: "project_unaffirmed", Label: "Unaffirmed"}
	engine := budgetStageEngine(t, budgetStageCohort(6), 2, budgetStageOptions(12, time.Second), &calls, telemetry)
	engine.graph = &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committed}},
		context: GraphContext{
			Cohort: budgetStageCohort(6),
			Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2 -- the fixture must actually retry or this test means nothing", calls)
	}
	// NON-VACUITY: a retraction must actually have happened, or the count
	// below is trivially satisfied.
	if telemetry.commitAffirmations == 0 {
		t.Fatal("no commit-affirmation retraction occurred; this fixture cannot observe the double-count it exists to pin")
	}
	if telemetry.commitAffirmations != 1 {
		t.Fatalf("commit-affirmation events = %d for ONE served investigation; the discarded first pass emitted its own", telemetry.commitAffirmations)
	}
}

// ── codex round 3 ────────────────────────────────────────────────────────────

// Codex round 3, finding 2 (P2) — and the reason this is written as a CLASS
// test rather than a third one-emitter test.
//
// Round 2 found the commit-affirmation retraction double-emitting on a retry.
// I deferred that ONE emitter. Round 3 immediately found two more in the same
// function doing the same thing. The defect was never "this emitter"; it was
// "this function emits, and a retry runs it twice". So this test asserts the
// property over EVERY per-investigation event the assembly produces, and a
// new emission that forgets to defer fails here without anyone remembering to
// add a case.
func TestRetryEmitsEveryPerInvestigationEventExactlyOnce(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	committed := SubjectRef{Kind: SubjectProject, CanonicalID: "project_unaffirmed", Label: "Unaffirmed"}
	engine := budgetStageEngine(t, budgetStageCohort(6), 2, budgetStageOptions(12, time.Second), &calls, telemetry)
	engine.graph = &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committed}},
		context: GraphContext{
			Cohort: budgetStageCohort(6),
			Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2 -- the fixture must actually retry or this test means nothing", calls)
	}
	// Each of these is documented as measuring per-INVESTIGATION behaviour,
	// so two events for one served investigation corrupts the denominator.
	for _, check := range []struct {
		name string
		got  int
	}{
		{"window canonicalization", len(telemetry.windowCanonicalizationOutcomes)},
		{"cohort driver narration", len(telemetry.cohortDriverNarrations)},
		{"commit-affirmation retraction", telemetry.commitAffirmations},
	} {
		// NON-VACUITY first: an event that never fired cannot observe a
		// double-count, and a test asserting "<= 1" on zero passes against
		// the defect it claims to pin.
		if check.got == 0 {
			t.Fatalf("%s never fired; this fixture cannot observe its double-count", check.name)
		}
		if check.got != 1 {
			t.Errorf("%s fired %d times for ONE served investigation; the discarded first pass emitted its own", check.name, check.got)
		}
	}
}

// Codex round 3, finding 1 (P2): the axis-conflict window veto returns AFTER
// the planning stage and was the one post-plan exit that did not stamp the
// plan — and its result is SAVED, so the omission is permanent in the store.
//
// Reachable exactly as codex described: a current-axis request carrying a
// confirmed window, whose interpretation moves the axis to historical.
func TestPostPlanAxisConflictVetoCarriesTheAnswerPlan(t *testing.T) {
	t.Parallel()
	calls := 0
	engine := budgetStageEngine(t, budgetStageCohort(2), 1, budgetStageOptions(30, time.Second), &calls)
	// Interpret moves the axis away from current (valid-time as-of), which is
	// what the veto exists to catch: the request committed a window the
	// interpretation no longer honours.
	// BEFORE the engine's fake clock (time.Unix(200,0)) -- an as-of in the
	// future is refused by resolveTimeContext before this veto is reached.
	asOf := time.Unix(100, 0).UTC()
	engine.interpreter = interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
		return InterpretedQuestion{
			Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
			TimeContext:      TimeContext{Axis: TemporalValidTime, AsOf: &asOf},
			FactRequirements: []FactRequirement{{Kind: FactStatus}},
		}, nil
	})

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.ResultID == "" {
		t.Fatalf("expected a composed veto result, got %+v", result.Status)
	}
	if result.AnswerPlan == nil {
		t.Fatal("a post-plan veto served and persisted no answer_plan; every exit after PlanAnswer must carry it")
	}
	if result.AnswerPlan.Family == "" {
		t.Fatalf("the stamped plan carries no family: %+v", result.AnswerPlan)
	}
}

// ── drop-shape sweep (post round 4) ──────────────────────────────────────────

// TestNarrowGroupedCohortNeverDropsAnUngroupedMember pins the data-loss defect
// round 4 exposed and the sweep confirmed.
//
// BuildCohortGroups leaves an unplaceable member ungrouped DELIBERATELY — its
// own doc comment says inventing a group for one, or silently removing it,
// "would both be worse than saying so" — and NarrowGroupedCohort then removed
// it, because its whole population was built from group member lists. The
// contract and the implementation disagreed for the third time in this area.
//
// On real data this is not hypothetical: the providers that carry the team
// association join on compounding risk, so a member whose facts came back
// empty genuinely has no derivable group.
func TestNarrowGroupedCohortNeverDropsAnUngroupedMember(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "a2", "a3", "orphan")
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "team_a"},
			MemberCanonicalIDs: []string{"a1", "a2", "a3"}, Complete: true, Total: 3},
	}
	kept, groups, narrowed := NarrowGroupedCohort(cohort, 3)
	if !narrowed {
		t.Fatal("narrowed = false; 4 members against a 3-member cap must narrow")
	}
	var sawOrphan bool
	for _, member := range kept {
		if member.Subject.CanonicalID == "orphan" {
			sawOrphan = true
		}
	}
	if !sawOrphan {
		t.Fatalf("the ungrouped member was silently dropped; kept = %v", canonicalIDsOf(kept))
	}
	if len(groups) != 1 || len(groups[0].MemberCanonicalIDs) == 0 {
		t.Fatalf("the group did not survive: %#v", groups)
	}
	// The cap is over the WHOLE member list, which is what it bounds.
	if len(kept) > 3 {
		t.Fatalf("kept %d members against a 3-member cap", len(kept))
	}
}

// TestNarrowGroupedCohortCountsUngroupedMembersInTheCap: the population the cap
// applies to is every cohort member, not just the grouped ones. Counting only
// grouped members reported "nothing to narrow" for a cohort that was over
// budget, which is how the oversized answer reached a refusal.
func TestNarrowGroupedCohortCountsUngroupedMembersInTheCap(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "orphan1", "orphan2")
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "team_a"},
			MemberCanonicalIDs: []string{"a1"}, Complete: true, Total: 1},
	}
	kept, _, narrowed := NarrowGroupedCohort(cohort, 2)
	if !narrowed {
		t.Fatal("narrowed = false: 1 grouped + 2 ungrouped = 3 members against a 2-member cap")
	}
	if len(kept) != 2 {
		t.Fatalf("kept %d members, want 2", len(kept))
	}
	// The GROUP's member survives: an ungrouped member is peeled before a
	// group is taken to its floor.
	var sawGrouped bool
	for _, member := range kept {
		if member.Subject.CanonicalID == "a1" {
			sawGrouped = true
		}
	}
	if !sawGrouped {
		t.Fatalf("a group lost its only member while ungrouped members remained: %v", canonicalIDsOf(kept))
	}
}

func canonicalIDsOf(members []CohortMember) []string {
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.Subject.CanonicalID)
	}
	return ids
}
