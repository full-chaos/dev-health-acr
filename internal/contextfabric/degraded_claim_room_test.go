package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const degradedCohortMembers = 13

var degradedCohortAnchor = SubjectRef{Kind: SubjectTeam, CanonicalID: "team_platform", Label: "Platform"}

// degradingComposer is a synthesizer whose model call always fails, so every
// answer is the production degraded composition (RuntimeAnswerSynthesizer).
// No model is called; the counters say how often each path ran and inputs
// keeps what each composition was given.
type degradingComposer struct {
	modelCalls *int
	inputs     *[]SynthesisInput
}

func (s degradingComposer) Synthesize(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
	*s.modelCalls++
	return InvestigationResult{}, &SynthesisFailure{Class: SynthesisFailureCallNotCompleted, Attempts: 2, Elapsed: 31 * time.Second, cause: errors.New("model call cancelled")}
}

func (s degradingComposer) ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, failure *SynthesisFailure) (InvestigationResult, error) {
	*s.inputs = append(*s.inputs, input)
	return RuntimeAnswerSynthesizer{}.ComposeDegraded(ctx, principal, input, failure)
}

// pathedProjectsGraph is the owned-projects listing with two relationship
// paths per member: from the anchor team to the member, and from the member
// to its repository.
type pathedProjectsGraph struct {
	ownedProjectsGraph
}

func (g pathedProjectsGraph) DiscoverContext(ctx context.Context, principal storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	graph, err := g.ownedProjectsGraph.DiscoverContext(ctx, principal, request)
	if err != nil {
		return graph, err
	}
	refs := []string{}
	for index, member := range graph.Cohort.Members {
		repository := SubjectRef{Kind: SubjectRepository, CanonicalID: fmt.Sprintf("repo_%03d", index), Label: fmt.Sprintf("repo %d", index)}
		for hop, ends := range [][2]SubjectRef{{degradedCohortAnchor, member.Subject}, {member.Subject, repository}} {
			ref := fmt.Sprintf("ref_path_%02d_%d", index, hop)
			graph.Paths = append(graph.Paths, RelationshipPath{
				PathID: fmt.Sprintf("path_member_%02d_%d", index, hop),
				Nodes:  []SubjectRef{ends[0], ends[1]},
				Edges: []RelationshipEdge{{
					Type: contractsv1.ContextFabricRelationshipRelatedTo, From: ends[0], To: ends[1],
					Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, EvidenceRefIDs: []string{ref},
				}},
				WhyRelevant:    "The project is reached from the team that owns it.",
				EvidenceRefIDs: []string{ref},
			})
			refs = append(refs, ref)
		}
		refs = append(refs, member.EvidenceRefIDs...)
	}
	graph.EvidenceRefIDs = refs
	return graph, nil
}

// degradedCohortFacts is, per member, a workload and a health fact (ranking
// signals, so the cohort carries ranked drivers and narration runs) and
// statusPerMember status facts with one scalar field each, which the degraded
// composition claims.
func degradedCohortFacts(members, statusPerMember int) []CanonicalFact {
	facts := []CanonicalFact{}
	for m := 0; m < members; m++ {
		id := fmt.Sprintf("project_%03d", m)
		subject := SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}
		fact := func(kind FactKind, ref string, fields map[string]FactValue) CanonicalFact {
			return CanonicalFact{Kind: kind, Subject: subject, Fields: fields, EvidenceRefIDs: []string{ref}, Source: "devhealthfacts." + string(kind), SourceVersion: "v1"}
		}
		facts = append(facts,
			fact(FactWorkload, fmt.Sprintf("ref_workload_%02d", m), map[string]FactValue{"forecast_p50_days": IntegerFactValue(int64(5 + 3*m))}),
			fact(FactHealth, fmt.Sprintf("ref_health_%02d", m), map[string]FactValue{"severity": StringFactValue([]string{"low", "elevated", "high"}[m%3])}),
		)
		for f := 0; f < statusPerMember; f++ {
			facts = append(facts, fact(FactStatus, fmt.Sprintf("ref_fact_%02d_%d", m, f), map[string]FactValue{fmt.Sprintf("signal_%d", f): StringFactValue(fmt.Sprintf("value %d", f))}))
		}
	}
	return facts
}

