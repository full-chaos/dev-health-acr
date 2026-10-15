package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The three model decision lines, certified from the bytes the real Runtime
// writes through a real slog JSON handler. The key sequences below are the
// shapes the lines carried before they were declared; they are literals so a
// declaration edit cannot move them.
var (
	interpretDecisionKeys = []string{
		"time", "level", "msg",
		"request_id", "org_id_hash", "operation", "outcome", "attempts", "fallback_used",
		"primary_failure_classification", "axis_source", "decoding_seed", "sample", "initial_sample", "redraws",
		"model_id", "model_version", "prompt_version", "interpretation_source",
		"attempts_total", "attempts_retried", "attempt_outcomes", "attempt_elapsed_ms",
		"fallback_attempts_total", "primary_provider", "primary_model_id", "primary_model_version",
	}
	synthesizeDecisionKeys = []string{
		"time", "level", "msg",
		"request_id", "org_id_hash", "operation", "outcome", "attempts", "fallback_used",
		"primary_failure_classification", "drivers", "findings", "claims", "evidence_refs",
		"model_id", "model_version", "prompt_version",
		"attempts_total", "attempts_retried", "attempt_outcomes", "attempt_elapsed_ms",
		"fallback_attempts_total", "primary_provider", "primary_model_id", "primary_model_version",
	}
	phraseDecisionKeys = []string{
		"time", "level", "msg",
		"request_id", "org_id_hash", "operation", "outcome", "attempts",
		"model_id", "model_version", "prompt_version",
		"attempts_total", "attempts_retried", "attempt_outcomes", "attempt_elapsed_ms",
	}
)

func certifyRuntime(t *testing.T, generator generator, override Config) (*Runtime, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	override.Logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runtime := mustRuntime(t, generator, override)
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	step := 0
	runtime.now = func() time.Time {
		step++
		return base.Add(time.Duration(step) * 137 * time.Millisecond)
	}
	return runtime, &buf
}

func decisionKeyOrder(t *testing.T, buf *bytes.Buffer, operation string) []string {
	t.Helper()
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var probe map[string]any
		if json.Unmarshal(raw, &probe) != nil || probe["msg"] != decisionEventMessage || probe["operation"] != operation {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		if _, err := dec.Token(); err != nil {
			t.Fatalf("token: %v", err)
		}
		var keys []string
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				t.Fatalf("token: %v", err)
			}
			keys = append(keys, tok.(string))
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				t.Fatalf("value: %v", err)
			}
		}
		return keys
	}
	t.Fatalf("no %q decision line in %s", operation, buf.String())
	return nil
}

func certifyDecision(t *testing.T, buf *bytes.Buffer, event eventspec.Event, want map[string]any) certify.Line {
	t.Helper()
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	result, err := certify.Certify(log, certify.Assertion{Event: event, Want: want})
	if err != nil {
		t.Fatalf("%s failed certification: %v\n%s", event.ID, err, buf.String())
	}
	return result.Line
}

func requireAbsent(t *testing.T, line certify.Line, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, present := line[key]; present {
			t.Errorf("key %q is present, want it absent when its condition does not hold", key)
		}
	}
}

func requireElapsedAdvanced(t *testing.T, line certify.Line) {
	t.Helper()
	elapsed, _ := line["attempt_elapsed_ms"].(string)
	if elapsed == "" {
		t.Fatalf("attempt_elapsed_ms is empty")
	}
	for _, entry := range strings.Split(elapsed, ",") {
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 || parts[1] == "0" {
			t.Fatalf("attempt_elapsed_ms entry %q must carry a non-zero value under an advancing clock (%q)", entry, elapsed)
		}
	}
}

