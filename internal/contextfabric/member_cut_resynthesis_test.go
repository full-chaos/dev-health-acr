package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	resynthMembers        = 13
	resynthCensusDeclared = 40
)

var resynthAnchor = SubjectRef{Kind: SubjectTeam, CanonicalID: "team_platform", Label: "Platform"}

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

// censusPathsGraph is the owned-projects listing with one relationship path
// from the anchor team to every member and a kind_census_truncated detail
// minted over the listed cohort, as the graph reader mints them.
type censusPathsGraph struct {
	ownedProjectsGraph
}

func (g censusPathsGraph) DiscoverContext(ctx context.Context, principal storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	graph, err := g.ownedProjectsGraph.DiscoverContext(ctx, principal, request)
	if err != nil {
		return graph, err
	}
	refs := []string{}
	for index, member := range graph.Cohort.Members {
		repository := SubjectRef{Kind: SubjectRepository, CanonicalID: fmt.Sprintf("repo_%03d", index), Label: fmt.Sprintf("repo %d", index)}
		for hop, ends := range [][2]SubjectRef{{resynthAnchor, member.Subject}, {member.Subject, repository}} {
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
	declared, served := resynthCensusDeclared, len(graph.Cohort.Members)
	reason := KindCensusTruncatedReason(SubjectProject, declared, served)
	detail := CoverageDetail{
		DetailID: "cov-graph-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
		Degrading: true, Kind: SubjectProject, Declared: &declared, Served: &served, Raw: reason,
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	graph.Coverage.Details = append(graph.Coverage.Details, detail)
	graph.Coverage.DegradedReasons = append(graph.Coverage.DegradedReasons, reason)
	return graph, nil
}

// resynthFacts is, per member, a workload and a health fact (the ranking's
// signals, so narration adds driver judgments) and factsPerMember status
// facts with one scalar field each, so the degraded composition claims them in
// member order.
func resynthFacts(members, factsPerMember int) []CanonicalFact {
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
		for f := 0; f < factsPerMember; f++ {
			facts = append(facts, fact(FactStatus, fmt.Sprintf("ref_fact_%02d_%d", m, f), map[string]FactValue{fmt.Sprintf("signal_%d", f): StringFactValue(fmt.Sprintf("value %d", f))}))
		}
	}
	return facts
}

type resynthFixture struct {
	modelCalls int
	inputs     []SynthesisInput
	telemetry  *recordingTelemetry
}

func (f *resynthFixture) engine(t *testing.T, factsPerMember int, maxBytes int64, reserve time.Duration, synthesizer AnswerSynthesizer) *Engine {
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
		Graph: censusPathsGraph{ownedProjectsGraph{&capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}, Details: []CoverageDetail{}}},
		}, resynthMembers}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			coverage := availableCoverage()
			coverage.DegradedReasons = []string{}
			return CanonicalFactBundle{Facts: resynthFacts(resynthMembers, factsPerMember), Coverage: coverage, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer:  synthesizer,
		Requirements: registryDeriver{},
		Telemetry:    f.telemetry,
	}, func() EngineOptions {
		options := budgetStageOptions(serverItemCeiling, reserve)
		options.MaxSerializedBytes = maxBytes
		return options
	}())
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func (f *resynthFixture) investigate(t *testing.T, engine *Engine) (InvestigationResult, error) {
	t.Helper()
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxCohortMembers = memberCutCallerCap
	return engine.Investigate(context.Background(), storage.Principal{OrgID: "org_listing"}, request)
}

func memberIDs(cohort *Cohort) []string {
	ids := []string{}
	if cohort == nil {
		return ids
	}
	for _, member := range cohort.Members {
		ids = append(ids, member.Subject.CanonicalID)
	}
	return ids
}

func TestResynthCalibration(t *testing.T) {
	for _, perMember := range []int{4} {
	for _, maxBytes := range []int64{0, 30000, 40000, 45000} {
		var fixture resynthFixture
		result, err := fixture.investigate(t, fixture.engine(t, perMember, maxBytes, 0, nil))
		t.Logf("maxBytes=%d", maxBytes)
		body, _ := json.Marshal(result)
		t.Logf("perMember=%d err=%v members=%v claims=%d paths=%d bytes=%d modelCalls=%d composes=%d cuts=%+v", perMember, err, memberIDs(result.Cohort), len(result.ClaimedFacts), len(result.Paths), len(body), fixture.modelCalls, len(fixture.inputs), fixture.telemetry.memberCuts)
		for _, n := range fixture.telemetry.planNarrowings {
			t.Logf("  narrowing %+v", n)
		}
		for _, n := range fixture.telemetry.claimDepthNarrowings {
			t.Logf("  claimdepth %+v", n)
		}
		_ = slices.Contains[[]string]
		_ = strings.Contains
	}
	}
}
