package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// expansionCap is how many targets an expanded kind admits for a large team.
const expansionCap = 200

// teamExpander admits a fixed number of targets for every kind a team
// question expands, out of a larger candidate set.
type teamExpander struct{ targets int }

func (e teamExpander) ExpandFactScope(_ context.Context, request cf.FactScopeExpansionRequest) (cf.FactScopeExpansionResult, error) {
	targets := make([]cf.SubjectRef, 0, e.targets)
	for index := 0; index < e.targets; index++ {
		switch request.TargetKind {
		case cf.SubjectWorkItem:
			id := fmt.Sprintf("PLAT-%04d", index)
			targets = append(targets, cf.SubjectRef{Kind: cf.SubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:" + id, Label: id})
		case cf.SubjectPullRequest:
			targets = append(targets, cf.SubjectRef{Kind: cf.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:e1198fbc-1945-3717-05d8-eb78866b4e90:%d", index+1), Label: fmt.Sprintf("PR #%d", index+1)})
		case contractsv1.ContextFabricSubjectPullRequestReview:
			targets = append(targets, cf.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: fmt.Sprintf("pull_request_review.v2:e1198fbc-1945-3717-05d8-eb78866b4e90:%d:review-%d", index+1, index+1), Label: fmt.Sprintf("PR #%d review", index+1)})
		default:
			return cf.FactScopeExpansionResult{}, nil
		}
	}
	return cf.FactScopeExpansionResult{Targets: targets, Counts: cf.FactScopeExpansionCounts{CandidateCount: 10287, Truncated: true}}, nil
}

// producerShapedFactProvider answers one fact per queried subject with the
// fields, evidence reference and source metadata the ClickHouse producer of
// its kind emits.
type producerShapedFactProvider struct{ capability cf.FactCapability }

func (p producerShapedFactProvider) Capability() cf.FactCapability { return p.capability }

func (p producerShapedFactProvider) ReadFacts(_ context.Context, _ storage.Principal, query cf.FactQuery) (cf.FactProviderResult, error) {
	facts := make([]cf.CanonicalFact, 0, len(query.Subjects))
	for _, subject := range query.Subjects {
		title := "Carry the delivery signal for " + subject.Label + " through the weekly review and the release checklist"
		fields := map[string]cf.FactValue{}
		switch p.capability.Kind {
		case cf.FactIdentity:
			fields["id"], fields["title"] = cf.StringFactValue(subject.Label), cf.StringFactValue(title)
		case cf.FactWork:
			fields["title"] = cf.StringFactValue(title)
		case cf.FactActualCompletion:
			fields["completed"], fields["completed_at"] = cf.BooleanFactValue(true), cf.StringFactValue("2026-09-12T10:00:00Z")
		case cf.FactBlockers:
			fields["blocked_by_work_item_id"] = cf.StringFactValue("PLAT-9999")
		default:
			fields["state"] = cf.StringFactValue("open")
		}
		facts = append(facts, cf.CanonicalFact{
			Kind: p.capability.Kind, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{"acr:v1:" + string(subject.Kind) + ":" + subject.CanonicalID},
			Source:         "devhealthfacts." + string(p.capability.Kind), SourceVersion: "devhealthfacts.clickhouse.v21",
		})
	}
	return cf.FactProviderResult{State: cf.SourceAvailable, Facts: facts}, nil
}

// recordedModelProvider is an OpenAI-compatible endpoint that answers every
// chat completion with one synthesis draft and keeps each request body.
type recordedModelProvider struct {
	mu       sync.Mutex
	requests [][]byte
	baseURL  string
}

const teamSynthesisJSON = `{
	"status": "partial",
	"direct_judgment": "The Platform team has open work.",
	"current_state": "Open work.",
	"strongest_pressures": [], "drivers": [],
	"remaining_work": [], "readiness_gaps": [], "conflicts": [], "limitations": [],
	"evidence_ref_ids": [], "claimed_facts": [],
	"deterministic_answer": "The Platform team has open work.",
	"warnings": []
}`

func newRecordedModelProvider(t *testing.T) *recordedModelProvider {
	t.Helper()
	provider := &recordedModelProvider{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read model request: %v", err)
		}
		provider.mu.Lock()
		provider.requests = append(provider.requests, body)
		provider.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"id":"chatcmpl-test","object":"chat.completion","created":1760000000,"model":"gpt-5-nano",
			"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":41,"completion_tokens":17,"total_tokens":58}}`, teamSynthesisJSON)
	}))
	t.Cleanup(server.Close)
	provider.baseURL = server.URL + "/v1/"
	return provider
}

func (p *recordedModelProvider) bodies() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.requests...)
}

// expandedTeamKinds are the kinds a team question reaches through scope
// expansion, with the subject kind each is read over.
var expandedTeamKinds = []struct {
	kind    cf.FactKind
	subject cf.SubjectKind
}{
	{cf.FactIdentity, cf.SubjectWorkItem}, {cf.FactActualCompletion, cf.SubjectWorkItem}, {cf.FactWork, cf.SubjectWorkItem},
	{cf.FactBlockers, cf.SubjectWorkItem}, {cf.FactPullRequests, cf.SubjectPullRequest}, {cf.FactReviews, contractsv1.ContextFabricSubjectPullRequestReview},
}

// teamSynthesisApp serves a team question through the real engine, the real
// fact registry and the real answer synthesizer over the given model runtime.
// Each expanded kind is read over targets subjects.
func teamSynthesisApp(t *testing.T, runtime cf.ModelRuntime, sink cf.ModelReceiptSink, targets int, logs *bytes.Buffer) (*App, string) {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	requirements := []cf.FactRequirement{{Kind: cf.FactHealth}}
	providers := []cf.FactProvider{}
	for _, expanded := range expandedTeamKinds {
		requirements = append(requirements, cf.FactRequirement{Kind: expanded.kind})
		providers = append(providers, producerShapedFactProvider{capability: teamFactCapability(expanded.kind, string(expanded.kind), expanded.subject)})
	}
	for _, kind := range []cf.FactKind{cf.FactHealth, cf.FactWorkload, cf.FactFlow, cf.FactInvestment, cf.FactLandscape, cf.FactReadiness} {
		providers = append(providers, echoFactProvider{capability: teamFactCapability(kind, string(kind), cf.SubjectTeam)})
	}
	registry, err := cf.NewFactCapabilityRegistry(providers, cf.FactRegistryOptions{ScopeExpander: teamExpander{targets: targets}, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	team := cf.SubjectRef{Kind: cf.SubjectTeam, CanonicalID: "team:PLATFORM", Label: "Platform"}
	store := rankingSurfaceStore{memoryinvestigation.NewStore()}
	next := 0
	telemetry := cf.NewSlogEngineTelemetry(logger)
	engine, err := cf.NewEngine(cf.EngineDependencies{
		Telemetry:   telemetry,
		Interpreter: surfaceInterpreter{},
		Graph: teamQuestionGraph{
			surfaceGraph: surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{team}}},
			requirements: requirements,
		},
		Facts: registry, Results: store,
		Synthesizer: cf.RuntimeAnswerSynthesizer{Runtime: runtime, Sink: sink, Telemetry: telemetry, Options: cf.RuntimeAnswerSynthesizerOptions{
			ServiceVersion: "acr-test", Backend: "graph", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
		}},
	}, cf.EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(700, 0).UTC() },
		NewResultID: func() string { next++; return fmt.Sprintf("result_team_synthesis_%02d", next) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
}

// productionModelRuntime builds the model runtime the hosted composition
// builds, pointed at the recorded provider.
func productionModelRuntime(t *testing.T, provider *recordedModelProvider, logs *bytes.Buffer) cf.ModelRuntime {
	t.Helper()
	runtime, err := modelprovider.New(context.Background(), modelprovider.Config{
		Provider: modelprovider.DefaultProvider, BaseURL: provider.baseURL, Model: modelprovider.DefaultModel,
		APIKey: "sk-configured", Timeout: 10 * time.Second, MaxAttempts: 1, MaxTransportRetries: 0, AllowInsecureBaseURL: true,
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// promptFact is one canonical fact as the model was given it.
type promptFact struct {
	Kind    string `json:"kind"`
	Subject struct {
		CanonicalID string `json:"canonical_id"`
	} `json:"subject"`
}

// givenSynthesisInput returns the synthesis input one recorded model request
// carried, and the facts in it.
func givenSynthesisInput(t *testing.T, body []byte) (string, []promptFact) {
	t.Helper()
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode the model request: %v", err)
	}
	for _, message := range request.Messages {
		if message.Role != "user" {
			continue
		}
		var text string
		if err := json.Unmarshal(message.Content, &text); err != nil {
			var parts []struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(message.Content, &parts); err != nil {
				t.Fatalf("decode the user message: %v", err)
			}
			for _, part := range parts {
				text += part.Text
			}
		}
		var input struct {
			Facts []promptFact `json:"canonical_facts"`
		}
		if err := json.Unmarshal([]byte(text), &input); err != nil {
			t.Fatalf("the user message is not the synthesis input: %v", err)
		}
		return text, input.Facts
	}
	t.Fatal("the model request carries no user message")
	return "", nil
}

func logLines(t *testing.T, logs string, message string) []map[string]any {
	t.Helper()
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		entry := map[string]any{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["msg"] == message {
			entries = append(entries, entry)
		}
	}
	return entries
}

func decodeTeamAnswer(t *testing.T, response *httptest.ResponseRecorder, logs *bytes.Buffer) cf.InvestigationResult {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s\nlogs:\n%s", response.Code, response.Body.String(), logs.String())
	}
	var result cf.InvestigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served result is not contract-valid: %v", err)
	}
	if entries := logLines(t, logs.String(), "context fabric investigation failed"); len(entries) != 0 {
		t.Fatalf("a served answer logged an investigation failure: %v", entries)
	}
	return result
}

func hasLimitation(result cf.InvestigationResult, limitation string) bool {
	for _, stated := range result.Limitations {
		if stated == limitation {
			return true
		}
	}
	return false
}

// A team whose expanded facts are larger than the model input bound. The
// answer is served: the model is given a bounded part of every kind, and the
// answer says so.
func TestATeamQuestionLargerThanTheModelInputIsAnsweredWithBoundedFacts(t *testing.T) {
	logs := &bytes.Buffer{}
	provider := newRecordedModelProvider(t)
	app, token := teamSynthesisApp(t, productionModelRuntime(t, provider, logs), nil, expansionCap, logs)

	response := postTeamQuestion(t, app, token)

	result := decodeTeamAnswer(t, response, logs)
	if !result.Coverage.Partial {
		t.Fatal("coverage.partial = false, want true: the model was given part of the facts")
	}
	if !hasLimitation(result, contractsv1.ContextFabricSynthesisInputBoundedLimitation) {
		t.Fatalf("limitations = %q, want the bounded-input disclosure", result.Limitations)
	}
	requests := provider.bodies()
	if len(requests) == 0 {
		t.Fatal("the model was not called")
	}
	input, given := givenSynthesisInput(t, requests[0])
	if len(input) > genkitruntime.DefaultExchangeMaxInputBytes {
		t.Fatalf("the model was given %d bytes, bound %d", len(input), genkitruntime.DefaultExchangeMaxInputBytes)
	}
	subjects := map[string][]string{}
	for _, fact := range given {
		subjects[fact.Kind] = append(subjects[fact.Kind], fact.Subject.CanonicalID)
	}
	share := len(subjects[string(cf.FactIdentity)])
	if share == 0 || share >= expansionCap {
		t.Fatalf("identity facts given = %d, want a part of the %d read", share, expansionCap)
	}
	for _, expanded := range expandedTeamKinds {
		if got := len(subjects[string(expanded.kind)]); got != share {
			t.Fatalf("%s facts given = %d, want %d: every kind keeps the same share", expanded.kind, got, share)
		}
	}
	for index, subject := range subjects[string(cf.FactIdentity)] {
		if want := fmt.Sprintf("work_item.v2:00000000-0000-0000-0000-000000000000:PLAT-%04d", index); subject != want {
			t.Fatalf("identity fact %d is about %s, want %s: the first subjects in read order stay", index, subject, want)
		}
		if other := subjects[string(cf.FactActualCompletion)][index]; other != subject {
			t.Fatalf("actual_completion fact %d is about %s, identity about %s: kinds read over the same subjects keep the same subjects", index, other, subject)
		}
	}
	if got := subjects[string(cf.FactHealth)]; len(got) != 1 || got[0] != "team:PLATFORM" {
		t.Fatalf("health facts given = %v, want the team's own fact", got)
	}

	bounds := logLines(t, logs.String(), "context fabric synthesis input bounded")
	if len(bounds) != 1 {
		t.Fatalf("bound lines = %d, want 1:\n%s", len(bounds), logs.String())
	}
	bound := bounds[0]
	if bound["level"] != "WARN" || bound["outcome"] != "fitted" || bound["kinds_bounded"] != float64(len(expandedTeamKinds)) {
		t.Fatalf("bound line = %v, want WARN outcome=fitted kinds_bounded=%d", bound, len(expandedTeamKinds))
	}
	if bound["max_input_bytes"] != float64(genkitruntime.DefaultExchangeMaxInputBytes) || bound["input_bytes"].(float64) <= bound["max_input_bytes"].(float64) {
		t.Fatalf("bound line = %v, want input_bytes above max_input_bytes=%d: the unbounded input did not fit", bound, genkitruntime.DefaultExchangeMaxInputBytes)
	}
	read := 0.0
	for _, ledger := range logLines(t, logs.String(), "context fabric fact read") {
		read += ledger["facts"].(float64)
	}
	if bound["facts_read"] != read || bound["facts_given"] != float64(len(given)) {
		t.Fatalf("bound line = %v, want facts_read=%v (the fact read ledger) facts_given=%d (the model request)", bound, read, len(given))
	}
}

// A team whose facts fit the model input. Nothing is bounded and nothing says
// it was.
func TestATeamQuestionThatFitsTheModelInputIsGivenEveryFact(t *testing.T) {
	const targets = 3
	logs := &bytes.Buffer{}
	provider := newRecordedModelProvider(t)
	app, token := teamSynthesisApp(t, productionModelRuntime(t, provider, logs), nil, targets, logs)

	response := postTeamQuestion(t, app, token)

	result := decodeTeamAnswer(t, response, logs)
	if hasLimitation(result, contractsv1.ContextFabricSynthesisInputBoundedLimitation) {
		t.Fatalf("limitations = %q, want no bounded-input disclosure", result.Limitations)
	}
	if entries := logLines(t, logs.String(), "context fabric synthesis input bounded"); len(entries) != 0 {
		t.Fatalf("bound lines = %v, want none", entries)
	}
	requests := provider.bodies()
	if len(requests) == 0 {
		t.Fatal("the model was not called")
	}
	_, given := givenSynthesisInput(t, requests[0])
	read := 0.0
	for _, ledger := range logLines(t, logs.String(), "context fabric fact read") {
		read += ledger["facts"].(float64)
	}
	if read == 0 || float64(len(given)) != read {
		t.Fatalf("facts given = %d, facts read = %v: a fitting input carries every fact", len(given), read)
	}
}

// scriptedSynthesisModel answers the synthesis call with a fixed error, or
// with a valid draft when it has none.
type scriptedSynthesisModel struct {
	err     error
	receipt bool
}

func (scriptedSynthesisModel) InterpretQuestion(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InterpretedQuestion, cf.ModelExecutionReceipt, error) {
	return cf.InterpretedQuestion{}, cf.ModelExecutionReceipt{}, errors.New("the team question fixture interprets through surfaceInterpreter")
}

func (m scriptedSynthesisModel) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input cf.SynthesisInput) (cf.SynthesisDraft, cf.ModelExecutionReceipt, error) {
	if m.err == nil {
		return teamQuestionModel{}.SynthesizeAnswer(ctx, principal, input)
	}
	var receipt cf.ModelExecutionReceipt
	if m.receipt {
		receipt = validRouteTestModelReceipt(cf.ModelOperationSynthesize)
		receipt.Outcome = "invalid_output"
	}
	return cf.SynthesisDraft{}, receipt, m.err
}

type failingReceiptSink struct{}

func (failingReceiptSink) RecordModelExecution(context.Context, storage.Principal, cf.ModelExecutionReceipt) error {
	return errors.New("insert model receipt: connection reset")
}

// What ends the investigation at synthesis is named. Nothing at that stage
// reaches the log as "unclassified", and the classes a caller can retry keep
// their status.
func TestASynthesisThatEndsTheInvestigationIsNamed(t *testing.T) {
	cases := []struct {
		name           string
		model          scriptedSynthesisModel
		sink           cf.ModelReceiptSink
		wantStatus     int
		wantClassified string
		wantRetryable  bool
	}{
		{name: "a bare error from the model runtime", model: scriptedSynthesisModel{err: errors.New("generator closed")},
			wantStatus: http.StatusInternalServerError, wantClassified: "synthesis_aborted"},
		{name: "a model input that no bounding fits", model: scriptedSynthesisModel{err: &cf.ModelInputOverflow{Bytes: 900_000, MaxBytes: 524_288}},
			wantStatus: http.StatusInternalServerError, wantClassified: "model_input_too_large"},
		{name: "a receipt the sink refuses after a valid draft", model: scriptedSynthesisModel{}, sink: failingReceiptSink{},
			wantStatus: http.StatusInternalServerError, wantClassified: "model_receipt_unrecorded"},
		{name: "an invalid model output keeps its class when the receipt is also refused", model: scriptedSynthesisModel{err: fmt.Errorf("%w: provider status", cf.ErrModelOutput), receipt: true}, sink: failingReceiptSink{},
			wantStatus: http.StatusBadGateway, wantClassified: "model_output_invalid", wantRetryable: true},
		{name: "an unavailable model keeps its class", model: scriptedSynthesisModel{err: fmt.Errorf("%w: model generation failed", cf.ErrModelUnavailable)},
			wantStatus: http.StatusServiceUnavailable, wantClassified: "dependency_unavailable", wantRetryable: true},
		{name: "a rate-limited model keeps its class", model: scriptedSynthesisModel{err: fmt.Errorf("%w: provider status", cf.ErrModelRateLimited)},
			wantStatus: http.StatusTooManyRequests, wantClassified: "rate_limited", wantRetryable: true},
		{name: "an invalid model output keeps its class", model: scriptedSynthesisModel{err: fmt.Errorf("%w: provider status", cf.ErrModelOutput)},
			wantStatus: http.StatusBadGateway, wantClassified: "model_output_invalid", wantRetryable: true},
		{name: "a rejected draft keeps its class", model: scriptedSynthesisModel{err: fmt.Errorf("%w: %w: claim is not grounded", cf.ErrSynthesisRejected, cf.ErrModelOutput)},
			wantStatus: http.StatusUnprocessableEntity, wantClassified: "synthesis_rejected", wantRetryable: true},
		{name: "an exceeded deadline keeps its class", model: scriptedSynthesisModel{err: context.DeadlineExceeded},
			wantStatus: http.StatusGatewayTimeout, wantClassified: "deadline_exceeded", wantRetryable: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			app, token := teamSynthesisApp(t, testCase.model, testCase.sink, 3, logs)

			response := postTeamQuestion(t, app, token)

			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			var body struct {
				Error struct {
					Retryable bool `json:"retryable"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Retryable != testCase.wantRetryable {
				t.Fatalf("retryable = %v, want %v body=%s", body.Error.Retryable, testCase.wantRetryable, response.Body.String())
			}
			entry := decodeFailureLog(t, logs.String())
			if got := entry["failure_stage"]; got != "synthesis" {
				t.Fatalf("failure_stage = %v, want synthesis", got)
			}
			if got := entry["failure_classification"]; got != testCase.wantClassified {
				t.Fatalf("failure_classification = %v, want %q", got, testCase.wantClassified)
			}
			if _, present := entry["failure_error_type"]; present {
				t.Fatalf("failure_error_type = %v on a named class", entry["failure_error_type"])
			}
			if strings.Contains(logs.String(), "generator closed") || strings.Contains(logs.String(), "connection reset") {
				t.Fatalf("a failure line carries the cause's own text:\n%s", logs.String())
			}
		})
	}
}

