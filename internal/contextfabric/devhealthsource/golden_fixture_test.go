package devhealthsource_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const goldenProjectionBatchPath = "../../../contracts/examples/v1/context_fabric_projection_batch.v1.json"

// goldenRelationshipPrefixes are the CHAOS-3802 producers' relationship-ID
// prefixes. Deriving the golden entries by prefix rather than listing them
// keeps this test honest about a producer that starts emitting a NEW edge:
// the regenerated set simply grows and the committed fixture no longer
// matches.
var goldenRelationshipPrefixes = []string{
	"relationship:work_item_project:",
	"relationship:work_item_team:",
	"relationship:project_team:",
}

// TestGoldenProjectionBatchMatchesProducerOutput is codex round-1 F3, and it
// is the whole point of "generate fixtures from the producer, never
// hand-author them": the committed golden JSON is compared against what the
// producers actually emit, on every run.
//
// The previous shape was an opt-in generator gated behind an env var, with
// nothing asserting the committed file still matched. That is strictly worse
// than no generator: a stale fixture -- edited by hand, left behind by a
// changed producer, or simply missing an edge type -- passed silently while
// looking like generated provenance. It also let the committed file carry
// only two of the three edge types without anything noticing.
func TestGoldenProjectionBatchMatchesProducerOutput(t *testing.T) {
	t.Parallel()
	produced := goldenRelationshipsFromProducer(t)
	if len(produced) < 3 {
		t.Fatalf("expected all three CHAOS-3802 edge types in the generated set, got %d", len(produced))
	}
	kinds := map[contractsv1.ContextFabricRelationshipType]bool{}
	for _, relationship := range produced {
		kinds[relationship.Type] = true
	}
	for _, want := range []contractsv1.ContextFabricRelationshipType{
		contractsv1.ContextFabricRelationshipBelongsToProject,
		contractsv1.ContextFabricRelationshipOwnedByTeam,
	} {
		if !kinds[want] {
			t.Fatalf("generated set is missing %q -- the fixture cannot document an edge type no producer emitted", want)
		}
	}

	committed := goldenRelationshipsFromFixture(t)
	wantJSON := mustMarshalIndent(t, produced)
	gotJSON := mustMarshalIndent(t, committed)
	if wantJSON != gotJSON {
		t.Fatalf("committed golden fixture has drifted from producer output.\nRegenerate with:\n  ACR_WRITE_GOLDEN_PROJECTION_BATCH=1 go test ./internal/contextfabric/devhealthsource/ -run TestGoldenProjectionBatchMatchesProducerOutput\n\nproducer:\n%s\n\ncommitted:\n%s", wantJSON, gotJSON)
	}
}

// goldenRelationshipsFromProducer runs the real producers over the
// live-shaped fixture rows and returns their CHAOS-3802 edges. Setting
// ACR_WRITE_GOLDEN_PROJECTION_BATCH=1 rewrites the committed file from this
// same output, so regeneration and verification can never disagree about
// what "producer output" means.
func goldenRelationshipsFromProducer(t *testing.T) []contractsv1.ContextFabricRelationshipProjection {
	t.Helper()
	batch := teamsProjectsBatch(t, liveShapedEdgeClient())
	produced := make([]contractsv1.ContextFabricRelationshipProjection, 0, len(batch.Relationships))
	for _, relationship := range batch.Relationships {
		if hasGoldenPrefix(relationship.RelationshipID) {
			produced = append(produced, relationship)
		}
	}
	if os.Getenv("ACR_WRITE_GOLDEN_PROJECTION_BATCH") == "1" {
		writeGoldenRelationships(t, produced)
	}
	return produced
}

func goldenRelationshipsFromFixture(t *testing.T) []contractsv1.ContextFabricRelationshipProjection {
	t.Helper()
	var doc struct {
		Relationships []contractsv1.ContextFabricRelationshipProjection `json:"relationships"`
	}
	raw, err := os.ReadFile(filepath.Clean(goldenProjectionBatchPath))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode golden fixture: %v", err)
	}
	committed := make([]contractsv1.ContextFabricRelationshipProjection, 0, len(doc.Relationships))
	for _, relationship := range doc.Relationships {
		if hasGoldenPrefix(relationship.RelationshipID) {
			committed = append(committed, relationship)
		}
	}
	return committed
}

func writeGoldenRelationships(t *testing.T, produced []contractsv1.ContextFabricRelationshipProjection) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(goldenProjectionBatchPath))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode golden fixture: %v", err)
	}
	var existing []json.RawMessage
	if err := json.Unmarshal(doc["relationships"], &existing); err != nil {
		t.Fatalf("decode golden relationships: %v", err)
	}
	kept := make([]json.RawMessage, 0, len(existing))
	for _, entry := range existing {
		var identified struct {
			RelationshipID string `json:"relationship_id"`
		}
		if err := json.Unmarshal(entry, &identified); err != nil {
			t.Fatalf("decode golden relationship: %v", err)
		}
		if !hasGoldenPrefix(identified.RelationshipID) {
			kept = append(kept, entry)
		}
	}
	for _, relationship := range produced {
		encoded, err := json.Marshal(relationship)
		if err != nil {
			t.Fatalf("encode produced relationship: %v", err)
		}
		kept = append(kept, encoded)
	}
	merged, err := json.Marshal(kept)
	if err != nil {
		t.Fatalf("encode golden relationships: %v", err)
	}
	doc["relationships"] = merged
	// Re-marshal through a generic map then re-indent, matching the file's
	// existing two-space style.
	var whole map[string]interface{}
	rebuilt, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode golden fixture: %v", err)
	}
	if err := json.Unmarshal(rebuilt, &whole); err != nil {
		t.Fatalf("decode rebuilt golden fixture: %v", err)
	}
	indented, err := json.MarshalIndent(whole, "", "  ")
	if err != nil {
		t.Fatalf("indent golden fixture: %v", err)
	}
	if err := os.WriteFile(goldenProjectionBatchPath, append(indented, '\n'), 0o600); err != nil {
		t.Fatalf("write golden fixture: %v", err)
	}
	t.Logf("regenerated %s from producer output", goldenProjectionBatchPath)
}

func hasGoldenPrefix(relationshipID string) bool {
	for _, prefix := range goldenRelationshipPrefixes {
		if len(relationshipID) >= len(prefix) && relationshipID[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func mustMarshalIndent(t *testing.T, value interface{}) string {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}
