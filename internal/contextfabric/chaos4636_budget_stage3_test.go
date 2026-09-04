package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4636 stage 3: measure the assembled result, re-synthesize ONCE with a
// smaller input, then a planned, explained refusal.
//
// RED ON origin/main (f9d9688c) by symbol absence: EngineOptions.MaxItems,
// EngineOptions.SynthesisDeadlineReserve, AnswerBudgetRefusal and
// PlanNarrowingEvent do not exist there, so this file does not compile.

// budgetStageCohort builds a discovered cohort of n members.
func budgetStageCohort(n int) *Cohort {
	members := make([]CohortMember, 0, n)
	for index := 0; index < n; index++ {
		id := string(rune('a'+index)) + "_project"
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
		})
	}
	return &Cohort{Kind: SubjectProject, Members: members, Rationale: "budget fixture", Complete: true}
}

// budgetStageEngine builds an engine whose synthesizer emits claimsPerMember
// claimed facts for every cohort member it is GIVEN -- which is what makes a
// smaller synthesis input produce a smaller answer, and therefore what makes
// re-synthesis a real reduction rather than a hopeful one.
func budgetStageEngine(t *testing.T, cohort *Cohort, claimsPerMember int, options EngineOptions, calls *int, telemetry ...*recordingTelemetry) *Engine {
	t.Helper()
	return budgetStageEngineWithTelemetry(t, cohort, claimsPerMember, options, calls, budgetStageTelemetry(telemetry))
}

// budgetStageEngineWithTelemetry is budgetStageEngine with the telemetry sink
// supplied directly rather than as a *recordingTelemetry, so a test can assert
// on the EMITTED LINE instead of a captured struct. A captured struct is one
// step short of what enforcement actually receives, which is the gap the
// refusal-arm tests exist to close.
func budgetStageEngineWithTelemetry(t *testing.T, cohort *Cohort, claimsPerMember int, options EngineOptions, calls *int, telemetry EngineTelemetry) *Engine {
	t.Helper()
	graphCohort := cohort
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: graphCohort,
				Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			*calls++
			claims := []ClaimedFact{}
			if input.Graph.Cohort != nil {
				for _, member := range input.Graph.Cohort.Members {
					for claim := 0; claim < claimsPerMember; claim++ {
						claims = append(claims, ClaimedFact{
							ClaimID: "claim_" + member.Subject.CanonicalID + "_" + string(rune('0'+claim)),
							Kind:    FactStatus, Subject: member.Subject, Field: "status",
							Value: ScalarValue{String: ptrString("green")},
						})
					}
				}
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Telemetry: telemetry,
	}, options)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func ptrString(v string) *string { return &v }

func budgetStageTelemetry(sinks []*recordingTelemetry) EngineTelemetry {
	if len(sinks) > 0 && sinks[0] != nil {
		return sinks[0]
	}
	return &recordingTelemetry{}
}

