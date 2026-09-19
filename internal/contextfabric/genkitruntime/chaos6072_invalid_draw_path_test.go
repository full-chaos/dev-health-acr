package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// invalidDrawCeiling is the redraw ceiling every cell of the table runs under.
const invalidDrawCeiling = 3

type invalidDrawKind int

const (
	drawOutOfEnumKind invalidDrawKind = iota
	drawOtherSchemaViolation
	drawMalformedJSON
	drawUnknownProperty
)

// invalidDrawText is the raw model text for one kind of invalid draw. The
// out-of-enum kind and the shape violation are refused by Genkit's local
// schema check; the malformed text never decodes at all.
func invalidDrawText(t *testing.T, kind invalidDrawKind) string {
	t.Helper()
	out := validInterpretationOutput()
	switch kind {
	case drawOutOfEnumKind:
		out.FactRequirements = []factRequirementOutput{{Kind: "kind_outside_the_enum"}}
	case drawOtherSchemaViolation:
		out.Shape = "not_a_shape"
	case drawMalformedJSON:
		return `{"shape": "open", `
	case drawUnknownProperty:
		// A well-formed, valid interpretation plus a property the schema
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

func validDrawText(t *testing.T) string {
	t.Helper()
	encoded, err := json.Marshal(validInterpretationOutput())
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// scriptedGenkitRuntime is the real composed runtime: genkit.Init, a model
// defined on it that answers call i with script[i] (the last repeats), and
// the production sdkGenerator. seeds records the decoding seed per call.
func scriptedGenkitRuntime(t *testing.T, script []string) (*Runtime, *captureLogger, *[]int64, *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	g := genkit.Init(ctx)
	var calls atomic.Int32
	seeds := &[]int64{}
	name := "test/chaos-6072-" + strings.ReplaceAll(t.Name(), "/", "-")
	genkit.DefineModel(g, name, &ai.ModelOptions{
		Label: "scripted",
		Supports: &ai.ModelSupports{
			Constrained: ai.ConstrainedSupportAll, SystemRole: true, Multiturn: true, Output: []string{"json"},
		},
	}, func(_ context.Context, request *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		i := int(calls.Add(1)) - 1
		if cfg, ok := request.Config.(map[string]any); ok {
			if seed, ok := cfg["seed"].(int64); ok {
				*seeds = append(*seeds, seed)
			}
		}
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
		MaxSynthesisResynthesisAttempts: invalidDrawCeiling, Logger: logger,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return rt, handler, seeds, &calls
}

// TestInvalidInterpretDrawTakesOneRejectionPath enumerates the outcome table
// for the detector Genkit's local schema check: every invalid draw, whatever
// its violation, redraws under the next sample index while draws remain and
// ends as the typed rejection (never a bare model-output error) at the ceiling.
func TestInvalidInterpretDrawTakesOneRejectionPath(t *testing.T) {
	kinds := []struct {
		name       string
		kind       invalidDrawKind
		wantReason contextfabric.InterpretationRejectionReason
		wantKind   string
	}{
		{"out_of_enum_kind", drawOutOfEnumKind, contextfabric.InterpretationRejectionReason("fact_requirement_kind_invalid"), "kind_outside_the_enum"},
		{"other_schema_violation", drawOtherSchemaViolation, contextfabric.InterpretationRejectionReason("shape_invalid"), ""},
		{"malformed_json", drawMalformedJSON, contextfabric.InterpretationRejectionUnclassified, ""},
		{"schema_only_violation", drawUnknownProperty, contextfabric.InterpretationRejectionUnclassified, ""},
	}
	for _, tc := range kinds {
		t.Run(tc.name+"/redraw_below_ceiling", func(t *testing.T) {
			rt, handler, seeds, calls := scriptedGenkitRuntime(t, []string{invalidDrawText(t, tc.kind), validDrawText(t)})
			req := validRequest()
			_, receipt, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, req)
			if err != nil {
				t.Fatalf("InterpretQuestion() error = %v, want the redraw to serve", err)
			}
			hash := contextfabric.QuestionHash(req.Question)
			want := []int64{chaos4631InterpretSeedFor(hash, 0), chaos4631InterpretSeedFor(hash, 1)}
			if calls.Load() != 2 || len(*seeds) != 2 || (*seeds)[0] != want[0] || (*seeds)[1] != want[1] {
				t.Fatalf("calls=%d seeds=%v, want 2 calls under sample 0 then sample 1 (%v)", calls.Load(), *seeds, want)
			}
			if receipt.Attempts != 2 || receipt.Usage.TotalTokens != 28 || receipt.Usage.InputTokens != 20 || receipt.Usage.OutputTokens != 8 {
				t.Fatalf("receipt attempts=%d usage=%+v, want both draws summed (2 / 20,8,28)", receipt.Attempts, receipt.Usage)
			}
			attrs := onlyDecisionEvent(t, handler).Attrs
			if got := attrInt(t, attrs, "redraws"); got != 1 {
				t.Fatalf("redraws = %d, want 1", got)
			}
		})
		t.Run(tc.name+"/terminal_at_ceiling", func(t *testing.T) {
			rt, handler, seeds, calls := scriptedGenkitRuntime(t, []string{invalidDrawText(t, tc.kind)})
			_, receipt, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
			if err == nil || !errors.Is(err, contextfabric.ErrInterpretationRejected) || !errors.Is(err, contextfabric.ErrModelOutput) {
				t.Fatalf("err = %v, want the typed interpretation rejection", err)
			}
			if got := contextfabric.InterpretationRejectionReasonOf(err); got != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got, tc.wantReason)
			}
			if calls.Load() != invalidDrawCeiling || len(*seeds) != invalidDrawCeiling {
				t.Fatalf("calls = %d, want the ceiling %d", calls.Load(), invalidDrawCeiling)
			}
			if receipt.Outcome != "invalid_output" || receipt.Attempts != invalidDrawCeiling || receipt.Usage.TotalTokens != 14*invalidDrawCeiling {
				t.Fatalf("receipt = %+v, want invalid_output over %d attempts with summed usage", receipt, invalidDrawCeiling)
			}
			attrs := onlyDecisionEvent(t, handler).Attrs
			if got := attrInt(t, attrs, "redraws"); got != invalidDrawCeiling-1 {
				t.Fatalf("redraws = %d, want %d", got, invalidDrawCeiling-1)
			}
			if got := attrs["rejection_reason"]; got != string(tc.wantReason) {
				t.Fatalf("rejection_reason = %v, want %q", got, tc.wantReason)
			}
			gotKind, present := attrs["rejected_fact_kind"]
			if tc.wantKind != "" && (!present || gotKind != tc.wantKind) {
				t.Fatalf("rejected_fact_kind = (%v, %t), want (%q, true)", gotKind, present, tc.wantKind)
			}
			if tc.wantKind == "" && present {
				t.Fatalf("rejected_fact_kind = %v, want absent", gotKind)
			}
		})
	}
}

// TestValidatorDetectedInvalidDrawTakesTheSameRejectionPath is the other
// detector column: draws Genkit's local check accepts and acr's validator
// refuses (an out-of-enum kind cannot reach the validator through the real
// runtime, so the scripted generator stands in for that one cell).
func TestValidatorDetectedInvalidDrawTakesTheSameRejectionPath(t *testing.T) {
	overlong := validInterpretationOutput()
	overlong.RequestedJudgment = strings.Repeat("x", 4096)
	outOfEnum := validInterpretationOutput()
	outOfEnum.FactRequirements = []factRequirementOutput{{Kind: "kind_outside_the_enum"}}
	cells := []struct {
		name       string
		draw       interpretationOutput
		wantReason contextfabric.InterpretationRejectionReason
		wantKind   string
	}{
		{"out_of_enum_kind", outOfEnum, "fact_requirement_kind_invalid", "kind_outside_the_enum"},
		{"other_violation", overlong, "requested_judgment_invalid", ""},
	}
	for _, tc := range cells {
		t.Run(tc.name+"/redraw_below_ceiling", func(t *testing.T) {
			gen := &redrawGenerator{outputs: []interpretationOutput{tc.draw, validInterpretationOutput()}}
			rt := mustRuntime(t, gen, Config{MaxSynthesisResynthesisAttempts: invalidDrawCeiling})
			if _, _, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
				t.Fatalf("error = %v, want the redraw to serve", err)
			}
			if len(gen.seeds) != 2 {
				t.Fatalf("draws = %d, want 2", len(gen.seeds))
			}
		})
		t.Run(tc.name+"/terminal_at_ceiling", func(t *testing.T) {
			handler, logger := newCaptureLogger()
			gen := &redrawGenerator{outputs: []interpretationOutput{tc.draw}}
			rt := mustRuntime(t, gen, Config{Logger: logger, MaxSynthesisResynthesisAttempts: invalidDrawCeiling})
			_, _, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
			if !errors.Is(err, contextfabric.ErrInterpretationRejected) || contextfabric.InterpretationRejectionReasonOf(err) != tc.wantReason {
				t.Fatalf("err = %v, want the typed rejection %q", err, tc.wantReason)
			}
			if len(gen.seeds) != invalidDrawCeiling {
				t.Fatalf("draws = %d, want %d", len(gen.seeds), invalidDrawCeiling)
			}
			attrs := onlyDecisionEvent(t, handler).Attrs
			if got, present := attrs["rejected_fact_kind"]; tc.wantKind != "" && (!present || got != tc.wantKind) {
				t.Fatalf("rejected_fact_kind = (%v, %t), want %q", got, present, tc.wantKind)
			}
		})
	}
}

func (h *captureLogger) rejectedDrawEvents() []decisionRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []decisionRecord
	for _, r := range h.records {
		if r.Message == rejectedInterpretDrawMessage {
			out = append(out, r)
		}
	}
	return out
}

