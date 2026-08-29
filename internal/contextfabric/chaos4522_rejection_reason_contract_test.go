package contextfabric

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSynthesisRejectedGoldenExampleUsesTheClosedVocabulary is codex R1
// finding 3, narrowed to the drift it actually creates.
//
// `details` is an open map on the wire (`error.v1.schema.json` sets
// `additionalProperties: true`), so publishing `rejection_reason` needs no
// schema or Go type change and is not a contract widening. What it DOES
// need is the same thing `violated_bound` already has: a golden fixture, so
// a consumer can discover the field and its shape rather than learning it
// from a live 422. Before CHAOS-4522 that fixture carried `details: {}` --
// a shape no consumer will now ever receive, since every synthesis
// rejection carries a reason.
//
// This test closes the drift half: the fixture's advertised value must be a
// member of the Go closed vocabulary, so renaming or removing a reason
// without updating the published example fails here instead of silently
// leaving consumers a fixture describing a value the server never sends.
func TestSynthesisRejectedGoldenExampleUsesTheClosedVocabulary(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "error_context_fabric_synthesis_rejected.v1.json"))
	if err != nil {
		t.Fatalf("read golden example: %v", err)
	}
	var example struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatalf("decode golden example: %v", err)
	}
	value, present := example.Error.Details["rejection_reason"]
	if !present {
		t.Fatal("the synthesis_rejected golden example carries no rejection_reason -- every synthesis rejection now sends one, so a fixture without it documents a shape no consumer receives")
	}
	reason, ok := value.(string)
	if !ok {
		t.Fatalf("rejection_reason = %v (%T), want a string", value, value)
	}
	if !ValidSynthesisRejectionReason(SynthesisRejectionReason(reason)) {
		t.Fatalf("the golden example advertises rejection_reason %q, which is not a member of the closed vocabulary -- the published example and the server have drifted", reason)
	}
}
