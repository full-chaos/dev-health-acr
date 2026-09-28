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
	// every "valid" comparison draw a zero-claim answer that CHAOS-5655's
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
	name := "test/chaos-6741-" + strings.ReplaceAll(t.Name(), "/", "-")
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
// of TestInvalidInterpretDrawTakesOneRejectionPath (CHAOS-6072): a draw
// Genkit's local schema check refuses, whatever its violation, must redraw
// while draws remain and end as the typed synthesis rejection (never a bare
// ErrModelOutput / upstream_invalid_output) at the ceiling -- RED on the
// unfixed baseline (0 redraws, bare ErrModelOutput after one call), GREEN
// with the CHAOS-6741 fix.
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
			// (CHAOS-5655's existing design, unchanged by this fix). calls
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
// interpret side.
func TestSynthesisSchemaOnlyRejectionDoesNotLeakIntoALaterValidatorRejection(t *testing.T) {
	rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{
		invalidSynthesisDrawText(t, drawSynthesisUnknownProperty),
		invalidSynthesisDrawText(t, drawSynthesisUnknownProperty),
		validSynthesisDrawText(t),
	})
	_, receipt, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want the third draw to serve", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	if receipt.Outcome != "success" {
		t.Fatalf("receipt.Outcome = %q, want success", receipt.Outcome)
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