func TestInterpretDecisionLineCertifiesAcrossTheModelShapes(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	identity := map[string]any{"request_id": "request_12345678", "operation": "interpret"}
	want := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range identity {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	t.Run("legitimate zero: served first try, no fallback, no rejection", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &generatorStub{interpretation: validInterpretationOutput()}, Config{})
		if _, _, err := runtime.InterpretQuestion(context.Background(), principal, validRequest()); err != nil {
			t.Fatalf("InterpretQuestion() error = %v", err)
		}
		if got := decisionKeyOrder(t, buf, "interpret"); !reflect.DeepEqual(got, interpretDecisionKeys) {
			t.Fatalf("interpret key sequence = %v, want %v", got, interpretDecisionKeys)
		}
		line := certifyDecision(t, buf, eventspec.ModelInterpretDecision, want(map[string]any{
			"outcome": "success", "fallback_used": false, "attempts_total": 1, "attempts_retried": 0,
			"fallback_attempts_total": 0, "model_id": "test/model", "primary_model_id": "test/model",
			"attempt_outcomes": "1:success",
		}))
		requireAbsent(t, line, "rejection_reason", "rejected_fact_kind")
		requireElapsedAdvanced(t, line)
	})

	t.Run("primary failure then fallback success keeps both identities", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &generatorStub{interpretErr: errors.New("503 unavailable")}, Config{
			Fallback: fallbackRuntime{interpreted: validInterpretedQuestion(), draft: validDraft()},
		})
		if _, _, err := runtime.InterpretQuestion(context.Background(), principal, validRequest()); err != nil {
			t.Fatalf("InterpretQuestion() error = %v", err)
		}
		line := certifyDecision(t, buf, eventspec.ModelInterpretDecision, want(map[string]any{
			"outcome": "fallback", "fallback_used": true,
			"model_id": "deterministic", "model_version": "v1",
			"primary_provider": "test-provider", "primary_model_id": "test/model", "primary_model_version": "test-model-v1",
			"primary_failure_classification": "unavailable", "attempts_total": 1, "attempt_outcomes": "1:unavailable",
			"fallback_attempts_total": 1,
		}))
		requireAbsent(t, line, "rejection_reason", "rejected_fact_kind")
		requireElapsedAdvanced(t, line)
	})

	t.Run("terminal failure without a fallback", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &generatorStub{interpretErr: errors.New("503 unavailable")}, Config{})
		if _, _, err := runtime.InterpretQuestion(context.Background(), principal, validRequest()); err == nil {
			t.Fatal("InterpretQuestion() error = nil, want the terminal failure")
		}
		line := certifyDecision(t, buf, eventspec.ModelInterpretDecision, want(map[string]any{
			"outcome": "unavailable", "fallback_used": false, "primary_model_id": "test/model",
			"attempt_outcomes": "1:unavailable",
		}))
		requireAbsent(t, line, "rejection_reason", "rejected_fact_kind")
	})

	t.Run("returned before a receipt exists still writes a certifiable line", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &generatorStub{interpretation: validInterpretationOutput()}, Config{})
		if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{}, validRequest()); err == nil {
			t.Fatal("InterpretQuestion() error = nil, want the unauthenticated refusal")
		}
		line := certifyDecision(t, buf, eventspec.ModelInterpretDecision, map[string]any{"request_id": "request_12345678", "operation": ""})
		requireAbsent(t, line, "rejection_reason", "rejected_fact_kind")
		if got := decisionKeyOrder(t, buf, ""); !reflect.DeepEqual(got, interpretDecisionKeys) {
			t.Fatalf("pre-receipt interpret key sequence = %v, want %v", got, interpretDecisionKeys)
		}
	})

	t.Run("rejection names its rule, and a rejected fact kind rides with it", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &generatorStub{interpretation: func() interpretationOutput {
			invalid := validInterpretationOutput()
			invalid.FactRequirements = []factRequirementOutput{{Kind: "invented_fact_kind_not_in_registry"}}
			return invalid
		}()}, Config{})
		if _, _, err := runtime.InterpretQuestion(context.Background(), principal, validRequest()); err == nil {
			t.Fatal("InterpretQuestion() error = nil, want a rejection")
		}
		line := certifyDecision(t, buf, eventspec.ModelInterpretDecision, want(map[string]any{
			"outcome": "invalid_output", "rejection_reason": "fact_requirement_kind_invalid",
			"rejected_fact_kind": "invented_fact_kind_not_in_registry",
		}))
		requireElapsedAdvanced(t, line)
		if got := decisionKeyOrder(t, buf, "interpret"); !reflect.DeepEqual(got, append(append([]string{}, interpretDecisionKeys...), "rejection_reason", "rejected_fact_kind")) {
			t.Fatalf("rejected interpret key sequence = %v", got)
		}
	})
}

