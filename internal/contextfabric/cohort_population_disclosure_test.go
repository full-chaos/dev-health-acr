package contextfabric

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// productionShapedGraph serves the cohort the way DiscoveredCohort does: capped
// at the caller's limit, the pool population only on GraphContext, never on the
// cohort itself.
type productionShapedGraph struct{ ownedProjectsGraph }

func (g productionShapedGraph) DiscoverContext(ctx context.Context, principal storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	graph, err := g.ownedProjectsGraph.DiscoverContext(ctx, principal, request)
	if graph.Cohort != nil {
		graph.Cohort.Population = 0
	}
	return graph, err
}

func TestCappedCohortServesThePopulationItReached(t *testing.T) {
	const population, cap = 13, 5
	engine := memberCutEngineAtCeiling(t, 0, nil, 0, 0, population, &recordingTelemetry{})
	engine.graph = productionShapedGraph{engine.graph.(ownedProjectsGraph)}
	engine.graph = productionShapedGraph{ownedProjectsGraph{engine.graph.(productionShapedGraph).capturingGraphReader, population}}
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxCohortMembers = cap
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_listing"}, request)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result.Cohort.Members); got != cap {
		t.Fatalf("served members = %d, want %d", got, cap)
	}
	if result.Cohort.Population != population {
		t.Errorf("served cohort population = %d, want %d", result.Cohort.Population, population)
	}
	cut := ""
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "5 of 13") {
			cut = limitation
		}
	}
	if cut == "" {
		t.Errorf("no served limitation states 5 of 13: %q", result.Limitations)
	}
}

func cohortCarryingEncode(orgID string, input SynthesisInput, maxBytes int) ([]byte, error) {
	return json.Marshal(map[string]any{"org_id": orgID, "cohort": input.Graph.Cohort})
}

func clientCohortPopulationTurn(t *testing.T, listed, population int, lowerBound bool) (InvestigationResult, string) {
	t.Helper()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "kind census match", Complete: false, Truncated: true}
	for i := 0; i < listed; i++ {
		id := fmt.Sprintf("team:T%02d", i)
		cohort.Members = append(cohort.Members, CohortMember{
			Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: id, Label: id}, Rank: i + 1,
			InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{fmt.Sprintf("evidence_team_%02d", i)},
		})
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: cohort, CohortPopulation: population, CohortPopulationLowerBound: lowerBound,
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: RuntimeAnswerSynthesizer{
			Runtime: &clientRuntime{},
			Options: RuntimeAnswerSynthesizerOptions{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
			ClientSynthesis: &ClientSynthesisAssembly{
				PromptVersion: "p", ModelOutputVersion: "o", SystemSHA256: hex.EncodeToString(sha256Sum("system")),
				Rules: []string{"rule"}, MaxBytes: clientTestMaxBytes, Encode: cohortCarryingEncode,
			},
		},
		Results: &resultStoreStub{}, Telemetry: &recordingTelemetry{},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_94270001" }})
	if err != nil {
		t.Fatal(err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_94270001"
	request.Question = "which teams are struggling?"
	request.SynthesisMode = SynthesisModeClient
	ctx, collected := WithSynthesisInputCollector(context.Background())
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatal(err)
	}
	bundle := collected()
	if bundle == nil {
		t.Fatal("no synthesis input was delivered")
	}
	return result, string(bundle.Input)
}

func TestClientSynthesisInputCarriesTheCohortPopulationAndTheServedAnswerStatesTheCut(t *testing.T) {
	result, input := clientCohortPopulationTurn(t, 5, 13, false)
	if !strings.Contains(input, `"population":13`) {
		t.Errorf("synthesis_input does not carry the cohort population 13: %s", input)
	}
	if result.Cohort.Population != 13 || len(result.Cohort.Members) != 5 {
		t.Errorf("served cohort has %d members and population %d, want 5 and 13", len(result.Cohort.Members), result.Cohort.Population)
	}
	found := false
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "5 of 13 members are listed") {
			found = true
			if strings.Contains(limitation, "team:") {
				t.Errorf("the cut limitation names a member: %q", limitation)
			}
		}
	}
	if !found {
		t.Errorf("no served limitation states 5 of 13: %q", result.Limitations)
	}
}

func TestACohortCutOnATruncatedPoolStatesALowerBound(t *testing.T) {
	result, _ := clientCohortPopulationTurn(t, 5, 13, true)
	if !result.Cohort.PopulationLowerBound {
		t.Error("population lower bound not served")
	}
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "5 of at least 13 members are listed") {
			return
		}
	}
	t.Errorf("no served limitation states 5 of at least 13: %q", result.Limitations)
}

func TestAnUncutCohortStatesNoPopulationAndNoCut(t *testing.T) {
	result, input := clientCohortPopulationTurn(t, 5, 5, false)
	if result.Cohort.Population != 0 || strings.Contains(input, `"population"`) {
		t.Errorf("an uncut cohort served a population: %d", result.Cohort.Population)
	}
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "Not every member is listed") {
			t.Errorf("an uncut cohort served a cut limitation: %q", limitation)
		}
	}
}

func TestTheCutSentenceNamesTheCapThatCutTheList(t *testing.T) {
	cohort := &Cohort{Kind: SubjectTeam, Population: 13, Members: make([]CohortMember, 5)}
	const requestCap = "the request's max_cohort_members limits the list"
	const itemBudget = "the server limits how many items one answer carries"
	for name, tc := range map[string]struct {
		narrowing []contractsv1.ContextFabricPlanNarrowing
		want      string
	}{
		"no narrowing step: the caller's cap": {nil, requestCap},
		"cardinality clamp to the item budget": {[]contractsv1.ContextFabricPlanNarrowing{
			{Stage: contractsv1.ContextFabricPlanNarrowingCardinality, Before: 50, After: 5},
		}, itemBudget},
		"synthesis input narrowing": {[]contractsv1.ContextFabricPlanNarrowing{
			{Stage: contractsv1.ContextFabricPlanNarrowingSynthesisInput, Before: 8, After: 5},
		}, itemBudget},
		"assembled result narrowing": {[]contractsv1.ContextFabricPlanNarrowing{
			{Stage: contractsv1.ContextFabricPlanNarrowingAssembledResult, Before: 8, After: 5},
		}, itemBudget},
	} {
		sentence, ok := cohortListedLimitation(cohort, tc.narrowing)
		if !ok || !strings.Contains(sentence, tc.want) {
			t.Errorf("%s: sentence %q (ok=%v), want it to name %q", name, sentence, ok, tc.want)
		}
	}
}