// TestStage3NamesWhyTheRetryWasDeclined: "this deployment reserves nothing",
// "this request had already spent its deadline" and "there was nothing left to
// narrow" have completely different fixes, and an operator must be able to
// tell them apart from the run's own artifacts. Found on the rig, where a live
// refusal logged only deadline_reserved=false and the three were
// indistinguishable.
func TestStage3NamesWhyTheRetryWasDeclined(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options EngineOptions
		cohort  *Cohort
		want    RetryDeclinedReason
	}{
		{"no reserve configured", budgetStageOptions(4, 0), budgetStageCohort(4), RetryDeclinedNoReserve},
		{"nothing left to narrow", budgetStageOptions(4, time.Second), budgetStageCohort(1), RetryDeclinedNothingToNarrow},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			telemetry := &recordingTelemetry{}
			engine := budgetStageEngine(t, testCase.cohort, 20, testCase.options, &calls, telemetry)
			_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
			if !errors.Is(err, ErrAnswerExceedsBudget) {
				t.Fatalf("error = %v, want a planned refusal", err)
			}
			var refusalEvent *PlanNarrowingEvent
			for index := range telemetry.planNarrowings {
				if telemetry.planNarrowings[index].RefusalPlanned {
					refusalEvent = &telemetry.planNarrowings[index]
				}
			}
			if refusalEvent == nil {
				t.Fatal("no refusal event was recorded")
			}
			if refusalEvent.RetryDeclined != testCase.want {
				t.Fatalf("RetryDeclined = %q, want %q", refusalEvent.RetryDeclined, testCase.want)
			}
			// A flat cohort must never report a group round-robin basis:
			// there are no groups to round-robin over.
			if refusalEvent.Basis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
				t.Fatalf("Basis = %q for a flat cohort, want canonical_id_lexical", refusalEvent.Basis)
			}
			// CHAOS-4735 criterion 6: the refusal telemetry carries the
			// continuation it offered, as a closed token. Before this the
			// event said which family refused and how badly, but not what
			// the caller was told to do about it -- and the field that held
			// that was free English, which cannot be a log dimension.
			declared, found := LookupQuestionFamily(refusalEvent.Family)
			if !found {
				t.Fatalf("refusal event carries family %q, which has no registry row", refusalEvent.Family)
			}
			if refusalEvent.NarrowerContinuationAxis != declared.NarrowerContinuationAxis {
				t.Fatalf("NarrowerContinuationAxis = %q, want the registry's declared %q for family %q",
					refusalEvent.NarrowerContinuationAxis, declared.NarrowerContinuationAxis, refusalEvent.Family)
			}
			if !ValidNarrowingContinuationAxis(refusalEvent.NarrowerContinuationAxis) {
				t.Fatalf("NarrowerContinuationAxis = %q is not a vocabulary member", refusalEvent.NarrowerContinuationAxis)
			}
		})
	}
}

func budgetStageOptions(maxItems int, reserve time.Duration) EngineOptions {
	return EngineOptions{
		ServiceVersion: "acr-test", MaxItems: maxItems, SynthesisDeadlineReserve: reserve,
		Now:         func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID: func() string { return "result_99999999" },
	}
}

// TestStage3ReSynthesizesOnceWhenTheAnswerDoesNotFit is the core of decision
// D5's "one bounded retry": the first answer is measured, found over budget,
// and the engine re-runs synthesis with FEWER MEMBERS -- it never trims the
// composed answer, because by then a dropped driver can orphan a render-shape
// point that cites it and make the stored and served answers diverge.
func TestStage3ReSynthesizesOnceWhenTheAnswerDoesNotFit(t *testing.T) {
	t.Parallel()
	calls := 0
	// 6 members x 2 claims = 12 claims + 6 cohort members = 18 items against
	// a 12-item budget. Halving to 3 members gives 6 claims + 3 members = 9.
	engine := budgetStageEngine(t, budgetStageCohort(6), 2, budgetStageOptions(12, time.Second), &calls)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want exactly 2 -- one bounded retry, never k of them", calls)
	}
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	if measurement.Items.Budgeted() > 12 {
		t.Fatalf("served an answer of %d budgeted items against a 12-item ceiling", measurement.Items.Budgeted())
	}
	if result.AnswerPlan == nil {
		t.Fatal("the served result carries no plan; an over-budget answer must name the number that was wrong")
	}
	if !result.AnswerPlan.Narrowed() {
		t.Fatal("the plan records no narrowing, but the answer was narrowed -- a narrowing the caller is not told about is the silent truncation this slice removes")
	}
	var sawStage3 bool
	for _, step := range result.AnswerPlan.Narrowing {
		if step.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult {
			sawStage3 = true
			if step.Overrun != contractsv1.ContextFabricBudgetOverrunItems {
				t.Fatalf("stage 3 step recorded overrun %q, want items", step.Overrun)
			}
			if step.After >= step.Before {
				t.Fatalf("stage 3 step did not reduce: before=%d after=%d", step.Before, step.After)
			}
		}
	}
	if !sawStage3 {
		t.Fatal("no assembled_result narrowing step was recorded")
	}
}