func TestSynthesizeDecisionLineCertifiesAcrossTheModelShapes(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	want := func(extra map[string]any) map[string]any {
		out := map[string]any{"request_id": "request_12345678", "operation": "synthesize"}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	t.Run("served draft carries draws and a measured-zero fact group", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &generatorStub{synthesis: validSynthesisOutput()}, Config{})
		if _, _, err := runtime.SynthesizeAnswer(context.Background(), principal, validSynthesisInput()); err != nil {
			t.Fatalf("SynthesizeAnswer() error = %v", err)
		}
		keys := decisionKeyOrder(t, buf, "synthesize")
		if !reflect.DeepEqual(keys[:len(synthesizeDecisionKeys)], synthesizeDecisionKeys) {
			t.Fatalf("synthesize base key sequence = %v, want prefix %v", keys, synthesizeDecisionKeys)
		}
		line := certifyDecision(t, buf, eventspec.ModelSynthesizeDecision, want(map[string]any{
			"outcome": "success", "fallback_used": false, "fallback_attempts_total": 0,
			"attempts_retried": 0, "model_id": "test/model", "primary_model_id": "test/model",
		}))
		requireAbsent(t, line, "rejection_reason")
		requireElapsedAdvanced(t, line)
	})

	t.Run("primary failure then fallback success keeps both identities and carries no draw", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &generatorStub{synthesisErr: errors.New("503 unavailable")}, Config{
			Fallback: fallbackRuntime{draft: validDraft()},
		})
		if _, _, err := runtime.SynthesizeAnswer(context.Background(), principal, validSynthesisInput()); err != nil {
			t.Fatalf("SynthesizeAnswer() error = %v", err)
		}
		if got := decisionKeyOrder(t, buf, "synthesize"); !reflect.DeepEqual(got[:len(synthesizeDecisionKeys)], synthesizeDecisionKeys) {
			t.Fatalf("synthesize key sequence = %v", got)
		}
		line := certifyDecision(t, buf, eventspec.ModelSynthesizeDecision, want(map[string]any{
			"outcome": "fallback", "fallback_used": true,
			"model_id": "deterministic", "model_version": "v1",
			"primary_provider": "test-provider", "primary_model_id": "test/model", "primary_model_version": "test-model-v1",
			"primary_failure_classification": "unavailable", "attempt_outcomes": "1:unavailable",
			"fallback_attempts_total": 1,
		}))
		requireAbsent(t, line, "draws_total", "draw_outcomes", "rejection_reason")
		requireElapsedAdvanced(t, line)
	})

	t.Run("terminal transport failure carries no draw fields", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &generatorStub{synthesisErr: errors.New("503 unavailable")}, Config{})
		if _, _, err := runtime.SynthesizeAnswer(context.Background(), principal, validSynthesisInput()); err == nil {
			t.Fatal("SynthesizeAnswer() error = nil, want the terminal failure")
		}
		if got := decisionKeyOrder(t, buf, "synthesize"); !reflect.DeepEqual(got, synthesizeDecisionKeys) {
			t.Fatalf("terminal-failure key sequence = %v, want %v", got, synthesizeDecisionKeys)
		}
		line := certifyDecision(t, buf, eventspec.ModelSynthesizeDecision, want(map[string]any{"outcome": "unavailable", "fallback_used": false}))
		requireAbsent(t, line, "draws_total", "draws_retried", "draw_outcomes", "draw_output_digests", "draw_rejected_clauses",
			"draw_rejected_reasons", "resynthesis_deadline_remaining_ms", "rejection_reason", "fact_group_size", "grounded_beyond_first")
	})

	t.Run("returned before a receipt exists still writes a certifiable line", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &generatorStub{synthesis: validSynthesisOutput()}, Config{})
		if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{}, validSynthesisInput()); err == nil {
			t.Fatal("SynthesizeAnswer() error = nil, want the unauthenticated refusal")
		}
		line := certifyDecision(t, buf, eventspec.ModelSynthesizeDecision, want(map[string]any{"operation": ""}))
		requireAbsent(t, line, "draws_total", "rejection_reason", "fact_group_size")
		if got := decisionKeyOrder(t, buf, ""); !reflect.DeepEqual(got, synthesizeDecisionKeys) {
			t.Fatalf("pre-receipt synthesize key sequence = %v, want %v", got, synthesizeDecisionKeys)
		}
	})

	t.Run("rejection names its rule", func(t *testing.T) {
		t.Parallel()
		output := validSynthesisOutput()
		output.Status = ""
		output.Drivers = nil
		output.ClaimedFacts = nil
		runtime, buf := certifyRuntime(t, &generatorStub{synthesis: output}, Config{})
		if _, _, err := runtime.SynthesizeAnswer(context.Background(), principal, validSynthesisInput()); err == nil {
			t.Fatal("SynthesizeAnswer() error = nil, want a rejection")
		}
		line := certifyDecision(t, buf, eventspec.ModelSynthesizeDecision, want(map[string]any{
			"outcome": "invalid_output", "rejection_reason": string(contextfabric.RejectionReasonStatusInvalid),
		}))
		if _, present := line["draws_total"]; !present {
			t.Error("a judged-and-rejected draft must carry its draw fields")
		}
	})
}

