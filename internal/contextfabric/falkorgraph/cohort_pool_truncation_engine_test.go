package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5168, END TO END: from the backend query that clipped the candidate
// pool to the cohort the COUNT STEP reads on the served document.
//
// WHY THIS TEST IS NOT IN package contextfabric. The chain under test starts
// at a real *Adapter over a fake connection and ends inside a real
// contextfabric.Engine. contextfabric cannot import falkorgraph (falkorgraph
// imports it), so the only package that can hold both halves unmocked is this
// one -- the same reason engine_org_isolation_test.go lives here.
//
// WHY IT IS NOT TWO TESTS. A reader-side test and an engine-side test, each
// green, would leave the seam between them untested and would not fail on the
// mutation this ticket's acceptance names ("delete the propagation"): the
// engine-side half would keep passing on a hand-built cohort. Here the cohort
// the engine carries is the one production discovery produced, so deleting the
// propagation at either end reddens THIS test.
//
// The intermediate stages are real: the engine plans, reads facts, groups,
// applies its stage-3 budget, ranks, and finalizes -- and finalization is
// where the count step runs. Each of those stages can rewrite Complete and
// Truncated (chaos4636_grouped_cohort.go's conjunction/disjunction,
// chaos4636_budget_stage3.go's narrowing), which is exactly why "the reader
// set the field" is not the same claim as "the count step saw it".

// committedAnchorGraphReader is the real adapter with ONE method replaced.
//
// ResolveSubjects is stubbed to hand back a committed subject because
// resolution's confidence machinery is not what this test is about, and
// driving it through a fake connection would make the fixture about lexical
// scores instead of about truncation. Everything the test actually asserts --
// DiscoverContext, the full-text query, the census gate, cohort assembly --
// is the production code path, reached through the embedded *Adapter.
//
// A COMMITTED subject is the point, not a convenience: censusAdmitted is
// `shapeAnchorEligible && len(request.Resolution.Committed) == 0`, so a
// committed anchor is precisely the production condition that skips the
// exhaustive census and leaves the full-text arm as the whole candidate pool.
// That is the condition the ticket names (reader.go's census gate), and it is
// the one under which the discarded truncation flag was the only evidence
// about completeness that existed.
type committedAnchorGraphReader struct {
	*Adapter
	committed contextfabric.SubjectRef
}

func (g committedAnchorGraphReader) ResolveSubjects(
	_ context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion,
	_ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection,
	_ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind,
) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{
		Candidates: []contextfabric.SubjectCandidate{},
		Committed:  []contextfabric.SubjectRef{g.committed},
	}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

// countingSynthesizer returns a complete answer so the engine reaches
// finalization, which is where the count step runs. A synthesizer that
// returned no_match would stop short of the stage under test.
type countingSynthesizer struct{}

func (countingSynthesizer) Synthesize(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	return contextfabric.InvestigationResult{
		Status: contextfabric.InvestigationComplete, DirectJudgment: "Counted.", CurrentState: "Nominal.",
		StrongestPressures: []string{}, Drivers: []contextfabric.DriverJudgment{},
		RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{},
		Paths: []contextfabric.RelationshipPath{}, Conflicts: []contextfabric.Finding{},
		Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []contextfabric.ClaimedFact{},
		Coverage:            contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "Counted.", Warnings: []string{},
		Versions: contextfabric.VersionSet{
			Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
		},
	}, nil
}

// discardingResultStore accepts every save and never returns a prior result,
// so nothing on the reuse path can supply a cohort this test did not discover.
type discardingResultStore struct{}

func (discardingResultStore) Save(context.Context, storage.Principal, contextfabric.InvestigationResult, contextfabric.SourceWatermarkSnapshot, contextfabric.RebuildEpoch, string, contextfabric.ReuseRetrievalIdentity, contextfabric.ReusePromptVersions, contextfabric.ReuseVersionAuthorities, int64, string) error {
	return nil
}

func (discardingResultStore) Get(context.Context, storage.Principal, string) (contextfabric.StoredInvestigationResult, error) {
	return contextfabric.StoredInvestigationResult{}, nil
}

// productionRequirementDeriver derives requirement rows through the
// PRODUCTION derivation rather than by hand-typing a `count` row, so a change
// to the obligation tables moves this test with them instead of leaving it
// asserting a row the product no longer derives.
type productionRequirementDeriver struct{}

func (productionRequirementDeriver) DeriveRequirements(frame contextfabric.QuestionFrame) []contextfabric.DerivedRequirement {
	return contextfabric.DeriveRequirements(frame, contextfabric.GenerateObligationSeed(nil), nil)
}

// countingCohortFrame is a COUNTING cohort question, built through
// DeriveFrameObligations rather than by listing obligations, so the `count`
// obligation this test depends on comes from the product's own tables.
func countingCohortFrame() *contextfabric.QuestionFrame {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	return &frame
}

// framedInterpreter is fixedInterpreter plus the VALIDATED FRAME.
//
// The frame is the input every seam-7 consumer reads -- the cohort kind, the
// census gate and the requirement derivation all come off this pointer -- so
// an interpreter that returns an empty outcome (as fixedInterpreter does)
// produces no cohort at all and no `count` obligation to derive. Carrying it
// here is what makes this a counting cohort investigation rather than an open
// question that happens to reach the graph.
type framedInterpreter struct {
	interpreted contextfabric.InterpretedQuestion
	frame       *contextfabric.QuestionFrame
}

func (f framedInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	return f.interpreted, contextfabric.QuestionFamilyOutcome{
		Frame:            f.frame,
		FrameObligations: f.frame.Obligations,
		Family:           contextfabric.QuestionFamilyScopedCohortStatus,
		Source:           contextfabric.QuestionFamilySourceModel,
	}, nil
}

