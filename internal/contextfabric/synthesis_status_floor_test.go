package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func cutEmptyCohortGraph() GraphContext {
	graph := emptyAffirmationGraph()
	declared, served := 0, 0
	detail := CoverageDetail{
		DetailID: "cov-graph-01", Source: "context-fabric:graph",
		Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated, Degrading: true,
		Kind: SubjectDeployment, Declared: &declared, Served: &served,
		Raw: "kind_census_truncated:deployment:0:0",
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	graph.Coverage.Partial = true
	graph.Coverage.DegradedReasons = []string{detail.Raw}
	graph.Coverage.Details = []CoverageDetail{detail}
	return graph
}

func scriptedStatusEngine(t *testing.T, modelStatus InvestigationStatus, graph GraphContext, committed bool, facts ...CanonicalFactBundle) (*Engine, InvestigationRequest) {
	t.Helper()
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "release_readiness_and_drivers",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
	}
	resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	bases := CommitBasisSet{}
	if committed {
		resolution = SubjectResolution{
			Candidates: []SubjectCandidate{affirmationCandidate(ResolutionCommitted)},
			Committed:  []SubjectRef{affirmationSubject},
		}
		bases = provenCommitBases(affirmationSubject)
	}
	bundle := emptyAffirmationFacts()
	if len(facts) > 0 {
		bundle = facts[0]
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graphReaderStub{resolution: resolution, context: graph, bases: bases},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return bundle, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			draft := affirmationResult()
			draft.SubjectResolution = SubjectResolution{}
			draft.Status = modelStatus
			draft.Versions = VersionSet{
				Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
				InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
			}
			return draft, nil
		}),
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(100, 0).UTC() },
		NewResultID:    func() string { return "result_12345678" },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.Question = "which deployments belong to the project?"
	return engine, request
}

func TestServedStatusDoesNotFollowModelNoMatchOverCommittedSubjectAndCohortTerminal(t *testing.T) {
	served := map[InvestigationStatus]InvestigationStatus{}
	for _, modelStatus := range []InvestigationStatus{InvestigationDegraded, InvestigationNoMatch} {
		engine, request := scriptedStatusEngine(t, modelStatus, cutEmptyCohortGraph(), true)
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
		if err != nil {
			t.Fatalf("model %q: %v", modelStatus, err)
		}
		served[modelStatus] = result.Status
	}
	if served[InvestigationNoMatch] == InvestigationNoMatch {
		t.Fatalf("model no_match was served as no_match over a committed subject with a cohort terminal; served by model status: %v", served)
	}
	if served[InvestigationNoMatch] != served[InvestigationDegraded] {
		t.Fatalf("served status follows the model: %v", served)
	}
}

func TestServedStatusFloorEngineShapes(t *testing.T) {
	factsBundle := factsForSubject(affirmationSubject)
	cases := []struct {
		name      string
		graph     GraphContext
		facts     CanonicalFactBundle
		committed bool
		want      InvestigationStatus
	}{
		{"committed with cohort terminal", cutEmptyCohortGraph(), emptyAffirmationFacts(), true, InvestigationDegraded},
		{"committed with a read fact row", emptyAffirmationGraph(), factsBundle, true, InvestigationDegraded},
		{"committed, no rows, no terminal (no-data case)", emptyAffirmationGraph(), emptyAffirmationFacts(), true, InvestigationNoMatch},
		{"nothing committed with a terminal detail", cutEmptyCohortGraph(), emptyAffirmationFacts(), false, InvestigationNoMatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, request := scriptedStatusEngine(t, InvestigationNoMatch, tc.graph, tc.committed, tc.facts)
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("Investigate: %v", err)
			}
			if result.Status != tc.want {
				t.Fatalf("status = %q, want %q", result.Status, tc.want)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("served result invalid: %v", err)
			}
			withheld := hasLimitation(result.Limitations, synthesisNarrativeWithheldLimitation)
			if withheld != (tc.want != InvestigationNoMatch) {
				t.Fatalf("withheld limitation present = %v for status %q", withheld, result.Status)
			}
		})
	}
}