// degradedCohortFixture is the connector cohort listing on a deployment where
// the model call fails and the cohort retry is declined (no deadline
// reserve): 13 owned projects under a 50-member limit, every answer composed
// by the production degraded composer.
type degradedCohortFixture struct {
	modelCalls int
	inputs     []SynthesisInput
	telemetry  *recordingTelemetry
	// candidates are the subject candidates the resolution returns.
	candidates []SubjectCandidate
	// itemCeiling is the service item ceiling; zero is serverItemCeiling.
	itemCeiling int
}

func (f *degradedCohortFixture) engine(t *testing.T, maxBytes int64, reserve time.Duration, synthesizer AnswerSynthesizer) *Engine {
	t.Helper()
	f.telemetry = &recordingTelemetry{}
	if synthesizer == nil {
		synthesizer = degradingComposer{modelCalls: &f.modelCalls, inputs: &f.inputs}
	}
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"Platform"}, MemberKind: SubjectProject},
		},
		Temporal: TemporalIntentCurrent,
		Version:  QuestionFrameVersion,
	}
	validated := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}, {Kind: FactWorkload}, {Kind: FactHealth}}},
			outcome:     QuestionFamilyOutcome{Frame: &validated.Frame, Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel},
		},
		Graph: pathedProjectsGraph{ownedProjectsGraph{&capturingGraphReader{
			resolution: SubjectResolution{Candidates: append([]SubjectCandidate{}, f.candidates...), Committed: []SubjectRef{}},
			context: GraphContext{Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}, Details: []CoverageDetail{}}},
		}, degradedCohortMembers}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			coverage := availableCoverage()
			coverage.DegradedReasons = []string{}
			return CanonicalFactBundle{Facts: degradedCohortFacts(degradedCohortMembers, 4), Coverage: coverage, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer:  synthesizer,
		Requirements: registryDeriver{},
		Telemetry:    f.telemetry,
	}, func() EngineOptions {
		ceiling := serverItemCeiling
		if f.itemCeiling > 0 {
			ceiling = f.itemCeiling
		}
		options := budgetStageOptions(ceiling, reserve)
		options.MaxSerializedBytes = maxBytes
		return options
	}())
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func (f *degradedCohortFixture) investigate(t *testing.T, engine *Engine) (InvestigationResult, error) {
	t.Helper()
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxCohortMembers = memberCutCallerCap
	return engine.Investigate(context.Background(), storage.Principal{OrgID: "org_listing"}, request)
}

// The connector cohort listing composed degraded: the composition's claims and
// the drivers narration adds after it fit the item budget together, at every
// byte ceiling, and the answer is served. Narration spends its own grant after
// composition, so a composer that claimed that grant too overran the item
// budget on the first pass (73 items against 66) whatever the member count,
// and at a 30 KB ceiling, where no lever could bring both axes in, the answer
// was refused.
func TestADegradedCohortAnswerLeavesNarrationItsGrant(t *testing.T) {
	for _, maxBytes := range []int64{0, 30000, 40000, 45000} {
		t.Run(fmt.Sprintf("max_bytes_%d", maxBytes), func(t *testing.T) {
			var fixture degradedCohortFixture
			result, err := fixture.investigate(t, fixture.engine(t, maxBytes, 0, nil))
			if err != nil {
				t.Fatalf("err = %v, want the degraded answer served", err)
			}
			if fixture.modelCalls != 1 || result.Status != InvestigationDegraded {
				t.Fatalf("model calls = %d status = %q, want the one failed call and a degraded answer", fixture.modelCalls, result.Status)
			}
			if len(result.Drivers) == 0 || result.Cohort == nil || len(result.Cohort.Members) != degradedCohortMembers {
				t.Fatalf("drivers = %d cohort = %+v, want narrated drivers over all %d members", len(result.Drivers), result.Cohort, degradedCohortMembers)
			}
			if items := contractsv1.CountContextFabricResultItems(result).Budgeted(); items > 66 {
				t.Fatalf("served items = %d, want at most the 66-item plan ceiling", items)
			}
			for _, event := range fixture.telemetry.planNarrowings {
				if event.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult && event.MeasuredItems > event.MaxItems {
					t.Fatalf("first pass measured %d items against %d: the composition and narration overran the item budget together", event.MeasuredItems, event.MaxItems)
				}
			}
			for _, event := range fixture.telemetry.claimDepthNarrowings {
				if event.Overrun == contractsv1.ContextFabricBudgetOverrunItems {
					t.Fatalf("claim-depth lever ran on an items overrun: %+v", event)
				}
			}
		})
	}
}

