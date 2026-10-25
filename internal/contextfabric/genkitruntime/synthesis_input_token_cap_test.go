package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
	"github.com/full-chaos/dev-health-go/llm"
)

const tokenCapModel = "token-cap-model"

var tokenCapPrincipal = storage.Principal{OrgID: "org_1"}

// promptOverheadBytes is what every synthesize call sends besides the input:
// the system prompt and the output schema, read from the prompt package.
func promptOverheadBytes() int {
	return len(synthesisprompt.System()) + len(synthesisprompt.OutputSchema())
}

// estimatedTokens is the estimate stated independently of the runtime: whole
// prompt bytes over three, rounded up.
func estimatedTokens(payloadBytes int) int {
	return (promptOverheadBytes() + payloadBytes + 2) / 3
}

func tokenCapInput(workFacts int, padding int) contextfabric.SynthesisInput {
	input := validSynthesisInput()
	for index := 0; index < workFacts; index++ {
		id := fmt.Sprintf("work_item_%04d", index)
		input.Facts.Facts = append(input.Facts.Facts, contextfabric.CanonicalFact{
			Kind:    contextfabric.FactWork,
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id, Label: id},
			Fields: map[string]contextfabric.FactValue{
				"title": contextfabric.StringFactValue(id + " " + strings.Repeat("x", padding)),
			},
			SourceState: contextfabric.SourceAvailable, Source: "ops", SourceVersion: "v1",
		})
	}
	return input
}

func payloadBytes(t *testing.T, input contextfabric.SynthesisInput) int {
	t.Helper()
	encoded, err := synthesisprompt.UserPayload(tokenCapPrincipal.OrgID, input, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return len(encoded)
}

func tokenCapRuntime(t *testing.T, tokenCap int) (*Runtime, *scriptedLLM, *captureLogger) {
	t.Helper()
	handler, logger := newCaptureLogger()
	runtime, client := tokenCapRuntimeLogging(t, tokenCap, logger)
	return runtime, client, handler
}

func tokenCapRuntimeLogging(t *testing.T, tokenCap int, logger *slog.Logger) (*Runtime, *scriptedLLM) {
	t.Helper()
	client := &scriptedLLM{model: tokenCapModel, replies: []llmReply{{result: llm.Result{
		Text: validSynthesisDrawText(t), Model: tokenCapModel,
		Usage: llm.Usage{InputTokens: intp(10), OutputTokens: intp(4)},
	}}}}
	runtime, err := NewWithLLM(Config{
		Provider: "test", Model: tokenCapModel, ModelVersion: "test-v1",
		InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1",
		SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1", Timeout: time.Minute,
		MaxAttempts: 1, MaxInputBytes: 512 << 10, MaxSynthesisInputTokens: tokenCap, Logger: logger,
	}, LLMClients{tokenCapModel: client})
	if err != nil {
		t.Fatalf("NewWithLLM() error = %v", err)
	}
	return runtime, client
}

func (s *scriptedLLM) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func synthesisInputRecords(h *captureLogger) []decisionRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []decisionRecord
	for _, record := range h.records {
		if record.Message == eventspec.SynthesisInput.Msg {
			out = append(out, record)
		}
	}
	return out
}

func lineInt(t *testing.T, record decisionRecord, key string) int {
	t.Helper()
	value, ok := record.Attrs[key]
	if !ok {
		t.Fatalf("synthesis input line has no %s: %v", key, record.Attrs)
	}
	number, ok := value.(int64)
	if !ok {
		t.Fatalf("%s = %#v, want an int", key, value)
	}
	return int(number)
}