// A model input that stays too large after the facts were bounded ends the
// investigation with both byte counts on the failure line and the bounding
// reported as exhausted.
func TestAModelInputThatNoBoundingFitsReportsItsSize(t *testing.T) {
	logs := &bytes.Buffer{}
	app, token := teamSynthesisApp(t, scriptedSynthesisModel{err: &cf.ModelInputOverflow{Bytes: 900_000, MaxBytes: 524_288}}, nil, 3, logs)

	response := postTeamQuestion(t, app, token)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 body=%s", response.Code, response.Body.String())
	}
	entry := decodeFailureLog(t, logs.String())
	if entry["failure_classification"] != "model_input_too_large" || entry["input_bytes"] != float64(900_000) || entry["max_input_bytes"] != float64(524_288) {
		t.Fatalf("failure line = %v, want model_input_too_large input_bytes=900000 max_input_bytes=524288", entry)
	}
	bounds := logLines(t, logs.String(), "context fabric synthesis input bounded")
	if len(bounds) != 1 || bounds[0]["outcome"] != "exhausted" || bounds[0]["level"] != "ERROR" {
		t.Fatalf("bound lines = %v, want one ERROR line with outcome=exhausted", bounds)
	}
	if bounds[0]["facts_given"].(float64) >= bounds[0]["facts_read"].(float64) || bounds[0]["passes"].(float64) < 1 {
		t.Fatalf("bound line = %v, want the facts reduced before the bounding gave up", bounds[0])
	}
}
