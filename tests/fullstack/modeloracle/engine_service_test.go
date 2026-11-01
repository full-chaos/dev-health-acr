package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

// These tests run the oracle in service mode behind the REAL modelprovider runtime and the
// REAL contextfabric.Engine, in-process on loopback, with no Docker and no other network.
//
// REAL: the oracle's HTTP handlers (the same routes() the binary serves), modelprovider.New
// and the shared llm client it builds, genkitruntime (prompts, output schema, output checks),
// RuntimeQuestionInterpreter, RuntimeAnswerSynthesizer, contextfabric.Engine (bounds, draft
// validation, evidence closure, completeness, stage events).
// FAKE: the graph reader (one committed project), the canonical fact reader (one status fact
// with one evidence ref), and the absent result store. They stand in for FalkorDB and Dev
// Health; the model replies are never faked here.

const (
	planPath       = "testdata/service-plan.v1.json"
	statusQuestion = "What is the status of full-chaos/dev-health-web?"
	againQuestion  = "Again: what is the status of full-chaos/dev-health-web?"
)

type oracleHarness struct {
	srv     *server
	url     string
	runtime contextfabric.ModelRuntime
}

// startOracle serves plan on loopback and builds the real runtime against it.
func startOracle(t *testing.T, planJSON []byte) *oracleHarness {
	t.Helper()
	return startOracleWithTimeout(t, planJSON, 10*time.Second)
}

func startOracleWithTimeout(t *testing.T, planJSON []byte, modelTimeout time.Duration) *oracleHarness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, planJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := loadPlan(path)
	if err != nil {
		t.Fatalf("loadPlan: %v", err)
	}
	srv := &server{plan: plan, logDir: dir, activity: make(chan struct{}, 1)}
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	rt, err := modelprovider.New(context.Background(), modelprovider.Config{
		Provider: "oracle", BaseURL: ts.URL + "/v1/", Model: modelID, APIKey: llm.NewSecret("dummy-not-a-key"),
		Timeout: modelTimeout, MaxAttempts: 1, AllowInsecureBaseURL: true,
	})
	if err != nil {
		t.Fatalf("modelprovider.New: %v", err)
	}
	return &oracleHarness{srv: srv, url: ts.URL, runtime: rt}
}

func readPlan(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// mutatePlan decodes the example plan, lets edit change it, and encodes it again.
func mutatePlan(t *testing.T, edit func(plan map[string]any)) []byte {
	t.Helper()
	var plan map[string]any
	if err := json.Unmarshal(readPlan(t), &plan); err != nil {
		t.Fatal(err)
	}
	edit(plan)
	out, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func serviceQuestions(plan map[string]any) []any {
	return plan["service"].(map[string]any)["questions"].([]any)
}

func project() contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_dev_health_web", Label: "dev-health-web"}
}

type fakeGraph struct{}

func (fakeGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "service-mode-fake-graph", Epoch: 0}, nil
}

func (fakeGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{project()}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

func (fakeGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{
		DriverCandidates: []contextfabric.DriverJudgment{}, EvidenceRefIDs: []string{},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{project()}}},
		Coverage:         contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

type fakeFacts struct{}

func (fakeFacts) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{
		Facts: []contextfabric.CanonicalFact{{
			Kind: contextfabric.FactStatus, Subject: project(),
			Fields:         map[string]contextfabric.FactValue{"status": contextfabric.StringFactValue("in_progress")},
			EvidenceRefIDs: []string{"evidence_status_0001"}, SourceState: contextfabric.SourceAvailable, Source: "ops", SourceVersion: "v1",
		}},
		Coverage: contextfabric.Coverage{
			Sources:         []contextfabric.SourceObservation{{Source: "canonical_fact:status", State: contextfabric.SourceAvailable}},
			DegradedReasons: []string{},
		},
		Version: "ops-v1",
	}, nil
}

func (h *oracleHarness) engine(t *testing.T) *contextfabric.Engine {
	t.Helper()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: h.runtime},
		Graph:       fakeGraph{}, Facts: fakeFacts{},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: h.runtime, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "service-mode-test", Backend: "graph"}},
	}, contextfabric.EngineOptions{
		ServiceVersion: "service-mode-test",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_service01" },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

func investigationRequest(question string) contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     "request_12345678", Question: question,
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "service-mode-test", Version: "0.1.0", Surface: "workbench"},
	}
}

func principal() storage.Principal { return storage.Principal{OrgID: "org_service_mode"} }

