package devhealthsource_test

import (
	"encoding/json"
	"os"
	"testing"
)

// TestGenerateGoldenRelationshipFixtures is the generator behind the two
// CHAOS-3802 entries in contracts/examples/v1/context_fabric_projection_batch.v1.json.
// Golden fixtures are generated from the producer, never hand-authored, so
// the example can never drift from what the code actually emits. Run with
// ACR_WRITE_GOLDEN_RELATIONSHIPS=<path> to regenerate.
func TestGenerateGoldenRelationshipFixtures(t *testing.T) {
	path := os.Getenv("ACR_WRITE_GOLDEN_RELATIONSHIPS")
	if path == "" {
		t.Skip("set ACR_WRITE_GOLDEN_RELATIONSHIPS=<path> to regenerate the golden relationship entries")
	}
	batch := teamsProjectsBatch(t, liveShapedEdgeClient())
	encoded, err := json.MarshalIndent(batch.Relationships, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
