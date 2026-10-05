package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// invalidSynthesisDrawCeiling is the redraw ceiling every cell of the table
// runs under.
const invalidSynthesisDrawCeiling = 3

type invalidSynthesisDrawKind int

const (
	drawSynthesisOutOfEnumStatus invalidSynthesisDrawKind = iota
	drawSynthesisUnknownProperty
	drawSynthesisMalformedJSON
)

// invalidSynthesisDrawText is the raw model text for one kind of invalid
// synthesis draw. The out-of-enum status is refused by Genkit's local schema
// check, as is the unknown property (a well-formed, otherwise-valid draft
// this package's own validator would accept); the malformed text never
// decodes at all.
func invalidSynthesisDrawText(t *testing.T, kind invalidSynthesisDrawKind) string {
	t.Helper()
	out := validSynthesisOutput()
	// ClaimedFacts is nil-valued (never set) on the fixture, which marshals
	// as JSON null (the schema requires an array) AND, unclaimed, makes
	// every "valid" comparison draw a zero-claim answer that 's
	// own redraw heuristic holds for one extra draw -- a second axis of
	// redraw this test is not exercising. A single grounded claim avoids
	// both.
	out.ClaimedFacts = []contextfabric.ClaimedFact{groundedReadinessClaim(validSynthesisInput())}
	switch kind {
	case drawSynthesisOutOfEnumStatus:
		out.Status = "not_a_status"
	case drawSynthesisMalformedJSON:
		return `{"status": "complete", `
	case drawSynthesisUnknownProperty:
		// A well-formed, valid synthesis draft plus a property the schema
		// forbids: only Genkit's local check can refuse it.
		valid, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSuffix(string(valid), "}") + `,"unexpected_field":1}`
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func validSynthesisDrawText(t *testing.T) string {
	t.Helper()
	out := validSynthesisOutput()
	out.ClaimedFacts = []contextfabric.ClaimedFact{groundedReadinessClaim(validSynthesisInput())}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// scriptedSynthesisGenkitRuntime is the real composed runtime: genkit.Init, a
// model defined on it that answers call i with script[i] (the last
// repeats), and the production sdkGenerator -- so Genkit's OWN local schema
// check runs on every draw, exactly as it does in production.
func scriptedSynthesisGenkitRuntime(t *testing.T, script []string) (*Runtime, *captureLogger, *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	g := genkit.Init(ctx)
	var calls atomic.Int32
	name := "test/synthesis-redraw-" + strings.ReplaceAll(t.Name(), "/", "-")
	genkit.DefineModel(g, name, &ai.ModelOptions{
		Label: "scripted",
		Supports: &ai.ModelSupports{
			Constrained: ai.ConstrainedSupportAll, SystemRole: true, Multiturn: true, Output: []string{"json"},
		},
	}, func(_ context.Context, _ *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		i := int(calls.Add(1)) - 1
		if i >= len(script) {
			i = len(script) - 1
		}
		return &ai.ModelResponse{
			Message:      &ai.Message{Role: ai.RoleModel, Content: []*ai.Part{ai.NewJSONPart(script[i])}},
			FinishReason: ai.FinishReasonStop,
			Usage:        &ai.GenerationUsage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14},
		}, nil
	})
	handler, logger := newCaptureLogger()
	rt, err := New(Config{
		Genkit: g, Provider: "test", Model: name, ModelVersion: "test-v1",
		Timeout: time.Second, MaxAttempts: 1, MaxInputBytes: 128 << 10,
		MaxSynthesisResynthesisAttempts: invalidSynthesisDrawCeiling, Logger: logger,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return rt, handler, &calls
}

// TestInvalidSynthesisDrawTakesOneRejectionPath is the synthesize-side twin
// of TestInvalidInterpretDrawTakesOneRejectionPath: a draw
// Genkit's local schema check refuses, whatever its violation, must redraw
// while draws remain and end as the typed synthesis rejection (never a bare
// ErrModelOutput / upstream_invalid_output) at the ceiling -- RED on the
// unfixed baseline (0 redraws, bare ErrModelOutput after one call), GREEN
// with the fix.
func TestInvalidSynthesisDrawTakesOneRejectionPath(t *testing.T) {
	kinds := []struct {
		name string
		kind invalidSynthesisDrawKind
	}{
		{"out_of_enum_status", drawSynthesisOutOfEnumStatus},
		{"malformed_json", drawSynthesisMalformedJSON},
		{"schema_only_violation", drawSynthesisUnknownProperty},
	}
	for _, tc := range kinds {
		t.Run(tc.name+"/redraw_below_ceiling", func(t *testing.T) {
			rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{invalidSynthesisDrawText(t, tc.kind), validSynthesisDrawText(t)})
			_, receipt, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if err != nil {
				t.Fatalf("SynthesizeAnswer() error = %v, want the redraw to serve", err)
			}
			if calls.Load() != 2 {
				t.Fatalf("calls = %d, want 2 (one rejected draw, one redraw) -- the bug ends the call after 1 with 0 redraws", calls.Load())
			}
			// receipt.Attempts is the FINAL draw's own attempt count (one
			// withRetry call, MaxAttempts=1 here) -- SynthesizeAnswer's draw
			// loop does not accumulate attempts ACROSS draws, only usage
			//. calls
			// and Usage.TotalTokens are what prove the redraw ran.
			if receipt.Outcome != "success" || receipt.Attempts != 1 || receipt.Usage.TotalTokens != 28 {
				t.Fatalf("receipt = %+v, want success (1 attempt on the final draw, usage summed over 2 draws)", receipt)
			}
		})
		t.Run(tc.name+"/terminal_at_ceiling", func(t *testing.T) {
			rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{invalidSynthesisDrawText(t, tc.kind)})
			_, receipt, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if err == nil || !errors.Is(err, contextfabric.ErrSynthesisRejected) || !errors.Is(err, contextfabric.ErrModelOutput) {
				t.Fatalf("err = %v, want the typed synthesis rejection (never a bare upstream_invalid_output)", err)
			}
			if calls.Load() != invalidSynthesisDrawCeiling {
				t.Fatalf("calls = %d, want the ceiling %d (the bug: 1 call, 0 redraws)", calls.Load(), invalidSynthesisDrawCeiling)
			}
			if receipt.Outcome != "invalid_output" || receipt.Attempts != 1 || receipt.Usage.TotalTokens != 14*invalidSynthesisDrawCeiling {
				t.Fatalf("receipt = %+v, want invalid_output (1 attempt on the final draw, usage summed over %d draws)", receipt, invalidSynthesisDrawCeiling)
			}
		})
	}
}

// TestSynthesisSchemaOnlyRejectionDoesNotLeakIntoALaterValidatorRejection:
// the reason of the terminal draw is that draw's own rule, mirroring
// TestSchemaOnlyRejectionDoesNotLeakIntoALaterValidatorRejection on the
// interpret side. Two schema-only draws are followed by a schema-valid draw
// this package's own validator rejects; it ends with its own reason, not the
// unclassified reason of the earlier schema-only draws.
func TestSynthesisSchemaOnlyRejectionDoesNotLeakIntoALaterValidatorRejection(t *testing.T) {
	ungrounded := validSynthesisOutput()
	claim := groundedReadinessClaim(validSynthesisInput())
	claim.Field = "field_no_fact_carries"
	ungrounded.ClaimedFacts = []contextfabric.ClaimedFact{claim}
	encoded, err := json.Marshal(ungrounded)
	if err != nil {
		t.Fatal(err)
	}
	rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{
		invalidSynthesisDrawText(t, drawSynthesisUnknownProperty),
		invalidSynthesisDrawText(t, drawSynthesisUnknownProperty),
		string(encoded),
	})
	_, _, gotErr := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if calls.Load() != invalidSynthesisDrawCeiling {
		t.Fatalf("calls = %d, want %d", calls.Load(), invalidSynthesisDrawCeiling)
	}
	if got := contextfabric.SynthesisRejectionReasonOf(gotErr); got != contextfabric.RejectionReasonClaimFieldUnobserved {
		t.Fatalf("reason = %q (err %v), want the validator's own rule for the terminal draw", got, gotErr)
	}
}

// TestRejectedSynthesisDrawsServeADegradedAnswerThatSaysSo: when every draw is
// refused, the answer layer serves the deterministic parts with a warning
// naming the failed model call, never an error.
func TestRejectedSynthesisDrawsServeADegradedAnswerThatSaysSo(t *testing.T) {
	rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{invalidSynthesisDrawText(t, drawSynthesisOutOfEnumStatus)})
	synthesizer := contextfabric.RuntimeAnswerSynthesizer{Runtime: rt}
	principal := storage.Principal{OrgID: "org_1"}
	input := validSynthesisInput()
	_, err := synthesizer.Synthesize(context.Background(), principal, input)
	var failure *contextfabric.SynthesisFailure
	if !errors.As(err, &failure) || failure.Class != contextfabric.SynthesisFailureRejected {
		t.Fatalf("err = %v, want a synthesis failure of class %s", err, contextfabric.SynthesisFailureRejected)
	}
	if calls.Load() != invalidSynthesisDrawCeiling {
		t.Fatalf("calls = %d, want %d", calls.Load(), invalidSynthesisDrawCeiling)
	}
	result, err := synthesizer.ComposeDegraded(context.Background(), principal, input, failure)
	if err != nil {
		t.Fatalf("ComposeDegraded() error = %v", err)
	}
	if !contextfabric.IsSynthesisModelFailureAnswer(result) || result.Status != contextfabric.InvestigationDegraded {
		t.Fatalf("result status %s warnings %v, want a degraded answer naming the model failure", result.Status, result.Warnings)
	}
	if len(result.ClaimedFacts) != 0 {
		t.Fatalf("claimed facts = %d, want none from a refused draw", len(result.ClaimedFacts))
	}
}