func (h *oracleHarness) calls(t *testing.T) serviceSnapshot {
	t.Helper()
	res, err := http.Get(h.url + "/service/calls")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var snap serviceSnapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func requireReady(t *testing.T, result contextfabric.InvestigationResult) {
	t.Helper()
	if result.Status != contextfabric.InvestigationComplete && result.Status != contextfabric.InvestigationPartial {
		t.Fatalf("Status = %q, want complete or partial; limitations %v", result.Status, result.Limitations)
	}
	if strings.TrimSpace(result.DirectJudgment) == "" {
		t.Fatal("DirectJudgment is empty")
	}
	if len(result.Drivers) < 1 {
		t.Fatal("the answer has no driver")
	}
	if len(result.Limitations) < 1 {
		t.Fatal("the answer has no limitation")
	}
	if len(result.EvidenceRefIDs) < 1 {
		t.Fatal("the answer cites no evidence ref")
	}
	if reflect.ValueOf(result.Completeness).IsZero() {
		t.Fatal("Completeness is not stamped")
	}
	if reflect.ValueOf(result.Coverage).IsZero() {
		t.Fatal("Coverage is empty")
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served result does not validate: %v", err)
	}
}

// AC3: every plan entry reaches a terminal ready answer through the real engine, and the
// oracle saw exactly the calls that did it, so the test cannot pass without reaching it.
func TestEveryServicePlanEntryReachesAReadyAnswerThroughTheRealEngine(t *testing.T) {
	h := startOracle(t, readPlan(t))
	engine := h.engine(t)
	// The questions are named here, not read back from the plan, so a plan that loses or
	// misspells an entry is red instead of quietly smaller.
	questions := []string{statusQuestion, againQuestion}
	if got := len(serviceQuestions(mustDecode(t, readPlan(t)))); got != len(questions) {
		t.Fatalf("the plan holds %d entries, this test asks %d", got, len(questions))
	}
	asked := 0
	for _, question := range questions {
		asked++
		t.Run(question, func(t *testing.T) {
			result, err := engine.Investigate(context.Background(), principal(), investigationRequest(question))
			if err != nil {
				t.Fatalf("Investigate: %v", err)
			}
			requireReady(t, result)
			byOp := h.calls(t).ByQuestion[question]
			for _, op := range []string{opInterpret, opSynthesize} {
				if got := byOp[op]; got.Answered != 1 || got.Failed != 0 || got.Started != 1 {
					t.Fatalf("%s calls for %q = %+v, want exactly one answered", op, question, got)
				}
			}
		})
	}
	snap := h.calls(t)
	if asked < 2 || snap.Started != asked*2 || snap.Answered != snap.Started || snap.Failed != 0 || snap.InFlight != 0 {
		t.Fatalf("snapshot = %+v for %d questions, want %d answered calls and no other", snap, asked, asked*2)
	}
	if h.srv.requests != snap.Started {
		t.Fatalf("oracle saw %d requests, service answered %d: a call went around service mode", h.srv.requests, snap.Started)
	}
}

func mustDecode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Plant control: the engine itself refuses a reply that breaks a bound. It does not error:
// it serves a degraded answer, so the ready check in the AC3 test is what goes red when a
// plan reply breaks a bound.
func TestAServiceReplyThatBreaksABoundIsNotServedAsReady(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"driver title over its bound": func(p map[string]any) {
			q := serviceQuestions(p)[0].(map[string]any)
			q["synthesis"].(map[string]any)["driver"].(map[string]any)["title"] = strings.Repeat("x", contractsv1.ContextFabricDriverTitleMaxLength+1)
		},
		"requested_judgment over its bound": func(p map[string]any) {
			serviceQuestions(p)[0].(map[string]any)["interpretation"].(map[string]any)["requested_judgment"] = strings.Repeat("x", contractsv1.ContextFabricRequestedJudgmentMaxLength+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := startOracle(t, mutatePlan(t, edit))
			result, err := h.engine(t).Investigate(context.Background(), principal(), investigationRequest(statusQuestion))
			if err == nil && (result.Status == contextfabric.InvestigationComplete || result.Status == contextfabric.InvestigationPartial) {
				t.Fatalf("the engine served a %q answer from a reply that breaks a bound", result.Status)
			}
		})
	}
}

