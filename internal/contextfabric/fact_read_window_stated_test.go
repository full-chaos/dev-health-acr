package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12142. Every fact read carries the canonical window whatever its
// provenance (factReadQuestion), and the rollups read it either way. The
// investment read bounds its work units only by a window the caller stated,
// so the read also says whether the caller stated it.

func TestFactReadWindowStatedFollowsTheWindowProvenance(t *testing.T) {
	t.Parallel()
	start, end := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	window := func(provenance WindowProvenance) *contractsv1.ContextFabricEffectiveEvidenceWindow {
		return &contractsv1.ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end, RelativeID: RelativeWindowTrailing30D, Provenance: provenance}
	}
	cases := []struct {
		name   string
		window *contractsv1.ContextFabricEffectiveEvidenceWindow
		want   bool
	}{
		{"no window", nil, false},
		{"inferred_default", window(WindowInferredDefault), false},
		{"no provenance", window(""), false},
		{"question_stated", window(WindowQuestionStated), true},
		{"clarification_confirmed", window(WindowClarificationConfirmed), true},
	}
	for _, tc := range cases {
		if got := factReadWindowStated(tc.window); got != tc.want {
			t.Errorf("%s: factReadWindowStated = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// captureFactReadEngine is the acceptance engine with a fact reader that
// records each request it is given.
func captureFactReadEngine(t *testing.T, now time.Time, requests *[]CanonicalFactRequest) *Engine {
	t.Helper()
	project := acceptanceProject()
	facts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		*requests = append(*requests, request)
		return bootstrapFactBundle(project), nil
	})
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: bootstrapDraft(project), receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: &countingInterpreter{interpretation: bootstrapInterpretation()},
		Graph:       graph,
		Facts:       facts,
		Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acceptance-test", Backend: "graph"}},
		Results:     newMapResultStore(),
	}, EngineOptions{
		ServiceVersion: "acceptance-test",
		Now:            func() time.Time { return now },
		NewResultID:    func() string { return "result_acceptance01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func TestTheFactReadSaysWhetherTheCallerStatedItsWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		request    func() InvestigationRequest
		provenance WindowProvenance
		stated     bool
	}{
		{"window in the request", func() InvestigationRequest {
			request := validInvestigationRequest()
			request.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}
			return request
		}, WindowQuestionStated, true},
		{"period in the question", func() InvestigationRequest {
			request := validInvestigationRequest()
			request.Question = "Which teams need attention this month?"
			request.Consumer.Surface = "mcp"
			return request
		}, WindowQuestionStated, true},
		{"inferred window, clarification declined", func() InvestigationRequest {
			request := validInvestigationRequest()
			request.Consumer.Surface = "mcp"
			request.Options.AllowClarification = false
			return request
		}, WindowInferredDefault, false},
	}
	for _, tc := range cases {
		var requests []CanonicalFactRequest
		engine := captureFactReadEngine(t, now, &requests)
		result, err := engine.Investigate(context.Background(), acceptancePrincipal(), tc.request())
		if err != nil {
			t.Fatalf("%s: Investigate() error = %v", tc.name, err)
		}
		if result.EffectiveEvidenceWindow == nil || result.EffectiveEvidenceWindow.Provenance != tc.provenance {
			t.Fatalf("%s: EffectiveEvidenceWindow = %#v, want provenance %q", tc.name, result.EffectiveEvidenceWindow, tc.provenance)
		}
		if len(requests) == 0 {
			t.Fatalf("%s: no fact read was issued, so this case measures nothing", tc.name)
		}
		for index, request := range requests {
			if request.Question.TimeContext.EvidenceWindow == nil {
				t.Errorf("%s: fact read %d carries no window; every provenance reaches the read", tc.name, index)
			}
			if request.EvidenceWindowStated != tc.stated {
				t.Errorf("%s: fact read %d EvidenceWindowStated = %v, want %v (provenance %q)", tc.name, index, request.EvidenceWindowStated, tc.stated, tc.provenance)
			}
		}
	}
}

// The group-rooted read of a grouped turn carries the same flag as its
// member read: both read the turn's one window.
func TestTheGroupReadSaysWhetherTheCallerStatedItsWindow(t *testing.T) {
	t.Parallel()
	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	groupRooted := recorder.groupRootedRequests(SubjectTeam)
	if len(groupRooted) == 0 {
		t.Fatalf("fixture defect: no group-rooted read was issued, so this pin measures nothing")
	}
	for index, read := range recorder.requests {
		if read.Question.TimeContext.EvidenceWindow == nil || !read.EvidenceWindowStated {
			t.Errorf("fact read %d (root kinds %v): window = %v, EvidenceWindowStated = %v; want the stated window on every read of the turn", index, recorder.rootKinds(index), read.Question.TimeContext.EvidenceWindow, read.EvidenceWindowStated)
		}
	}
}

// The flag reaches the provider's FactQuery: buildFactQuery copies it.
func TestBuildFactQueryCarriesWhetherTheCallerStatedItsWindow(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	capability := FactCapability{
		Kind: FactMetrics, Name: "ops-metrics", Version: "metrics-v1",
		SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
	}
	for _, stated := range []bool{true, false} {
		request := canonicalFactRequest(project, FactMetrics)
		request.EvidenceWindowStated = stated
		query, err := buildFactQuery(request, FactRequirement{Kind: FactMetrics}, capability, investigationScopeSubjectSet(request), []SubjectRef{project})
		if err != nil {
			t.Fatalf("stated=%v: buildFactQuery() error = %v", stated, err)
		}
		if query.EvidenceWindowStated != stated {
			t.Errorf("FactQuery.EvidenceWindowStated = %v, want the request's %v", query.EvidenceWindowStated, stated)
		}
	}
}