// An input whose estimated prompt is one token over the cap is never sent:
// the call returns the input overflow, with the byte bound the cap leaves, and
// the model is not called. At the cap itself the same input is sent.
func TestASynthesisInputOverTheTokenCapIsNotSent(t *testing.T) {
	input := tokenCapInput(400, 40)
	payload := payloadBytes(t, input)
	atCap := estimatedTokens(payload)
	if atCap-1 < MinSynthesisInputTokens {
		t.Fatalf("fixture estimate %d leaves no cap below it above the minimum %d", atCap, MinSynthesisInputTokens)
	}

	runtime, client, _ := tokenCapRuntime(t, atCap-1)
	_, _, err := runtime.SynthesizeAnswer(context.Background(), tokenCapPrincipal, input)
	var overflow *contextfabric.ModelInputOverflow
	if !errors.As(err, &overflow) {
		t.Fatalf("err = %v, want the input overflow", err)
	}
	if want := 3*(atCap-1) - promptOverheadBytes(); overflow.Bytes != payload || overflow.MaxBytes != want {
		t.Fatalf("overflow = %d bytes, bound %d; want %d bytes, bound %d", overflow.Bytes, overflow.MaxBytes, payload, want)
	}
	if client.calls() != 0 {
		t.Fatalf("model calls = %d, want 0", client.calls())
	}

	runtime, client, _ = tokenCapRuntime(t, atCap)
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), tokenCapPrincipal, input); err != nil {
		t.Fatalf("at the cap: err = %v, want the call placed", err)
	}
	if client.calls() != 1 {
		t.Fatalf("at the cap: model calls = %d, want 1", client.calls())
	}
}

// With the cap off the runtime keeps today's byte bound only: an input the
// cap would refuse is sent, and the input line carries no estimate.
func TestWithTheTokenCapOffTheByteBoundAloneDecides(t *testing.T) {
	input := tokenCapInput(400, 40)
	runtime, client, logs := tokenCapRuntime(t, 0)
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), tokenCapPrincipal, input); err != nil {
		t.Fatalf("err = %v, want the call placed", err)
	}
	if client.calls() != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls())
	}
	records := synthesisInputRecords(logs)
	if len(records) != 1 {
		t.Fatalf("synthesis input lines = %d, want 1", len(records))
	}
	for _, key := range []string{"input_token_estimate", "input_token_cap"} {
		if _, ok := records[0].Attrs[key]; ok {
			t.Fatalf("the line carries %s with the cap off: %v", key, records[0].Attrs)
		}
	}
}

// With the cap on, the input line of a placed call names the estimate and the
// cap. The estimate rounds a part token up, so the input is one whose prompt
// is not a whole number of tokens.
func TestTheSynthesisInputLineNamesTheEstimateAndTheCap(t *testing.T) {
	input, payload := tokenCapInput(400, 40), 0
	for padding := 40; padding < 43; padding++ {
		input = tokenCapInput(400, padding)
		if payload = payloadBytes(t, input); (promptOverheadBytes()+payload)%3 != 0 {
			break
		}
	}
	if (promptOverheadBytes()+payload)%3 == 0 {
		t.Fatal("no fixture prompt with a part token")
	}
	tokenCap := estimatedTokens(payload) + 7
	runtime, _, logs := tokenCapRuntime(t, tokenCap)
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), tokenCapPrincipal, input); err != nil {
		t.Fatalf("err = %v, want the call placed", err)
	}
	records := synthesisInputRecords(logs)
	if len(records) != 1 {
		t.Fatalf("synthesis input lines = %d, want 1", len(records))
	}
	if got := lineInt(t, records[0], "input_token_estimate"); got != estimatedTokens(payload) {
		t.Fatalf("input_token_estimate = %d, want %d", got, estimatedTokens(payload))
	}
	if got := lineInt(t, records[0], "input_token_cap"); got != tokenCap {
		t.Fatalf("input_token_cap = %d, want %d", got, tokenCap)
	}
}

// The estimate and the cap survive the log scrubber: a key that names tokens
// is redacted unless it is a declared counter.
func TestTheTokenCapFieldsSurviveTheLogScrubber(t *testing.T) {
	input := tokenCapInput(400, 40)
	tokenCap := estimatedTokens(payloadBytes(t, input)) + 7
	var buffer bytes.Buffer
	runtime, _ := tokenCapRuntimeLogging(t, tokenCap, slog.New(telemetryscrub.Handler(slog.NewJSONHandler(&buffer, nil))))
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), tokenCapPrincipal, input); err != nil {
		t.Fatalf("err = %v, want the call placed", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil || record["msg"] != eventspec.SynthesisInput.Msg {
			continue
		}
		if record["input_token_cap"] != float64(tokenCap) || record["input_token_estimate"] != float64(estimatedTokens(payloadBytes(t, input))) {
			t.Fatalf("scrubbed line = %v, want the cap %d and the estimate in clear", record, tokenCap)
		}
		return
	}
	t.Fatalf("no synthesis input line in %s", buffer.String())
}