// AC2: an unknown question fails loudly with a named envelope, never a default answer.
func TestAQuestionWithNoPlanEntryFailsLoudlyWithANamedEnvelope(t *testing.T) {
	h := startOracle(t, readPlan(t))
	_, err := h.engine(t).Investigate(context.Background(), principal(), investigationRequest("A question the plan does not hold?"))
	if err == nil {
		t.Fatal("an unplanned question was answered")
	}
	snap := h.calls(t)
	if snap.Failed != 1 || snap.Answered != 0 || snap.Calls[0].Operation != opInterpret || !strings.Contains(snap.Calls[0].Reason, "no service plan entry") {
		t.Fatalf("snapshot = %+v, want one failed interpret call naming the missing entry", snap)
	}
	// The envelope on the wire.
	body := `{"model":"m","messages":[{"role":"system","content":"x conform to the following schema:\n\n` + "```" + `{\"properties\":{\"fact_requirements\":{}}}` + "```" + `"},{"role":"user","content":"{\"question\":\"nope\"}"}]}`
	content, _ := firstMessage(t, postChat(t, h.url, body))["content"].(string)
	const want = `{"schema_version":"fullstack_model_service_failure.v1","operation":"interpret","question":"nope","reason":"no service plan entry for this question (operation interpret)"}`
	if content != want {
		t.Fatalf("envelope = %s, want exactly %s", content, want)
	}
}

// The discriminator is pinned on the engine's real schemas: if an output type loses or
// renames its marker property, the engine's calls stop classifying and this goes red.
func TestOperationDiscriminatorMatchesTheEnginesRealSchemas(t *testing.T) {
	h := startOracle(t, readPlan(t))
	if _, err := h.engine(t).Investigate(context.Background(), principal(), investigationRequest(statusQuestion)); err != nil {
		t.Fatal(err)
	}
	ph, ok := h.runtime.(contextfabric.OfferPhrasingModelRuntime)
	if !ok {
		t.Fatal("the real runtime offers no phrasing call")
	}
	if _, _, err := ph.PhraseStructureOffers(context.Background(), principal(), contextfabric.StructureOfferPhrasingInput{
		RequestID: "request_12345678",
		Options:   []contextfabric.StructureOfferPhrasingOption{{OptionID: "option_0001", Member: "subject_kind", Kind: contractsv1.ContextFabricSubjectKind("repository"), Label: "Repository: full-chaos/dev-health-web"}},
	}); err != nil {
		t.Fatalf("phrasing: %v", err)
	}
	ops := map[string]int{}
	for _, c := range h.calls(t).Calls {
		if c.State != stateAnswered {
			t.Fatalf("call %+v did not classify and answer", c)
		}
		ops[c.Operation]++
	}
	if ops[opInterpret] != 1 || ops[opSynthesize] != 1 || ops[opPhrase] != 1 || len(ops) != 3 {
		t.Fatalf("operations = %v, want one each of interpret, synthesize, phrase", ops)
	}
}

func TestClassifyOperationRefusesWhatItCannotTellApart(t *testing.T) {
	fence := func(props string) string {
		return "x conform to the following schema:\n\n```{\"properties\":{" + props + "}}```"
	}
	cases := map[string]string{
		"no schema block": "plain system message",
		"unclosed":        "x conform to the following schema:\n\n```{",
		"not json":        "x conform to the following schema:\n\n```nope```",
		"no marker":       fence(`"other":{}`),
		"two markers":     fence(`"fact_requirements":{},"phrasings":{}`),
	}
	for name, system := range cases {
		if op, err := classifyOperation(system); err == nil {
			t.Errorf("%s classified as %q", name, op)
		}
	}
	if op, err := classifyOperation(fence(`"phrasings":{}`)); err != nil || op != opPhrase {
		t.Errorf("phrasings = %q, %v", op, err)
	}
}

// Offer phrasing is optional per entry. With none planned for the offered labels the call
// fails loudly and the engine falls back to structure, which is its own contract.
func TestOfferPhrasingIsAnsweredOnlyWhenPlannedAndFailsLoudlyOtherwise(t *testing.T) {
	h := startOracle(t, readPlan(t))
	ph := h.runtime.(contextfabric.OfferPhrasingModelRuntime)
	option := func(label string) contextfabric.StructureOfferPhrasingInput {
		return contextfabric.StructureOfferPhrasingInput{RequestID: "request_12345678", Options: []contextfabric.StructureOfferPhrasingOption{
			{OptionID: "option_0001", Member: "subject_kind", Kind: contractsv1.ContextFabricSubjectKind("repository"), Label: label}}}
	}
	draft, _, err := ph.PhraseStructureOffers(context.Background(), principal(), option("Repository: full-chaos/dev-health-web"))
	if err != nil || len(draft.Phrasings) != 1 || draft.Phrasings[0].OptionID != "option_0001" || draft.Phrasings[0].Phrasing != "The dev-health-web repository" {
		t.Fatalf("draft = %+v err = %v, want the planned phrasing for option_0001", draft, err)
	}
	if _, _, err := ph.PhraseStructureOffers(context.Background(), principal(), option("Label no plan holds")); err == nil {
		t.Fatal("an unplanned offer was phrased")
	}
	snap := h.calls(t)
	if snap.Answered != 1 || snap.Failed != 1 || !strings.Contains(snap.Calls[1].Reason, "no plan entry holds phrasing") {
		t.Fatalf("snapshot = %+v", snap)
	}
}

