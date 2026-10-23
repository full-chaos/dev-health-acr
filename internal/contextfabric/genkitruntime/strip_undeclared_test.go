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

func synthesisWireSchema(t *testing.T) map[string]any {
	t.Helper()
	schema, err := newOutputSchema(synthesisOutput{}, false)
	if err != nil {
		t.Fatal(err)
	}
	return schema.wire
}

// drawWithExtras is a real marshalled synthesis draw with undeclared keys at
// the top level and inside the first claimed fact: the shape prod returned.
func drawWithExtras(t *testing.T, extra map[string]any, claimExtra map[string]any) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(validSynthesisDrawText(t)), &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range extra {
		fields[key] = value
	}
	claims, _ := fields["claimed_facts"].([]any)
	if len(claims) == 0 {
		t.Fatal("fixture draw carries no claimed fact")
	}
	first, _ := claims[0].(map[string]any)
	for key, value := range claimExtra {
		first[key] = value
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestUndeclaredPropertiesAreDroppedAndTheDrawServes(t *testing.T) {
	text := drawWithExtras(t, map[string]any{"reasoning": "x", "confidence_note": 3}, map[string]any{"citation": "y"})
	rt, logger, calls := scriptedSynthesisGenkitRuntime(t, []string{text})
	draft, receipt, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want the draw to serve with its undeclared properties dropped", err)
	}
	if calls.Load() != 1 || receipt.Outcome != "success" {
		t.Fatalf("calls = %d outcome = %q, want one successful draw", calls.Load(), receipt.Outcome)
	}
	clean, _, cleanErr := func() (contextfabric.SynthesisDraft, any, error) {
		rt, _, _ := scriptedSynthesisGenkitRuntime(t, []string{validSynthesisDrawText(t)})
		d, r, e := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
		return d, r, e
	}()
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}
	got, _ := json.Marshal(draft)
	want, _ := json.Marshal(clean)
	if string(got) != string(want) {
		t.Fatalf("draft differs from the clean draw's draft:\n got %s\nwant %s", got, want)
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	var found bool
	for _, record := range logger.records {
		if record.Message != llmUndeclaredDroppedMessage {
			continue
		}
		found = true
		if record.Attrs["dropped_paths"] != int64(3) || record.Attrs["operation"] != "synthesize" {
			t.Fatalf("drop line attrs = %v, want 3 paths for synthesize", record.Attrs)
		}
		paths, _ := record.Attrs["paths"].([]string)
		joined := strings.Join(paths, ",")
		for _, want := range []string{"$." + droppedKeyLabel("reasoning"), "$." + droppedKeyLabel("confidence_note"), "$.claimed_facts[]." + droppedKeyLabel("citation")} {
			if !strings.Contains(joined, want) {
				t.Fatalf("paths = %v, missing %s", paths, want)
			}
		}
		for _, value := range []string{"\"x\"", "\"y\""} {
			if strings.Contains(joined, value) {
				t.Fatalf("paths quote a value: %v", paths)
			}
		}
	}
	if !found {
		t.Fatalf("no %q line logged", llmUndeclaredDroppedMessage)
	}
}

func TestDroppingUndeclaredPropertiesNeverMasksARefusal(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"missing_required_field": func(f map[string]any) { delete(f, "direct_judgment") },
		"declared_key_wrong_type": func(f map[string]any) {
			f["limitations"] = "not a list"
		},
		"out_of_enum_status": func(f map[string]any) { f["status"] = "not_a_status" },
	} {
		t.Run(name, func(t *testing.T) {
			var fields map[string]any
			if err := json.Unmarshal([]byte(validSynthesisDrawText(t)), &fields); err != nil {
				t.Fatal(err)
			}
			mutate(fields)
			fields["undeclared_extra"] = true
			encoded, _ := json.Marshal(fields)
			rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{string(encoded)})
			_, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if !errors.Is(err, contextfabric.ErrSynthesisRejected) {
				t.Fatalf("err = %v, want the typed synthesis rejection", err)
			}
			if calls.Load() != invalidSynthesisDrawCeiling {
				t.Fatalf("calls = %d, want %d redraws before refusing", calls.Load(), invalidSynthesisDrawCeiling)
			}
		})
	}
}

func TestDropUndeclaredPropertiesLeavesOpenNodesAndCleanRepliesAlone(t *testing.T) {
	schema := synthesisWireSchema(t)
	clean := validSynthesisDrawText(t)
	if out, dropped := dropUndeclaredProperties(clean, schema); out != clean || len(dropped) != 0 {
		t.Fatalf("clean reply changed or reported drops: %v", dropped)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(clean), &fields); err != nil {
		t.Fatal(err)
	}
	fields["coverage_disclosures"] = []any{map[string]any{"detail_id": "d", "text": "t", "free_form": 1}}
	claim := fields["claimed_facts"].([]any)[0].(map[string]any)
	claim["rows"] = []any{map[string]any{"fields": map[string]any{"any_key_at_all": map[string]any{"string": "v"}}}}
	encoded, _ := json.Marshal(fields)
	out, dropped := dropUndeclaredProperties(string(encoded), schema)
	if len(dropped) != 0 || out != string(encoded) {
		t.Fatalf("open map or untyped node was altered: dropped=%v", dropped)
	}
	if out, dropped := dropUndeclaredProperties("not json", schema); out != "not json" || len(dropped) != 0 {
		t.Fatalf("undecodable text changed")
	}
}

func TestDropUndeclaredPropertiesKeepsLargeNumbersExact(t *testing.T) {
	schema := synthesisWireSchema(t)
	var fields map[string]any
	if err := json.Unmarshal([]byte(validSynthesisDrawText(t)), &fields); err != nil {
		t.Fatal(err)
	}
	fields["extra"] = 1
	encoded, _ := json.Marshal(fields)
	text := strings.Replace(string(encoded), `"extra":1`, `"extra":1,"coverage_disclosures":[{"detail_id":"d","text":"t","n":9007199254740993}]`, 1)
	out, dropped := dropUndeclaredProperties(text, schema)
	if len(dropped) != 1 || !strings.Contains(out, "9007199254740993") {
		t.Fatalf("dropped=%v, large number rewritten in %s", dropped, out)
	}
}

// A model-chosen key can be anything, a credential-shaped string included: the
// log names it by digest only, never by its text, at any depth.
func TestDroppedKeyNamesNeverReachTheLog(t *testing.T) {
	const secretKey = "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	text := drawWithExtras(t, map[string]any{secretKey: "private-value"}, map[string]any{secretKey + "_nested": 1})
	rt, logger, _ := scriptedSynthesisGenkitRuntime(t, []string{text})
	if _, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", err)
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	var sawDrop bool
	for _, record := range logger.records {
		rendered := record.Message + " " + fmt.Sprint(record.Attrs)
		if strings.Contains(rendered, "ghp_") || strings.Contains(rendered, "private-value") {
			t.Fatalf("log line carries the model-chosen key or its value: %s", rendered)
		}
		if record.Message == llmUndeclaredDroppedMessage {
			sawDrop = true
			paths, _ := record.Attrs["paths"].([]string)
			if !strings.Contains(strings.Join(paths, ","), droppedKeyLabel(secretKey)) {
				t.Fatalf("paths = %v, want the digest label of the dropped key", paths)
			}
		}
	}
	if !sawDrop {
		t.Fatal("no drop line logged")
	}
}
