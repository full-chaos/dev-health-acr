package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// teamQuestionGraph commits one team and asks the fact read for the kinds a
// team status question plans.
type teamQuestionGraph struct {
	surfaceGraph
	requirements []cf.FactRequirement
}

func (g teamQuestionGraph) DiscoverContext(context.Context, storage.Principal, cf.GraphDiscoveryRequest) (cf.GraphContext, error) {
	return cf.GraphContext{FactRequirements: g.requirements, Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}}}, nil
}

// echoFactProvider answers one fact per queried subject, with the subject
// taken from the query, the way the pull request producer does.
type echoFactProvider struct {
	capability cf.FactCapability
	panics     bool
}

func (p echoFactProvider) Capability() cf.FactCapability { return p.capability }

func (p echoFactProvider) ReadFacts(_ context.Context, _ storage.Principal, query cf.FactQuery) (cf.FactProviderResult, error) {
	if p.panics {
		panic("provider broke")
	}
	facts := make([]cf.CanonicalFact, 0, len(query.Subjects))
	for _, subject := range query.Subjects {
		facts = append(facts, cf.CanonicalFact{Kind: p.capability.Kind, Subject: subject, Fields: map[string]cf.FactValue{"state": cf.StringFactValue("open")}})
	}
	return cf.FactProviderResult{State: cf.SourceAvailable, Facts: facts}, nil
}

// labelLessPullRequestExpander is the production expansion shape: pull
// request targets with no label, one more candidate than was admitted.
type labelLessPullRequestExpander struct{}

func (labelLessPullRequestExpander) ExpandFactScope(_ context.Context, request cf.FactScopeExpansionRequest) (cf.FactScopeExpansionResult, error) {
	if request.TargetKind != cf.SubjectPullRequest {
		return cf.FactScopeExpansionResult{}, nil
	}
	return cf.FactScopeExpansionResult{
		Targets: []cf.SubjectRef{
			{Kind: cf.SubjectPullRequest, CanonicalID: "pull_request:repo-a:1"},
			{Kind: cf.SubjectPullRequest, CanonicalID: "pull_request:repo-a:2"},
		},
		Counts: cf.FactScopeExpansionCounts{CandidateCount: 3, Truncated: true},
	}, nil
}

type bareErrorFactReader struct{ err error }

func (r bareErrorFactReader) ReadFacts(context.Context, storage.Principal, cf.CanonicalFactRequest) (cf.CanonicalFactBundle, error) {
	return cf.CanonicalFactBundle{}, r.err
}

func teamFactCapability(kind cf.FactKind, name string, subjectKind cf.SubjectKind) cf.FactCapability {
	return cf.FactCapability{
		Kind: kind, Name: name, Version: "v1", SupportedSubjectKinds: []cf.SubjectKind{subjectKind},
		Dimension: cf.HealthDimensionExecutionCompletion, SubjectRoles: []cf.FactRole{cf.FactRoleSubject},
	}
}

// teamQuestionModel drafts an answer that cites nothing and takes its status
// from the coverage the synthesis input carries: partial when the fact read
// reached it degraded, complete otherwise.
type teamQuestionModel struct{}

func (teamQuestionModel) InterpretQuestion(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InterpretedQuestion, cf.ModelExecutionReceipt, error) {
	return cf.InterpretedQuestion{}, cf.ModelExecutionReceipt{}, errors.New("the team question fixture interprets through surfaceInterpreter")
}

func (teamQuestionModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input cf.SynthesisInput) (cf.SynthesisDraft, cf.ModelExecutionReceipt, error) {
	draft := validRouteTestSynthesisDraft()
	if input.Facts.Coverage.Partial {
		draft.Status = cf.InvestigationPartial
	}
	draft.Drivers = []cf.DriverJudgment{}
	draft.DirectJudgment = "The Platform team has open work."
	draft.CurrentState = "Open work."
	draft.DeterministicAnswer = "The Platform team has open work."
	receipt := validRouteTestModelReceipt(cf.ModelOperationSynthesize)
	receipt.Outcome = "success"
	return draft, receipt, nil
}