// The degraded claim room reserves the narration grant only when narration
// will run (the cohort carries a ranked driver); a cohort with no ranked
// driver, or no cohort, leaves it to the claims.
func TestTheDegradedClaimRoomReservesTheNarrationGrantOnlyWhenNarrationRuns(t *testing.T) {
	const maxItems, members = 66, 13
	allocation := AllocateItems(AnswerPlan{Budget: AnswerPlanBudget{MaxItems: maxItems}}, 0, members)
	if allocation.NarrationGrant <= 0 {
		t.Fatalf("narration grant = %d: the fixture does not discriminate", allocation.NarrationGrant)
	}
	cohort := func(ranked bool) *Cohort {
		cohort := budgetStageCohort(members)
		if ranked {
			cohort.Members[0].Drivers = []CohortMemberDriver{{Signal: "workload", Value: 1, Weight: 1, WeightContributed: 1}}
		}
		return cohort
	}
	base := maxItems - members - 1
	candidates := []SubjectCandidate{{Subject: degradedCohortAnchor}, {Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_platform_ops"}}}
	for _, row := range []struct {
		name       string
		cohort     *Cohort
		candidates []SubjectCandidate
		want       int
	}{
		{"ranked", cohort(true), nil, base - allocation.NarrationGrant},
		{"ranked_with_candidates", cohort(true), candidates, base - allocation.NarrationGrant - len(candidates)},
		{"unranked", cohort(false), nil, base},
		{"no_cohort", nil, nil, base},
	} {
		input := SynthesisInput{Allocation: allocation, Graph: GraphContext{Cohort: row.cohort, Resolution: SubjectResolution{Candidates: row.candidates}}}
		if got := degradedClaimLimit(input); got != row.want {
			t.Errorf("%s: limit = %d, want %d", row.name, got, row.want)
		}
	}
}

// The claim room charges the subject candidates the served answer carries: the
// candidates the composer reads (input.Graph.Resolution) are the ones the
// answer serves (result.SubjectResolution), and with them charged the answer
// still fits.
func TestTheDegradedClaimRoomChargesTheCandidatesTheAnswerServes(t *testing.T) {
	fixture := degradedCohortFixture{candidates: []SubjectCandidate{
		{ReceiptID: "receipt_platform_1", Subject: degradedCohortAnchor, State: ResolutionProposed, MatchReasons: []string{"Label match."}, Confidence: 0.9, EvidenceRefIDs: []string{}},
		{ReceiptID: "receipt_platform_2", Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_platform_ops", Label: "Platform Ops"}, State: ResolutionProposed, MatchReasons: []string{"Label match."}, Confidence: 0.8, EvidenceRefIDs: []string{}},
	}}
	result, err := fixture.investigate(t, fixture.engine(t, 0, 0, nil))
	if err != nil {
		t.Fatalf("err = %v, want the degraded answer served", err)
	}
	ids := func(candidates []SubjectCandidate) []string {
		out := []string{}
		for _, candidate := range candidates {
			out = append(out, candidate.Subject.CanonicalID)
		}
		return out
	}
	served := ids(result.SubjectResolution.Candidates)
	if len(served) == 0 {
		t.Fatal("the answer serves no candidate: the row does not discriminate")
	}
	if len(fixture.inputs) == 0 {
		t.Fatal("the degraded composer never ran")
	}
	for index, input := range fixture.inputs {
		if got := ids(input.Graph.Resolution.Candidates); !slices.Equal(got, served) {
			t.Fatalf("composition %d read candidates %v, the answer serves %v", index, got, served)
		}
	}
	if items := contractsv1.CountContextFabricResultItems(result).Budgeted(); items > 66 {
		t.Fatalf("served items = %d with %d candidates, want at most 66", items, len(served))
	}
	for _, event := range fixture.telemetry.planNarrowings {
		if event.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult && event.MeasuredItems > event.MaxItems {
			t.Fatalf("first pass measured %d items against %d with %d candidates", event.MeasuredItems, event.MaxItems, len(served))
		}
	}
}