// TestFencedInvalidSynthesisDrawKeepsItsRejection: Genkit unwraps three
// fence shapes before it validates, so a refused synthesis draw wearing any
// of them must still be recovered and redrawn.
func TestFencedInvalidSynthesisDrawKeepsItsRejection(t *testing.T) {
	fences := map[string]string{
		"json_fence":     "```json\n%s\n```",
		"plain_fence":    "```\n%s\n```",
		"implicit_fence": "```%s```",
	}
	for name, format := range fences {
		t.Run(name, func(t *testing.T) {
			bad := fmt.Sprintf(format, invalidSynthesisDrawText(t, drawSynthesisOutOfEnumStatus))
			rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{bad})
			_, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if !errors.Is(err, contextfabric.ErrSynthesisRejected) {
				t.Fatalf("err = %v, want the typed synthesis rejection", err)
			}
			if calls.Load() != invalidSynthesisDrawCeiling {
				t.Fatalf("calls = %d, want %d", calls.Load(), invalidSynthesisDrawCeiling)
			}
			// The same fence around a valid draw is accepted by Genkit itself.
			rt, _, _ = scriptedSynthesisGenkitRuntime(t, []string{fmt.Sprintf(format, validSynthesisDrawText(t))})
			if _, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
				t.Fatalf("fenced valid draw refused: %v", err)
			}
		})
	}
}

