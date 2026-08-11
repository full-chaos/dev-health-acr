package contextfabric

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type interpreterFunc func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error)

func (f interpreterFunc) Interpret(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
	return f(ctx, principal, request)
}

type graphReaderStub struct {
	resolution SubjectResolution
	context    GraphContext
}

func (g graphReaderStub) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion) (SubjectResolution, error) {
	return g.resolution, nil
}

func (g graphReaderStub) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return g.context, nil
}

type factReaderFunc func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error)

func (f factReaderFunc) ReadFacts(ctx context.Context, principal storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	return f(ctx, principal, request)
}

type synthesizerFunc func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error)

func (f synthesizerFunc) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	return f(ctx, principal, input)
}

type resultStoreStub struct {
	saved InvestigationResult
}

func (s *resultStoreStub) Save(_ context.Context, _ storage.Principal, result InvestigationResult) error {
	s.saved = result
	return nil
}

func (s *resultStoreStub) Get(context.Context, storage.Principal, string) (InvestigationResult, error) {
	return InvestigationResult{}, nil
}

func TestEngineInvestigatesNovelQuestionThroughComposableCapabilities(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	resolution := SubjectResolution{Committed: []SubjectRef{project}}
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "release_readiness_and_drivers", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}, {Kind: FactReadiness}},
	}
	store := &resultStoreStub{}
	var observedFactRequest CanonicalFactRequest
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(_ context.Context, _ storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
			if !strings.Contains(request.Question, "why can’t") {
				t.Fatalf("question = %q, want novel phrasing", request.Question)
			}
			return interpretation, nil
		}),
		Graph: graphReaderStub{
			resolution: resolution,
			context: GraphContext{
				FactRequirements: []FactRequirement{{Kind: FactBlockers}, {Kind: FactReadiness}},
				EvidenceRefIDs:   []string{"evidence_project_status"},
			},
		},
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			observedFactRequest = request
			return CanonicalFactBundle{Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			if len(input.Graph.Resolution.Committed) != 1 || input.Graph.Resolution.Committed[0] != project {
				t.Fatalf("synthesis subjects = %#v, want %#v", input.Graph.Resolution.Committed, project)
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Ask Dev is not ready to ship.",
				DeterministicAnswer: "Ask Dev is not ready to ship because release-readiness blockers remain.",
				Versions:            VersionSet{ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1"},
			}, nil
		}),
		Results: store,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(100, 0).UTC() }, NewResultID: func() string { return "result_12345678" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequest()
	request.Question = "Most of it is closed, so why can’t this thing actually ship?"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.ResultID != "result_12345678" {
		t.Fatalf("ResultID = %q", result.ResultID)
	}
	if result.Versions.ServiceVersion != "acr-test" {
		t.Fatalf("ServiceVersion = %q", result.Versions.ServiceVersion)
	}
	if !reflect.DeepEqual(result, store.saved) {
		t.Fatalf("saved result does not match returned result")
	}
	if !reflect.DeepEqual(observedFactRequest.Subjects, []SubjectRef{project}) {
		t.Fatalf("fact subjects = %#v", observedFactRequest.Subjects)
	}
	wantKinds := []FactKind{FactStatus, FactReadiness, FactBlockers}
	if got := factKinds(observedFactRequest.Requirements); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("fact requirement kinds = %#v, want %#v", got, wantKinds)
	}
}

func TestNewEngineRequiresAllCoreCapabilities(t *testing.T) {
	t.Parallel()

	_, err := NewEngine(EngineDependencies{}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_1" }})
	if err == nil || !strings.Contains(err.Error(), "requires interpreter, graph, facts, and synthesizer") {
		t.Fatalf("NewEngine() error = %v", err)
	}
}

func factKinds(requirements []FactRequirement) []FactKind {
	kinds := make([]FactKind, 0, len(requirements))
	for _, requirement := range requirements {
		kinds = append(kinds, requirement.Kind)
	}
	return kinds
}