func TestSynthesizeDecisionLineCertifiesTheConditionalGroups(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	want := func(extra map[string]any) map[string]any {
		out := map[string]any{"request_id": "request_12345678", "operation": "synthesize"}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	t.Run("a widened fact group reports its size and a measured grounded_beyond_first", func(t *testing.T) {
		t.Parallel()
		input := validSynthesisInput()
		sparse := input.Facts.Facts[0]
		sparse.Fields = map[string]contextfabric.FactValue{"backlog_size": contextfabric.IntegerFactValue(24)}
		input.Facts.Facts = append([]contextfabric.CanonicalFact{sparse}, input.Facts.Facts...)
		output := validSynthesisOutput()
		output.ClaimedFacts = []contextfabric.ClaimedFact{groundedReadinessClaim(input)}
		runtime, buf := certifyRuntime(t, &generatorStub{synthesis: output}, Config{})
		if _, _, err := runtime.SynthesizeAnswer(context.Background(), principal, input); err != nil {
			t.Fatalf("SynthesizeAnswer() error = %v", err)
		}
		line := certifyDecision(t, buf, eventspec.ModelSynthesizeDecision, want(map[string]any{"outcome": "success", "fact_group_size": 2}))
		if _, present := line["grounded_beyond_first"]; !present {
			t.Error("grounded_beyond_first must ride with fact_group_size")
		}
		requireAbsent(t, line, "rejection_reason")
	})

	t.Run("a redraw under a request deadline reports the draw sequence and the deadline decision", func(t *testing.T) {
		t.Parallel()
		gen := &drawSequenceGenerator{outputs: []synthesisOutput{invalidTitleSynthesisOutput(), validSynthesisOutput()}}
		runtime, buf := certifyRuntime(t, gen, Config{MaxAttempts: 1, MaxSynthesisResynthesisAttempts: MaxSynthesisResynthesisAttemptsCeiling})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if _, _, err := runtime.SynthesizeAnswer(ctx, principal, validSynthesisInput()); err != nil {
			t.Fatalf("SynthesizeAnswer() error = %v", err)
		}
		line := certifyDecision(t, buf, eventspec.ModelSynthesizeDecision, want(map[string]any{
			"outcome": "success", "draws_total": 3, "draws_retried": 2,
			"resynthesis_stopped_for_deadline": false,
		}))
		keys := decisionKeyOrder(t, buf, "synthesize")
		wantTail := []string{
			"draws_total", "draws_retried", "draw_outcomes", "draw_output_digests", "draw_rejected_clauses", "draw_rejected_reasons",
			"resynthesis_deadline_remaining_ms", "resynthesis_deadline_reserved_ms", "resynthesis_stopped_for_deadline",
		}
		if got := keys[len(synthesizeDecisionKeys) : len(synthesizeDecisionKeys)+len(wantTail)]; !reflect.DeepEqual(got, wantTail) {
			t.Fatalf("conditional key sequence = %v, want %v", got, wantTail)
		}
		if line["draw_outcomes"] != "1:invalid_output,2:success,3:success" {
			t.Fatalf("draw_outcomes = %v", line["draw_outcomes"])
		}
	})
}

func TestPhraseDecisionLineCertifiesServedAndPreReceiptShapes(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}

	t.Run("served after one retry", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &sequencedGenerator{phrasing: phrasingOutput{}, errs: []error{retryableUnavailable()}},
			Config{MaxAttempts: 2, PhrasingModel: "test/phrasing"})
		if _, _, err := runtime.PhraseStructureOffers(context.Background(), principal, validPhrasingInput()); err != nil {
			t.Fatalf("PhraseStructureOffers() error = %v", err)
		}
		if got := decisionKeyOrder(t, buf, "phrase_offers"); !reflect.DeepEqual(got, phraseDecisionKeys) {
			t.Fatalf("phrase key sequence = %v, want %v", got, phraseDecisionKeys)
		}
		line := certifyDecision(t, buf, eventspec.ModelPhraseDecision, map[string]any{
			"request_id": "request_00000001", "operation": "phrase_offers",
			"attempts_total": 2, "attempts_retried": 1, "attempt_outcomes": "1:unavailable,2:success",
		})
		requireElapsedAdvanced(t, line)
	})

	t.Run("returned before the model was reached", func(t *testing.T) {
		t.Parallel()
		runtime, buf := certifyRuntime(t, &sequencedGenerator{}, Config{PhrasingModel: "test/phrasing"})
		if _, _, err := runtime.PhraseStructureOffers(context.Background(), principal, contextfabric.StructureOfferPhrasingInput{}); err == nil {
			t.Fatal("PhraseStructureOffers() error = nil, want the empty-offer-set rejection")
		}
		certifyDecision(t, buf, eventspec.ModelPhraseDecision, map[string]any{
			"request_id": "", "operation": "", "attempts_total": 0, "attempt_outcomes": "",
		})
	})
}