// runCohortTruncationInvestigation drives one full investigation whose graph
// reader is the real adapter over a full-text result of `fulltextRowCount`
// rows, and returns the served document.
func runCohortTruncationInvestigation(t *testing.T, fulltextRowCount int) contextfabric.InvestigationResult {
	t.Helper()
	frame := countingCohortFrame()
	adapter := poolTruncationAdapter(t, fulltextRowCount, nil)
	graph := committedAnchorGraphReader{
		Adapter:   adapter,
		committed: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_anchor", Label: "Team Anchor"},
	}

	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: framedInterpreter{
			interpreted: contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
				TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				FactRequirements: []contextfabric.FactRequirement{},
			},
			frame: frame,
		},
		Graph:        graph,
		Facts:        emptyFactReader{},
		Synthesizer:  countingSynthesizer{},
		Results:      discardingResultStore{},
		Requirements: productionRequirementDeriver{},
	}, contextfabric.EngineOptions{
		ServiceVersion: "acr-test",
		NewResultID:    func() string { return "result_51680001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_51680001",
		Question: "which teams are struggling",
		TimeContext: contextfabric.TimeContext{
			Axis: contextfabric.TemporalCurrent,
			// A cohort investigation needs a CONFIRMED window before it
			// reaches assembly; an unconfirmed one stops at clarification,
			// which is a legitimate outcome and not what this test is about.
			EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
		},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != contextfabric.InvestigationComplete {
		t.Fatalf("status = %q, want %q -- this fixture never reached assembly, so it proves nothing about the count step",
			result.Status, contextfabric.InvestigationComplete)
	}
	return result
}

// servedCountRow returns the served document's assembled-result `count` row.
//
// It reads the SERVED document. A test that reached into the engine for the
// number would be measuring the engine's opinion of what it sent.
func servedCountRow(t *testing.T, result contextfabric.InvestigationResult) contextfabric.RequirementOutcomeRow {
	t.Helper()
	var found []contextfabric.RequirementOutcomeRow
	for _, row := range result.Completeness.Outcomes {
		if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult && row.Obligation == string(contextfabric.ObligationCount) {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("assembled-result `count` rows = %d, want exactly 1 -- the count step did not run on this fixture, so nothing below measures whether it saw the truncation", len(found))
	}
	return found[0]
}

// TestClippedFulltextPoolReachesTheCohortTheCountStepReads is the end-to-end
// proof, and it is the test the ticket's acceptance mutation targets.
//
// It fails at the parent for the reported harm: the served document states an
// exact count over a cohort that reports itself complete, while the search
// that produced that cohort's every member told the reader it had returned
// fewer matches than it found.
func TestClippedFulltextPoolReachesTheCohortTheCountStepReads(t *testing.T) {
	t.Parallel()
	result := runCohortTruncationInvestigation(t, poolTruncationFulltextCollectLimit+1)

	if result.Cohort == nil {
		t.Fatal("the served document carries no cohort -- the count step had nothing to count and this test measures nothing")
	}
	if len(result.Cohort.Members) != poolTruncationTeamCount {
		t.Fatalf("served members = %d, want %d -- the fixture's shape moved", len(result.Cohort.Members), poolTruncationTeamCount)
	}
	if len(result.Cohort.Members) >= 10 {
		t.Fatalf("served members (%d) reached MaxCohortMembers (10): the pre-existing cap disclosure would carry this test", len(result.Cohort.Members))
	}

	row := servedCountRow(t, result)
	if row.Served != poolTruncationTeamCount {
		t.Fatalf("count row served = %d, want %d -- the row does not describe the member set the document carries", row.Served, poolTruncationTeamCount)
	}

	// THE HARM. The count is exact over the resolved set and says so; the
	// only thing on the document that can stop a reader from taking it for a
	// population is the cohort's own truncation. Deleting the propagation in
	// reader.go or in DiscoveredCohort reds exactly here.
	if !result.Cohort.Truncated {
		t.Error("the served cohort reports Truncated=false after discovery clipped the pool it was built from -- " +
			"an exact count over that cohort is served as a census of the population, which is the defect")
	}
	if result.Cohort.Complete {
		t.Error("the served cohort reports Complete=true over a clipped candidate pool")
	}
}

// TestWholeFulltextPoolReachesTheCountStepAsACompleteCohort is the
// complement, end to end, on the same fixture one row smaller.
//
// Without it, "always report truncated" would pass the test above and would
// destroy the completeness claim of every cohort answer the product serves.
func TestWholeFulltextPoolReachesTheCountStepAsACompleteCohort(t *testing.T) {
	t.Parallel()
	result := runCohortTruncationInvestigation(t, poolTruncationFulltextCollectLimit)

	if result.Cohort == nil {
		t.Fatal("the served document carries no cohort")
	}
	if len(result.Cohort.Members) != poolTruncationTeamCount {
		t.Fatalf("served members = %d, want %d -- the two directions must carry the same members or they are not comparable", len(result.Cohort.Members), poolTruncationTeamCount)
	}
	row := servedCountRow(t, result)
	if row.Served != poolTruncationTeamCount {
		t.Fatalf("count row served = %d, want %d", row.Served, poolTruncationTeamCount)
	}
	if result.Cohort.Truncated {
		t.Error("the served cohort reports Truncated=true for a search that returned every match it found")
	}
	if !result.Cohort.Complete {
		t.Error("the served cohort reports Complete=false over a whole pool below the member cap")
	}
}