// TestStage3RefusesWithAnExplanationWhenTheRetryStillDoesNotFit is decision
// D5 option C. "Always serve" was unsound -- re-synthesis does not reduce the
// candidate list, which the route counts -- so the honest outcome is a
// refusal that says what was too large and what would fit.
func TestStage3RefusesWithAnExplanationWhenTheRetryStillDoesNotFit(t *testing.T) {
	t.Parallel()
	calls := 0
	// 20 claims per member makes even a single member exceed a 4-item budget,
	// so the retry cannot rescue it.
	engine := budgetStageEngine(t, budgetStageCohort(4), 20, budgetStageOptions(4, time.Second), &calls)

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err == nil {
		t.Fatal("Investigate() returned no error for an answer that cannot fit")
	}
	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want ErrAnswerExceedsBudget", err)
	}
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error %v does not carry AnswerBudgetRefusal", err)
	}
	// The whole point of C over today's behaviour: the refusal EXPLAINS.
	if refusal.Overrun != contractsv1.ContextFabricBudgetOverrunItems {
		t.Fatalf("Overrun = %q", refusal.Overrun)
	}
	if refusal.MeasuredItems <= refusal.MaxItems {
		t.Fatalf("refusal reports %d measured items against a %d ceiling; it should exceed it", refusal.MeasuredItems, refusal.MaxItems)
	}
	// CHAOS-4735: the refusal still EXPLAINS, but with a closed token rather
	// than an English sentence the engine wrote. Asserting vocabulary
	// MEMBERSHIP rather than non-emptiness is the point -- non-emptiness is
	// what a re-introduced phrase table would also satisfy.
	if !ValidNarrowingContinuationAxis(refusal.NarrowerContinuationAxis) {
		t.Fatalf("NarrowerContinuationAxis = %q, not a member of the closed vocabulary; an unexplained 413 is the status quo this replaces, and free text is the shape it may not be replaced WITH", refusal.NarrowerContinuationAxis)
	}
	// And it is the family's DECLARED axis, not something re-derived at the
	// refusal. This fixture's plan carries `unclassified`, whose declared
	// axis is `none` -- which is the correct answer and a deliberate
	// behaviour change: the deleted switch's DEFAULT arm handed unclassified
	// questions "ask about a single subject, or a shorter evidence window",
	// inventing advice for a question the engine had just failed to classify.
	declared, found := LookupQuestionFamily(refusal.Family)
	if !found {
		t.Fatalf("refusal carries family %q, which has no registry row", refusal.Family)
	}
	if refusal.NarrowerContinuationAxis != declared.NarrowerContinuationAxis {
		t.Fatalf("NarrowerContinuationAxis = %q for family %q, want the registry's declared %q", refusal.NarrowerContinuationAxis, refusal.Family, declared.NarrowerContinuationAxis)
	}
	if !refusal.RetryAttempted {
		t.Fatal("RetryAttempted = false, but the deadline allowed a retry and there were members to narrow")
	}
}

// TestStage3DoesNotRetryWithoutAReservedDeadline is decision D5's second,
// independent hole. The whole request shares one timeout, so an unreserved
// retry after a slow first synthesis is a 504 rather than a partial answer.
// An engine that was not told how long it may spend does not gamble the
// caller's deadline on a second model call.
func TestStage3DoesNotRetryWithoutAReservedDeadline(t *testing.T) {
	t.Parallel()
	calls := 0
	engine := budgetStageEngine(t, budgetStageCohort(6), 2, budgetStageOptions(12, 0), &calls)

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want a planned refusal rather than a gamble", err)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times with no reserved deadline, want exactly 1", calls)
	}
	var refusal AnswerBudgetRefusal
	if errors.As(err, &refusal) && refusal.RetryAttempted {
		t.Fatal("RetryAttempted = true, but no deadline was reserved")
	}
}

// TestStage3RefusesRatherThanRetryWhenTooLittleDeadlineRemains: the same
// hole, from the other side. A context whose remaining time is below the
// reserve must refuse, not start a synthesis that would time out.
func TestStage3RefusesRatherThanRetryWhenTooLittleDeadlineRemains(t *testing.T) {
	t.Parallel()
	calls := 0
	engine := budgetStageEngine(t, budgetStageCohort(6), 2, budgetStageOptions(12, time.Hour), &calls)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want a planned refusal", err)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times with 50ms left against a 1h reserve, want exactly 1", calls)
	}
}