func teamQuestionApp(t *testing.T, facts cf.CanonicalFactReader, logs *bytes.Buffer) (*App, string) {
	t.Helper()
	team := cf.SubjectRef{Kind: cf.SubjectTeam, CanonicalID: "team:PLATFORM", Label: "Platform"}
	store := rankingSurfaceStore{memoryinvestigation.NewStore()}
	next := 0
	engine, err := cf.NewEngine(cf.EngineDependencies{
		Telemetry:   cf.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(logs, nil))),
		Interpreter: surfaceInterpreter{},
		Graph: teamQuestionGraph{
			surfaceGraph: surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{team}}},
			requirements: []cf.FactRequirement{{Kind: cf.FactHealth}, {Kind: cf.FactPullRequests}, {Kind: cf.FactWorkload}},
		},
		Facts: facts, Results: store,
		Synthesizer: cf.RuntimeAnswerSynthesizer{Runtime: teamQuestionModel{}, Options: cf.RuntimeAnswerSynthesizerOptions{
			ServiceVersion: "acr-test", Backend: "graph", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
		}},
	}, cf.EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(700, 0).UTC() },
		NewResultID: func() string { next++; return fmt.Sprintf("result_team_question_%02d", next) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
}

func postTeamQuestion(t *testing.T, app *App, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := surfaceRequest("request_team_question")
	request.Question = "How is the Platform team doing?"
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "https://acr.example.test/api/v1/context-fabric/investigations", bytes.NewReader(payload))
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-ACR-Client-Version", "1.2.5")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httpRequest)
	return recorder
}

// The production failure: a team question whose pull request fact read is
// refused by the registry. The answer is served, partial, with the refused
// kind named in coverage.
func TestATeamQuestionWithARefusedFactReadAnswersPartial(t *testing.T) {
	logs := &bytes.Buffer{}
	// Every kind the engine plans for a team status question has a provider
	// that answers, so the one kind that degrades is the refused one.
	teamKinds := []cf.FactKind{cf.FactHealth, cf.FactWorkload, cf.FactFlow, cf.FactInvestment, cf.FactLandscape, cf.FactReadiness}
	providers := []cf.FactProvider{echoFactProvider{capability: teamFactCapability(cf.FactPullRequests, "pull_requests", cf.SubjectPullRequest)}}
	for _, kind := range teamKinds {
		providers = append(providers, echoFactProvider{capability: teamFactCapability(kind, string(kind), cf.SubjectTeam)})
	}
	registry, err := cf.NewFactCapabilityRegistry(providers, cf.FactRegistryOptions{ScopeExpander: labelLessPullRequestExpander{}, Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	app, token := teamQuestionApp(t, registry, logs)

	response := postTeamQuestion(t, app, token)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a refused fact read degrades the answer. body=%s\nlogs:\n%s", response.Code, response.Body.String(), logs.String())
	}
	var result cf.InvestigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served result is not contract-valid: %v", err)
	}
	if result.Status != cf.InvestigationPartial {
		t.Fatalf("status = %q, want %q", result.Status, cf.InvestigationPartial)
	}
	if !result.Coverage.Partial {
		t.Fatal("coverage.partial = false, want true")
	}
	states := map[string]cf.SourceState{}
	for _, source := range result.Coverage.Sources {
		states[source.Source] = source.State
	}
	if states["canonical_fact:pull_requests"] != cf.SourceUnavailable {
		t.Fatalf("coverage states = %v, want canonical_fact:pull_requests unavailable", states)
	}
	for _, kind := range teamKinds {
		if states["canonical_fact:"+string(kind)] != cf.SourceAvailable {
			t.Fatalf("coverage states = %v, want %s available: the kinds around the refused one are still read", states, kind)
		}
	}
	if len(result.Coverage.DegradedReasons) != 1 || !strings.HasPrefix(result.Coverage.DegradedReasons[0], "pull_requests: ") ||
		!strings.Contains(result.Coverage.DegradedReasons[0], "canonical fact provider returned a result that was rejected") {
		t.Fatalf("degraded reasons = %v, want exactly one, naming pull_requests as rejected", result.Coverage.DegradedReasons)
	}
	if strings.Contains(response.Body.String(), "pull_request:repo-a") {
		t.Fatalf("the served answer carries a subject from the refused result: %s", response.Body.String())
	}
	if strings.Contains(logs.String(), "context fabric investigation failed") {
		t.Fatalf("a served answer logged an investigation failure:\n%s", logs.String())
	}
	rejection := decodeLogLine(t, logs.String(), "context fabric fact result rejected")
	if rejection["kind"] != "pull_requests" || rejection["rejection_cause"] != "fact_subject_invalid" {
		t.Fatalf("rejection line = %v, want kind=pull_requests rejection_cause=fact_subject_invalid", rejection)
	}
}