// The same question gets the same reply every time, from any instance: no per-question state.
func TestTheSameQuestionGetsTheSameReplyFromAnyOracleInstance(t *testing.T) {
	var seen []string
	for i := 0; i < 2; i++ {
		h := startOracle(t, readPlan(t))
		for j := 0; j < 2; j++ {
			interpreted, _, err := h.runtime.InterpretQuestion(context.Background(), principal(), investigationRequest(statusQuestion))
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(interpreted)
			seen = append(seen, string(encoded))
		}
	}
	for _, s := range seen[1:] {
		if s != seen[0] {
			t.Fatalf("replies differ:\n%s\n%s", seen[0], s)
		}
	}
}

// Delays are held side by side: five held calls run at once and the snapshot shows it.
func TestFiveDelayedCallsAreHeldAtOnceAndNotSerialised(t *testing.T) {
	const hold = 600
	plan := mutatePlan(t, func(p map[string]any) {
		base := serviceQuestions(p)[0].(map[string]any)
		var qs []any
		for i := 0; i < 5; i++ {
			raw, _ := json.Marshal(base)
			var q map[string]any
			_ = json.Unmarshal(raw, &q)
			q["question"] = fmt.Sprintf("Held %d: what is the status of full-chaos/dev-health-web?", i)
			q["delay_ms"] = map[string]any{"interpret": hold}
			qs = append(qs, q)
		}
		p["service"].(map[string]any)["questions"] = qs
	})
	h := startOracle(t, plan)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, _, err := h.runtime.InterpretQuestion(context.Background(), principal(), investigationRequest(fmt.Sprintf("Held %d: what is the status of full-chaos/dev-health-web?", i))); err != nil {
				t.Errorf("call %d: %v", i, err)
			}
		}(i)
	}
	peak := 0
	for time.Since(start) < hold*time.Millisecond/2+200*time.Millisecond && peak < 5 {
		if n := h.calls(t).InFlight; n > peak {
			peak = n
		}
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	if peak != 5 {
		t.Fatalf("peak in-flight = %d, want 5 held at once", peak)
	}
	if elapsed := time.Since(start); elapsed >= 3*hold*time.Millisecond {
		t.Fatalf("five delays of %d ms took %v: they were serialised", hold, elapsed)
	}
}

// A request that is cancelled during its delay ends quietly: the oracle records it and does
// not write an error, and it keeps serving.
func TestACancelledDelayEndsQuietly(t *testing.T) {
	plan := mutatePlan(t, func(p map[string]any) {
		serviceQuestions(p)[0].(map[string]any)["delay_ms"] = map[string]any{"interpret": 5000}
	})
	h := startOracle(t, plan)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, _, err := h.runtime.InterpretQuestion(ctx, principal(), investigationRequest(statusQuestion)); err == nil {
		t.Fatal("a cancelled call returned an answer")
	}
	deadline := time.Now().Add(3 * time.Second)
	for h.calls(t).Cancelled != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("snapshot = %+v, want one cancelled call", h.calls(t))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if snap := h.calls(t); snap.Failed != 0 || snap.Answered != 0 {
		t.Fatalf("snapshot = %+v, a cancelled call must not count as failed or answered", snap)
	}
	// The oracle still serves after a cancel.
	if res, err := http.Get(h.url + "/health"); err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("health after cancel: %v %v", res, err)
	}
}

