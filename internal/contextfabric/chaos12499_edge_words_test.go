package contextfabric

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12499. A path's edge reads a served word, on ACR's own example of a
// served result, through the real decoder and the real serve step.
func TestEachEdgeTypeInAServedPathHasAWord(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repopath.Root(t), "contracts/examples/v1/context_fabric_investigation_result_render_shapes.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	served, _ := ServeVocabularyLabels(result, true, 0)
	edges := 0
	for _, path := range served.Paths {
		for _, edge := range path.Edges {
			edges++
			if served.VocabularyLabels["relationship_type:"+string(edge.Type)] == "" {
				t.Errorf("edge type %q has no served word", edge.Type)
			}
		}
	}
	if edges == 0 {
		t.Fatal("the example has no edge: this measured nothing")
	}
}

func TestAPathServesExactlyTheWordsOfItsEdges(t *testing.T) {
	result := InvestigationResult{Paths: []contractsv1.ContextFabricRelationshipPath{{Edges: []contractsv1.ContextFabricRelationshipEdge{
		{Type: contractsv1.ContextFabricRelationshipBlocks}, {Type: contractsv1.ContextFabricRelationshipCorrelatedWithIncident},
	}}}}
	got := serveWords(t, result)
	if len(got) != 2 || got["relationship_type:BLOCKS"] != "blocks" || got["relationship_type:CORRELATED_WITH_INCIDENT"] != "correlated in time with incident" {
		t.Fatalf("map = %v", got)
	}
}

// A client must tolerate an edge type it does not know, and so must the map: no
// key, and no raw token as a label.
func TestAnUnknownEdgeTypeHasNoKey(t *testing.T) {
	result := InvestigationResult{Paths: []contractsv1.ContextFabricRelationshipPath{{Edges: []contractsv1.ContextFabricRelationshipEdge{{Type: "A_FUTURE_EDGE"}}}}}
	if got := serveWords(t, result); len(got) != 0 {
		t.Fatalf("an unknown edge served %v", got)
	}
}