// What still ends the investigation at the fact read is named. Nothing at
// that stage reaches the log as "unclassified".
func TestAFactReadThatEndsTheInvestigationIsNamed(t *testing.T) {
	panicking, err := cf.NewFactCapabilityRegistry([]cf.FactProvider{
		echoFactProvider{capability: teamFactCapability(cf.FactHealth, "health", cf.SubjectTeam), panics: true},
	}, cf.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name           string
		facts          cf.CanonicalFactReader
		wantStatus     int
		wantStage      string
		wantClassified string
	}{
		{name: "a bare error from the fact reader", facts: bareErrorFactReader{err: errors.New("fact capability health: subject is outside the discovered investigation set")},
			wantStatus: http.StatusInternalServerError, wantStage: "fact_read", wantClassified: "fact_read_aborted"},
		{name: "a provider panic", facts: panicking,
			wantStatus: http.StatusInternalServerError, wantStage: "unknown", wantClassified: "panic"},
		{name: "an unavailable dependency keeps its own class", facts: bareErrorFactReader{err: fmt.Errorf("%w: fact gate", cf.ErrUnavailable)},
			wantStatus: http.StatusServiceUnavailable, wantStage: "fact_read", wantClassified: "dependency_unavailable"},
		{name: "a rejected interpretation keeps its own class", facts: bareErrorFactReader{err: fmt.Errorf("%w: parameter is not allowed", cf.ErrInterpretationRejected)},
			wantStatus: http.StatusUnprocessableEntity, wantStage: "fact_read", wantClassified: "interpretation_rejected"},
		{name: "no investigation subjects keeps its own class", facts: bareErrorFactReader{err: fmt.Errorf("%w: canonical fact request", cf.ErrNoInvestigationSubjects)},
			wantStatus: http.StatusInternalServerError, wantStage: "fact_read", wantClassified: "no_investigation_subjects"},
		{name: "an exceeded deadline keeps its own class", facts: bareErrorFactReader{err: context.DeadlineExceeded},
			wantStatus: http.StatusGatewayTimeout, wantStage: "fact_read", wantClassified: "deadline_exceeded"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			app, token := teamQuestionApp(t, testCase.facts, logs)

			response := postTeamQuestion(t, app, token)

			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			entry := decodeFailureLog(t, logs.String())
			if got := entry["failure_stage"]; got != testCase.wantStage {
				t.Fatalf("failure_stage = %v, want %q", got, testCase.wantStage)
			}
			if got := entry["failure_classification"]; got != testCase.wantClassified {
				t.Fatalf("failure_classification = %v, want %q", got, testCase.wantClassified)
			}
			if _, present := entry["failure_error_type"]; present {
				t.Fatalf("failure_error_type = %v on a named class", entry["failure_error_type"])
			}
		})
	}
}
