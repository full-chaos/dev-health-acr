package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const toolInputLiftedLine = "context fabric model reply had its tool input wrapper lifted"

func validDrawFields(t *testing.T) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(validSynthesisDrawText(t)), &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func encodeDraw(t *testing.T, fields map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// partlyWrappedDraw keeps some keys at the root and moves the rest under
// "input", the other shape the live replies had.
func partlyWrappedDraw(t *testing.T) map[string]any {
	t.Helper()
	root := validDrawFields(t)
	inner := map[string]any{}
	for _, key := range []string{"direct_judgment", "current_state", "claimed_facts", "deterministic_answer", "warnings"} {
		inner[key] = root[key]
		delete(root, key)
	}
	root["input"] = inner
	return root
}

func logRecords(logger *captureLogger, message string) []map[string]any {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	var out []map[string]any
	for _, record := range logger.records {
		if record.Message == message {
			out = append(out, record.Attrs)
		}
	}
	return out
}

func TestSynthesisDrawWrappedInAToolInputObjectIsLifted(t *testing.T) {
	clean, _, err := func() (contextfabric.SynthesisDraft, any, error) {
		rt, _, _ := scriptedSynthesisGenkitRuntime(t, []string{validSynthesisDrawText(t)})
		return rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	}()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(clean)
	for name, tc := range map[string]struct {
		reply  map[string]any
		lifted int64
	}{
		"whole draft under input":       {map[string]any{"input": validDrawFields(t)}, 13},
		"part of the draft under input": {partlyWrappedDraw(t), 5},
	} {
		t.Run(name, func(t *testing.T) {
			text := encodeDraw(t, tc.reply)
			rt, logger, calls := scriptedSynthesisGenkitRuntime(t, []string{text})
			draft, receipt, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if err != nil {
				t.Fatalf("SynthesizeAnswer() error = %v (reason %q), want the wrapped draw served", err, contextfabric.SynthesisRejectionReasonOf(err))
			}
			if calls.Load() != 1 || receipt.Outcome != "success" {
				t.Fatalf("calls = %d outcome = %q, want one successful draw", calls.Load(), receipt.Outcome)
			}
			if got, _ := json.Marshal(draft); string(got) != string(want) {
				t.Fatalf("draft differs from the unwrapped draw's draft:\n got %s\nwant %s", got, want)
			}
			lines := logRecords(logger, toolInputLiftedLine)
			if len(lines) != 1 {
				t.Fatalf("lift lines = %d, want 1", len(lines))
			}
			attrs := lines[0]
			rawDigest := contextfabric.DigestModelValue([]byte(text))
			if attrs["operation"] != "synthesize" || attrs["lifted_keys"] != tc.lifted || attrs["reply_bytes"] != int64(len(text)) || attrs["reply_digest"] != rawDigest || len(attrs) != 4 {
				t.Fatalf("lift line attrs = %v, want operation, lifted_keys=%d, reply_bytes=%d, reply_digest=%s only", attrs, tc.lifted, len(text), rawDigest)
			}
			if receipt.OutputDigest == rawDigest {
				t.Fatalf("receipt digest = raw reply digest %s, want the lifted draft's own digest", rawDigest)
			}
			if dropped := logRecords(logger, llmUndeclaredDroppedMessage); len(dropped) != 0 {
				t.Fatalf("undeclared-dropped lines = %v, want none after the lift", dropped)
			}
		})
	}
}

func TestToolInputLiftNeverMasksARefusal(t *testing.T) {
	collision := partlyWrappedDraw(t)
	collision["input"].(map[string]any)["status"] = "complete"
	ungrounded := validDrawFields(t)
	ungrounded["drivers"] = []any{}
	ungrounded["claimed_facts"].([]any)[0].(map[string]any)["field"] = "field_no_fact_carries"
	stringified := validSynthesisDrawText(t)
	for name, tc := range map[string]struct {
		reply  map[string]any
		reason contextfabric.SynthesisRejectionReason
	}{
		"a lifted key collides with a root key":    {collision, ""},
		"input holds the draft as a string":        {map[string]any{"input": stringified}, ""},
		"input holds an array":                     {map[string]any{"input": []any{validDrawFields(t)}}, ""},
		"a lifted draft cites an unobserved field": {map[string]any{"input": ungrounded}, contextfabric.RejectionReasonClaimFieldUnobserved},
	} {
		t.Run(name, func(t *testing.T) {
			rt, logger, calls := scriptedSynthesisGenkitRuntime(t, []string{encodeDraw(t, tc.reply)})
			_, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if !errors.Is(err, contextfabric.ErrSynthesisRejected) {
				t.Fatalf("err = %v, want the typed synthesis rejection", err)
			}
			if calls.Load() != invalidSynthesisDrawCeiling {
				t.Fatalf("calls = %d, want %d", calls.Load(), invalidSynthesisDrawCeiling)
			}
			if tc.reason != "" {
				if got := contextfabric.SynthesisRejectionReasonOf(err); got != tc.reason {
					t.Fatalf("reason = %q, want %q", got, tc.reason)
				}
				if len(logRecords(logger, toolInputLiftedLine)) != invalidSynthesisDrawCeiling {
					t.Fatalf("want one lift line per draw before the claim rule refused it")
				}
				return
			}
			if lines := logRecords(logger, toolInputLiftedLine); len(lines) != 0 {
				t.Fatalf("lift lines = %v, want none", lines)
			}
		})
	}
}

func TestNestedInputObjectIsNotLifted(t *testing.T) {
	fields := validDrawFields(t)
	fields["drivers"].([]any)[0].(map[string]any)["input"] = map[string]any{"status": "complete"}
	rt, logger, _ := scriptedSynthesisGenkitRuntime(t, []string{encodeDraw(t, fields)})
	if _, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", err)
	}
	if lines := logRecords(logger, toolInputLiftedLine); len(lines) != 0 {
		t.Fatalf("lift lines = %v, want none for a nested input key", lines)
	}
	dropped := logRecords(logger, llmUndeclaredDroppedMessage)
	if len(dropped) != 1 || !strings.Contains(fmt.Sprint(dropped[0]["paths"]), "$.drivers[]."+droppedKeyLabel("input")) {
		t.Fatalf("drop lines = %v, want the nested input key dropped", dropped)
	}
}