// TestMergeFallbackReceiptKeepsThePrimaryAttemptHistory pins the identity rule
// the decision line depends on: the answer identity becomes the fallback's,
// the primary's attempt count survives, and usage from both legs is summed.
func TestMergeFallbackReceiptKeepsThePrimaryAttemptHistory(t *testing.T) {
	t.Parallel()
	primary := contextfabric.ModelExecutionReceipt{
		Provider: "p-provider", Model: "p-model", ModelVersion: "p-v1", Attempts: 3, Outcome: "unavailable",
		OutputDigest: "primary-digest", Usage: contextfabric.ModelUsage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14},
	}
	fallback := contextfabric.ModelExecutionReceipt{
		Provider: "f-provider", Model: "f-model", ModelVersion: "f-v1", Attempts: 1,
		Usage: contextfabric.ModelUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
	}
	merged := mergeFallbackReceipt(primary, fallback)
	if !merged.FallbackUsed || merged.Provider != "f-provider" || merged.Model != "f-model" || merged.ModelVersion != "f-v1" {
		t.Fatalf("merged identity = %q/%q/%q used=%t, want the fallback leg's", merged.Provider, merged.Model, merged.ModelVersion, merged.FallbackUsed)
	}
	if merged.Attempts != 3 || merged.Outcome != "unavailable" {
		t.Fatalf("merged attempts/outcome = %d/%q, want the primary's 3/unavailable kept", merged.Attempts, merged.Outcome)
	}
	if merged.OutputDigest != "primary-digest" {
		t.Fatalf("merged digest = %q, want the primary's kept when the fallback has none", merged.OutputDigest)
	}
	if merged.Usage.TotalTokens != 17 || merged.Usage.InputTokens != 11 || merged.Usage.OutputTokens != 6 {
		t.Fatalf("merged usage = %+v, want the two legs summed", merged.Usage)
	}
}