// Stage events, by ORDER and not by clock (this test measures no milliseconds): when the
// oracle holds the interpret call in flight, the interpreting stage is already open (its start
// is already buffered in the sink) and has not completed. The hold is long and released by
// cancelling the request context after the assertions, so the test does not sleep through it.
// The engine code is not changed.
func TestTheInterpretingStageIsOpenBeforeAndWhileTheOracleHoldsTheInterpretCall(t *testing.T) {
	plan := mutatePlan(t, func(p map[string]any) {
		serviceQuestions(p)[0].(map[string]any)["delay_ms"] = map[string]any{"interpret": 600000}
	})
	h := startOracle(t, plan)
	engine := h.engine(t)
	sink := contextfabric.NewInvestigationStageSink(contextfabric.InvestigationStageSinkCapacity)
	ctx, release := context.WithCancel(contextfabric.WithInvestigationStageSink(context.Background(), sink))
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := engine.Investigate(ctx, principal(), investigationRequest(statusQuestion))
		done <- err
	}()
	deadline := time.Now().Add(30 * time.Second)
	for h.calls(t).InFlight != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the interpret call never reached the oracle")
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := h.calls(t)
	if snap.Answered != 0 || snap.Calls[0].Operation != opInterpret {
		t.Fatalf("snapshot = %+v, want the interpret call held and nothing answered", snap)
	}
	// The call is in flight now. The stage start must already be in the sink, no waiting.
	select {
	case first := <-sink.Transitions():
		if first.Stage != contractsv1.ContextFabricStageInterpreting || first.State != contractsv1.ContextFabricStageStarted {
			t.Fatalf("first transition = %+v, want interpreting started", first)
		}
	default:
		t.Fatal("the model call is in flight but the interpreting stage has not opened")
	}
	select {
	case extra := <-sink.Transitions():
		t.Fatalf("transition %+v during the hold: the hold is not inside the open interpreting stage", extra)
	default:
	}
	release()
	if err := <-done; err == nil {
		t.Fatal("a released hold still produced an answer")
	}
}

// Plan hygiene.
func TestAServicePlanIsValidatedAtLoad(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"duplicate question": func(p map[string]any) {
			qs := serviceQuestions(p)
			p["service"].(map[string]any)["questions"] = append(qs, qs[0])
		},
		"no interpretation": func(p map[string]any) { delete(serviceQuestions(p)[0].(map[string]any), "interpretation") },
		"negative delay": func(p map[string]any) {
			serviceQuestions(p)[0].(map[string]any)["delay_ms"] = map[string]any{"interpret": -1}
		},
		"unknown field": func(p map[string]any) { serviceQuestions(p)[0].(map[string]any)["default_answer"] = true },
	} {
		path := filepath.Join(t.TempDir(), "plan.json")
		if err := os.WriteFile(path, mutatePlan(t, edit), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadPlan(path); err == nil {
			t.Errorf("%s: plan loaded", name)
		}
	}
}

func postChat(t *testing.T, url, body string) map[string]any {
	t.Helper()
	res, err := http.Post(url+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func firstMessage(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	return response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
}

// A plan without a service section behaves exactly as before for a no-tools call, and a
// service plan leaves a call that offers tools to the OpenCode script.
func TestServiceModeNeverTouchesTheOpenCodePath(t *testing.T) {
	const noTools = `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"{\"question\":\"q\"}"}]}`
	const withTools = `{"model":"m","messages":[{"role":"user","content":"go"}],"tools":[{"type":"function","function":{"name":"acr_context_for_task"}}]}`

	plain := mutatePlan(t, func(p map[string]any) { delete(p, "service") })
	h := startOracle(t, plain)
	got := firstMessage(t, postChat(t, h.url, noTools))["content"]
	if want := `{"schema_version":"fullstack_model_failure.v1","reason":"context_for_task was not offered by the client"}`; got != want {
		t.Fatalf("no-tools call to an OpenCode plan = %v, want the unchanged failure text", got)
	}
	if snap := h.calls(t); snap.Started != 0 {
		t.Fatalf("an OpenCode plan recorded service calls: %+v", snap)
	}
	if err := h.srv.writeObservations(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(h.srv.logDir, "model-observations.json"))
	if bytes.Contains(raw, []byte(`"service"`)) {
		t.Fatal("an OpenCode plan's observations gained a service section")
	}

	h = startOracle(t, readPlan(t))
	calls, _ := firstMessage(t, postChat(t, h.url, withTools))["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("a service plan answered a tools call without the OpenCode tool call: %v", calls)
	}
	if name := calls[0].(map[string]any)["function"].(map[string]any)["name"]; name != "acr_context_for_task" {
		t.Fatalf("a service plan answered a tools call with %v, want the OpenCode tool call", name)
	}
	if snap := h.calls(t); snap.Started != 0 {
		t.Fatalf("a tools call was counted as a service call: %+v", snap)
	}
}