// The served answer: an input over the cap is cut to fit, every prompt the
// model is sent is within the cap, and the answer discloses the cut with the
// bounded-input limitation and a per-kind detail.
func TestAnInputOverTheTokenCapIsCutAndTheAnswerSaysSo(t *testing.T) {
	input := tokenCapInput(400, 40)
	full := estimatedTokens(payloadBytes(t, input))
	tokenCap := MinSynthesisInputTokens + (full-MinSynthesisInputTokens)/2
	runtime, client, _ := tokenCapRuntime(t, tokenCap)
	synthesizer := contextfabric.RuntimeAnswerSynthesizer{Runtime: runtime}

	result, err := synthesizer.Synthesize(context.Background(), tokenCapPrincipal, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v, want the cut answer served", err)
	}
	if client.calls() == 0 {
		t.Fatal("the model was never called")
	}
	for index := 0; index < client.calls(); index++ {
		if got := estimatedTokens(len(client.request(index).Prompt)); got > tokenCap {
			t.Fatalf("call %d sent an estimated %d tokens, over the cap %d", index, got, tokenCap)
		}
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisInputBoundedLimitation) {
		t.Fatalf("limitations = %q, want the bounded-input disclosure", result.Limitations)
	}
	var work *contextfabric.CoverageDetail
	for index, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisInputBounded && detail.FactKind == contextfabric.FactWork {
			work = &result.Coverage.Details[index]
		}
	}
	if work == nil || work.Declared == nil || work.Served == nil || *work.Declared != 400 || *work.Served >= 400 || *work.Served < 1 {
		t.Fatalf("coverage details = %+v, want a work detail of fewer than 400 given of 400 read", result.Coverage.Details)
	}
}

// When no cut can bring the input under the cap, the call ends with the input
// overflow and the model is never called.
func TestAnInputNoCutCanFitUnderTheTokenCapIsNeverSent(t *testing.T) {
	input := tokenCapInput(1, 3*MinSynthesisInputTokens)
	runtime, client, _ := tokenCapRuntime(t, MinSynthesisInputTokens)
	synthesizer := contextfabric.RuntimeAnswerSynthesizer{Runtime: runtime}

	_, err := synthesizer.Synthesize(context.Background(), tokenCapPrincipal, input)
	if !errors.Is(err, contextfabric.ErrModelInputTooLarge) {
		t.Fatalf("err = %v, want ErrModelInputTooLarge", err)
	}
	if client.calls() != 0 {
		t.Fatalf("model calls = %d, want 0", client.calls())
	}
}

// The cap is refused when negative or when it leaves less than the smallest
// input the byte bound admits; zero is off.
func TestTheTokenCapRange(t *testing.T) {
	for _, tc := range []struct {
		tokenCap int
		ok       bool
	}{{-1, false}, {0, true}, {MinSynthesisInputTokens - 1, false}, {MinSynthesisInputTokens, true}} {
		_, err := NewWithLLM(Config{
			Provider: "test", Model: tokenCapModel, ModelVersion: "test-v1", Timeout: time.Minute,
			MaxAttempts: 1, MaxSynthesisInputTokens: tc.tokenCap,
		}, LLMClients{tokenCapModel: &scriptedLLM{model: tokenCapModel}})
		if (err == nil) != tc.ok {
			t.Fatalf("cap %d: err = %v, want ok=%v", tc.tokenCap, err, tc.ok)
		}
	}
	if want := (promptOverheadBytes() + 8<<10 + 2) / 3; MinSynthesisInputTokens != want {
		t.Fatalf("MinSynthesisInputTokens = %d, want %d (8 KiB of input over the prompt overhead)", MinSynthesisInputTokens, want)
	}
}