// TestAnAnswerThatFitsIsNeverNarrowed is the discriminating control: a build
// that narrows eagerly -- or measures against the wrong ceiling -- flips
// exactly this, and every other test here would still pass.
func TestAnAnswerThatFitsIsNeverNarrowed(t *testing.T) {
	t.Parallel()
	calls := 0
	engine := budgetStageEngine(t, budgetStageCohort(3), 1, budgetStageOptions(30, time.Second), &calls)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times for an answer that fits, want 1", calls)
	}
	if result.AnswerPlan == nil {
		t.Fatal("a fitting answer still carries a plan")
	}
	for _, step := range result.AnswerPlan.Narrowing {
		if step.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult {
			t.Fatalf("an answer that fits recorded a stage-3 narrowing: %+v", step)
		}
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 3 {
		t.Fatalf("cohort was narrowed despite fitting: %+v", result.Cohort)
	}
}

// TestAnUnconfiguredBudgetNarrowsNothing: an engine composed without the
// ceilings behaves exactly as it did before this slice. Every existing
// composition and every test that does not set them means precisely that.
func TestAnUnconfiguredBudgetNarrowsNothing(t *testing.T) {
	t.Parallel()
	calls := 0
	engine := budgetStageEngine(t, budgetStageCohort(8), 5, budgetStageOptions(0, 0), &calls)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times with no configured budget, want 1", calls)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 8 {
		t.Fatalf("an unconfigured engine narrowed the cohort: %+v", result.Cohort)
	}
}

// TestNarrowSynthesisInputBasisSurvivesTheNoNarrowReturn pins codex round 1,
// finding 4 (EXECUTED): a grouped cohort already at its floor (every group
// down to one member) runs the overlap-aware selection, finds no room to
// narrow further, and returns Narrow=false through the SAME early return
// that also carries Before/After -- but that return did not carry Basis,
// so the caller's refusal telemetry fell back to a stale default
// (largest_group_round_robin) that named an order that never ran.
func TestNarrowSynthesisInputBasisSurvivesTheNoNarrowReturn(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "b1", "c1")
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "ta", Label: "ta"}, MemberCanonicalIDs: []string{"a1"}, Complete: true, Total: 1},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "tb", Label: "tb"}, MemberCanonicalIDs: []string{"b1"}, Complete: true, Total: 1},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "tc", Label: "tc"}, MemberCanonicalIDs: []string{"c1"}, Complete: true, Total: 1},
	}
	params := synthesisAssemblyParams{Graph: GraphContext{Cohort: cohort}, Facts: CanonicalFactBundle{}}
	result := narrowSynthesisInput(params, &AnswerPlan{})
	if result.Narrow {
		t.Fatal("every group is already at its floor; expected the no-narrow terminal case")
	}
	if result.Basis != contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover {
		t.Fatalf("Basis = %q on the no-narrow return, want overlap_aware_set_cover -- the selection DID run, it just found nothing left to narrow", result.Basis)
	}
}

// TestNarrowSynthesisInputTrivialCohortReportsNoBasisAtAll documents the
// DELIBERATE counterpart to the finding above (shape-swept from the same
// class, team-lead's review): a cohort of at most one member returns before
// ANY selection algorithm runs at all -- there is no basis to report because
// none executed, unlike the no-narrow case above where the overlap-aware
// selection DID run and simply found nothing left to cut. Pinning this
// distinction so a future change cannot "fix" it into looking like the
// bug class this ticket already closed.
func TestNarrowSynthesisInputTrivialCohortReportsNoBasisAtAll(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1")
	params := synthesisAssemblyParams{Graph: GraphContext{Cohort: cohort}, Facts: CanonicalFactBundle{}}
	result := narrowSynthesisInput(params, &AnswerPlan{})
	if result.Narrow {
		t.Fatal("a one-member cohort cannot narrow further")
	}
	if result.Basis != "" {
		t.Fatalf("Basis = %q, want the empty zero value -- no selection algorithm ran at all for a trivial cohort", result.Basis)
	}
}
