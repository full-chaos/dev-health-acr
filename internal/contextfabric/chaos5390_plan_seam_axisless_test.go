package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// axislessPlanEngine builds a turn whose plan carries NO group axis -- a
// grouped family whose winning sample named no group kind -- over a cohort of
// the given kind.
func axislessPlanEngine(t *testing.T, cohortKind SubjectKind) (*Engine, InvestigationRequest) {
	t.Helper()
	cohort := &Cohort{Kind: cohortKind, Rationale: "kind census match", Complete: true, Members: []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
	}}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "project_status",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactMetrics}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: groupedFamilyInterpreter{interpretation: interpretation, groupKind: ""},
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts:    []CanonicalFact{},
				Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationPartial, DirectJudgment: "The evidence is thin.", CurrentState: "One project was discovered.",
				DeterministicAnswer: "One project was discovered.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
			}, nil
		}),
		Results: &resultStoreStub{}, Telemetry: &recordingTelemetry{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(300, 0).UTC() },
		NewResultID:    func() string { return "result_53900002" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_53900002"
	return engine, request
}

// TestAPlanWithNoGroupAxisIsNeverRefusedAsASelfGroup is the plan seam's
// ZERO cell, found by the input-domain table.
//
// I6 is "the group axis equals the member axis". A plan that has NO group
// axis cannot violate it, whatever its member kind is. The seam compared the
// two kinds bare, so a cohort that came back without a kind met an axis-less
// plan at "" == "" and the turn was refused with `frame_invariant_violated`
// -- an invariant about grouping, published on an answer that never grouped.
// Before this branch that comparison only cleared an already-empty axis, so
// the cell was harmless; once the seam refuses, it is a false refusal naming
// the wrong cause.
//
// What the cell does AFTER the seam is the base's behaviour, unchanged: the
// contract's own cohort bound rejects a kindless cohort at result validation
// (executed at origin/main: "cohort violates v1 bounds"). The seam must hand
// the cell to that owner, not pre-empt it with an I6 refusal.
func TestAPlanWithNoGroupAxisIsNeverRefusedAsASelfGroup(t *testing.T) {
	t.Parallel()

	engine, request := axislessPlanEngine(t, "")
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	t.Logf("axis-less plan over a kindless cohort: err=%v status=%q refusal_basis=%q", err, result.Status, result.RefusalBasis)
	if err != nil && !strings.Contains(err.Error(), "cohort violates v1 bounds") {
		t.Fatalf("Investigate() error = %v, want nil or the contract's own cohort-bound rejection the base returns for this cell", err)
	}
	if err == nil {
		t.Errorf("an axis-less plan over a kindless cohort returned a document -- the base rejects this cohort at result validation, and the seam must not change that owner's decision")
	}
	if result.RefusalBasis == contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Errorf("an axis-less plan was refused with %q -- a plan with no group axis cannot group a kind by itself, so the refusal names an invariant the turn never touched",
			result.RefusalBasis)
	}
}

// TestAnAxislessPlanOverAKindedCohortServes is the CONTROL: the same turn
// over a cohort that does carry a kind serves, at the parent and at the tip.
func TestAnAxislessPlanOverAKindedCohortServes(t *testing.T) {
	t.Parallel()

	engine, request := axislessPlanEngine(t, SubjectProject)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("CONTROL BROKEN: Investigate() error = %v", err)
	}
	if result.RefusalBasis != "" {
		t.Fatalf("CONTROL BROKEN: refusal_basis = %q on an ordinary axis-less turn", result.RefusalBasis)
	}
}