func TestApplyServerStatusFloorClauses(t *testing.T) {
	members := &Cohort{Members: []CohortMember{{Subject: affirmationSubject}}}
	committed := SubjectResolution{Committed: []SubjectRef{affirmationSubject}}
	base := func() InvestigationResult {
		r := affirmationResult()
		r.Status = InvestigationNoMatch
		r.SubjectResolution = committed
		return r
	}
	t.Run("members served floors to partial", func(t *testing.T) {
		graph := emptyAffirmationGraph()
		graph.Cohort = members
		r := base()
		out := applyServerStatusFloor(&r, graph, emptyAffirmationFacts())
		if out == nil || r.Status != InvestigationPartial || out.To != InvestigationPartial || out.Reason != SynthesisStatusOverrideNoMatchOverCohortOutcome || out.CommittedCount != 1 {
			t.Fatalf("status %q outcome %#v", r.Status, out)
		}
		if again := applyServerStatusFloor(&r, graph, emptyAffirmationFacts()); again != nil {
			t.Fatal("not idempotent")
		}
	})
	t.Run("nil result and uncommitted result are left alone", func(t *testing.T) {
		if applyServerStatusFloor(nil, cutEmptyCohortGraph(), emptyAffirmationFacts()) != nil {
			t.Fatal("floored nil")
		}
		r := base()
		r.SubjectResolution = SubjectResolution{}
		if applyServerStatusFloor(&r, cutEmptyCohortGraph(), factsForSubject(affirmationSubject)) != nil || r.Status != InvestigationNoMatch {
			t.Fatal("floored with nothing committed")
		}
	})
	t.Run("every terminal code floors, with the disclosure and partial coverage", func(t *testing.T) {
		for _, code := range []contractsv1.ContextFabricCoverageDetailCode{
			contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
			contractsv1.ContextFabricCoverageDetailGraphProjectDeploymentsUnlinked,
			contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization,
			contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated,
		} {
			graph := cutEmptyCohortGraph()
			graph.Coverage.Details[0].Code = code
			r := base()
			r.Coverage.Partial = false
			if applyServerStatusFloor(&r, graph, emptyAffirmationFacts()) == nil || r.Status != InvestigationDegraded {
				t.Fatalf("code %q did not floor", code)
			}
			if !r.Coverage.Partial || !hasLimitation(r.Limitations, synthesisNarrativeWithheldLimitation) {
				t.Fatalf("code %q: partial=%v limitations=%v", code, r.Coverage.Partial, r.Limitations)
			}
		}
	})
	t.Run("non-degrading terminal code does not floor", func(t *testing.T) {
		graph := cutEmptyCohortGraph()
		graph.Coverage.Details[0].Degrading = false
		r := base()
		if applyServerStatusFloor(&r, graph, emptyAffirmationFacts()) != nil || r.Status != InvestigationNoMatch {
			t.Fatal("floored on a non-degrading detail")
		}
	})
	t.Run("non-terminal degrading code does not floor", func(t *testing.T) {
		graph := cutEmptyCohortGraph()
		graph.Coverage.Details[0].Code = contractsv1.ContextFabricCoverageDetailGraphValidityUnbounded
		r := base()
		if applyServerStatusFloor(&r, graph, emptyAffirmationFacts()) != nil {
			t.Fatal("floored on a non-terminal code")
		}
	})
	t.Run("fact for another subject does not floor", func(t *testing.T) {
		other := affirmationSubject
		other.CanonicalID = other.CanonicalID + "-other"
		r := base()
		if applyServerStatusFloor(&r, emptyAffirmationGraph(), factsForSubject(other)) != nil {
			t.Fatal("floored on a fact of another subject")
		}
	})
	t.Run("a no_data fact row does not floor", func(t *testing.T) {
		bundle := factsForSubject(affirmationSubject)
		bundle.Facts[0].SourceState = SourceNoData
		r := base()
		if applyServerStatusFloor(&r, emptyAffirmationGraph(), bundle) != nil {
			t.Fatal("floored on a no_data row")
		}
	})
	t.Run("a stale fact row floors", func(t *testing.T) {
		bundle := factsForSubject(affirmationSubject)
		bundle.Facts[0].SourceState = SourceStale
		r := base()
		if applyServerStatusFloor(&r, emptyAffirmationGraph(), bundle) == nil || r.Status != InvestigationDegraded {
			t.Fatal("did not floor on a stale row")
		}
	})
	t.Run("population_truncated is an outcome-row cause, not a graph detail, and does not floor here", func(t *testing.T) {
		graph := cutEmptyCohortGraph()
		graph.Coverage.Details[0].Code = contractsv1.ContextFabricCoverageDetailPopulationTruncated
		r := base()
		if applyServerStatusFloor(&r, graph, emptyAffirmationFacts()) != nil {
			t.Fatal("floored on population_truncated")
		}
	})
	t.Run("refusal basis is left alone", func(t *testing.T) {
		r := base()
		r.RefusalBasis = "frame_invariant"
		if applyServerStatusFloor(&r, cutEmptyCohortGraph(), emptyAffirmationFacts()) != nil {
			t.Fatal("floored a refusal")
		}
	})
	t.Run("a non-no_match status is never touched", func(t *testing.T) {
		for _, status := range []InvestigationStatus{InvestigationComplete, InvestigationPartial, InvestigationDegraded, InvestigationClarificationRequired} {
			r := base()
			r.Status = status
			if applyServerStatusFloor(&r, cutEmptyCohortGraph(), emptyAffirmationFacts()) != nil || r.Status != status {
				t.Fatalf("touched %q", status)
			}
		}
	})
}