func synthesisInputLineAttr(t *testing.T, logger *captureLogger, key string) string {
	t.Helper()
	logger.mu.Lock()
	defer logger.mu.Unlock()
	for _, record := range logger.records {
		if record.Message == eventspec.SynthesisInput.Msg {
			return attrString(t, record.Attrs, key)
		}
	}
	t.Fatal("no synthesis input line logged")
	return ""
}

func synthesisDecisionAttr(t *testing.T, logger *captureLogger, key string) string {
	t.Helper()
	events := logger.decisionEvents()
	if len(events) == 0 {
		t.Fatal("no decision event logged")
	}
	return attrString(t, events[len(events)-1].Attrs, key)
}

// A rejected draw is identified by the text the model wrote, not by what the
// lenient parse keeps: a draw that differs from the valid one only by a
// schema-forbidden property has its own digest, and a draw that never decoded
// reports an unknown claim count instead of zero.
func TestRejectedSynthesisDrawsKeepTheirOwnDigestAndUnknownClaims(t *testing.T) {
	extra := invalidSynthesisDrawText(t, drawSynthesisUnknownProperty)
	valid := validSynthesisDrawText(t)
	rt, logger, _ := scriptedSynthesisGenkitRuntime(t, []string{extra, valid})
	if _, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", err)
	}
	want := fmt.Sprintf("1:%s,2:%s", contextfabric.DigestModelValue([]byte(extra)), contextfabric.DigestModelValue([]byte(valid)))
	if got := synthesisDecisionAttr(t, logger, "draw_output_digests"); got != want {
		t.Fatalf("draw_output_digests = %q, want %q", got, want)
	}
	if got := synthesisInputLineAttr(t, logger, "draw_claims"); got != "1:1,2:1" {
		t.Fatalf("draw_claims = %q, want 1:1,2:1", got)
	}

	first, second := `{"status": "complete", `, `{"status": "partial", `
	rt, logger, _ = scriptedSynthesisGenkitRuntime(t, []string{first, second, first})
	_, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if !errors.Is(err, contextfabric.ErrSynthesisRejected) {
		t.Fatalf("err = %v, want the typed synthesis rejection", err)
	}
	digest := contextfabric.DigestModelValue
	want = fmt.Sprintf("1:%s,2:%s,3:%s", digest([]byte(first)), digest([]byte(second)), digest([]byte(first)))
	if got := synthesisDecisionAttr(t, logger, "draw_output_digests"); got != want {
		t.Fatalf("draw_output_digests = %q, want %q", got, want)
	}
	if got := synthesisInputLineAttr(t, logger, "draw_claims"); got != "1:?,2:?,3:?" {
		t.Fatalf("draw_claims = %q, want an unknown count for draws that never decoded", got)
	}
}

// The error a refused draw returns names a class, never what the model wrote.
func TestRejectedSynthesisDrawErrorDoesNotQuoteTheModelOutput(t *testing.T) {
	out := validSynthesisOutput()
	out.Status = "marker_status_from_the_model"
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	extra := strings.TrimSuffix(validSynthesisDrawText(t), "}") + `,"marker_status_from_the_model":1}`
	for name, text := range map[string]string{
		"out_of_enum":    string(encoded),
		"malformed":      `{"status": "marker_status_from_the_model", `,
		"extra_property": extra,
	} {
		t.Run(name, func(t *testing.T) {
			rt, _, _ := scriptedSynthesisGenkitRuntime(t, []string{text})
			_, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if !errors.Is(err, contextfabric.ErrSynthesisRejected) {
				t.Fatalf("err = %v, want the typed synthesis rejection", err)
			}
			if strings.Contains(err.Error(), "marker_status_from_the_model") {
				t.Fatalf("error quotes the model output: %v", err)
			}
		})
	}
}