func TestLiftToolInputWrapperLeavesEveryOtherShapeByteIdentical(t *testing.T) {
	if llmToolInputLiftedMessage != toolInputLiftedLine {
		t.Fatalf("lift message = %q, want %q", llmToolInputLiftedMessage, toolInputLiftedLine)
	}
	schema := synthesisWireSchema(t)
	declaresInput := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"input": map[string]any{"type": "object"}}}
	wrapped := encodeDraw(t, map[string]any{"input": validDrawFields(t)})
	for name, tc := range map[string]struct {
		candidate string
		schema    map[string]any
	}{
		"a clean reply":                {validSynthesisDrawText(t), schema},
		"an empty input object":        {`{"input":{},"status":"complete"}`, schema},
		"input as null":                {`{"input":null}`, schema},
		"a root that is not an object": {`[{"input":{"status":"complete"}}]`, schema},
		"not json":                     {`{"input":`, schema},
		"a schema that declares input": {wrapped, declaresInput},
		"a schema with no properties":  {wrapped, map[string]any{"type": "object"}},
	} {
		t.Run(name, func(t *testing.T) {
			if out, lifted := liftToolInputWrapper(tc.candidate, tc.schema); out != tc.candidate || lifted != 0 {
				t.Fatalf("lifted %d keys, out = %s, want the candidate unchanged", lifted, out)
			}
		})
	}
}

func TestLiftToolInputWrapperKeepsValuesExact(t *testing.T) {
	out, lifted := liftToolInputWrapper(`{"input":{"coverage_disclosures":[{"n":9007199254740993,"t":"<a&b>"}],"status":"complete"}}`, synthesisWireSchema(t))
	if lifted != 2 {
		t.Fatalf("lifted = %d, want 2", lifted)
	}
	if out != `{"coverage_disclosures":[{"n":9007199254740993,"t":"<a&b>"}],"status":"complete"}` {
		t.Fatalf("out = %s, want the members at the root with values unchanged", out)
	}
}

func TestToolInputLiftIsSynthesisOnly(t *testing.T) {
	interpretation, err := newOutputSchema(interpretationOutput{}, true)
	if err != nil {
		t.Fatal(err)
	}
	handler, logger := newCaptureLogger()
	generator := llmGenerator{interpretation: interpretation, logger: logger}
	wrapped := `{"input":{"shape":"single_subject"}}`
	candidate, err := generator.check(context.Background(), "interpret", wrapped, generator.interpretation)
	if err == nil || candidate != wrapped {
		t.Fatalf("interpret check = (%s, %v), want the wrapped reply refused unchanged", candidate, err)
	}
	if lines := logRecords(handler, toolInputLiftedLine); len(lines) != 0 {
		t.Fatalf("lift lines = %v, want none for interpretation", lines)
	}
}