// TestRecoveredInterpretSequenceKeepsEveryRejectedDrawOnTheTrace: a draw
// rejected by either detector and then recovered by a redraw still leaves one
// Info line naming its own reason, kind, sample and attempt.
func TestRecoveredInterpretSequenceKeepsEveryRejectedDrawOnTheTrace(t *testing.T) {
	rt, handler, _, calls := scriptedGenkitRuntime(t, []string{
		invalidDrawText(t, drawOutOfEnumKind), invalidDrawText(t, drawUnknownProperty), validDrawText(t),
	})
	if _, _, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("error = %v, want the third draw to serve", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	lines := handler.rejectedDrawEvents()
	if len(lines) != 2 {
		t.Fatalf("rejected-draw lines = %d, want 2 (one per rejected draw): %#v", len(lines), lines)
	}
	first, second := lines[0].Attrs, lines[1].Attrs
	if first["rejection_reason"] != "fact_requirement_kind_invalid" || first["rejected_fact_kind"] != "kind_outside_the_enum" || attrInt(t, first, "sample") != 0 || attrInt(t, first, "attempt") != 1 {
		t.Fatalf("first rejected-draw line = %#v", first)
	}
	if second["rejection_reason"] != "unclassified" || attrInt(t, second, "sample") != 1 || attrInt(t, second, "attempt") != 2 {
		t.Fatalf("second rejected-draw line = %#v", second)
	}
	if _, present := second["rejected_fact_kind"]; present {
		t.Fatalf("second line carries a kind: %#v", second)
	}
}

// TestSchemaOnlyRejectionDoesNotLeakIntoALaterValidatorRejection: the
// reason of the terminal draw is that draw's own rule.
func TestSchemaOnlyRejectionDoesNotLeakIntoALaterValidatorRejection(t *testing.T) {
	overlong := validInterpretationOutput()
	overlong.RequestedJudgment = strings.Repeat("x", 4096)
	encoded, err := json.Marshal(overlong)
	if err != nil {
		t.Fatal(err)
	}
	rt, _, _, _ := scriptedGenkitRuntime(t, []string{invalidDrawText(t, drawUnknownProperty), invalidDrawText(t, drawUnknownProperty), string(encoded)})
	_, _, gotErr := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if got := contextfabric.InterpretationRejectionReasonOf(gotErr); got != "requested_judgment_invalid" {
		t.Fatalf("reason = %q (err %v), want the validator's own rule for the terminal draw", got, gotErr)
	}
}
