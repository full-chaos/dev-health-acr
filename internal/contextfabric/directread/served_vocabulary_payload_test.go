package directread

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"
)

// The golden holds the bytes read_relationships served before the value sets
// became exported constants.
func TestReadRelationshipsServedVocabularyBytesAreUnchanged(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
	tiers := []string{"native", "explicit_text", "heuristic", "guess"}
	for i, tier := range tiers {
		id := "rel-" + string(rune('a'+i))
		graph.edges = append(graph.edges, edgeBetween(id, "LINKS_PULL_REQUEST", repoA, teamT, map[string]interface{}{"property_link_provenance": tier}))
	}
	var out [][]byte
	for _, asOf := range []string{"", "2026-10-01T00:00:00Z"} {
		reader, _ := newRelReader(graph, &now)
		response, err := reader.Read(relCtx("served"), unrestricted, RelationshipsRequest{
			Subject: RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}, Types: []string{"LINKS_PULL_REQUEST"}, AsOf: asOf,
		})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.MarshalIndent(response, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, encoded)
	}
	got := append(bytes.Join(out, []byte("\n")), '\n')
	const golden = "testdata/read_relationships_served_vocabulary.golden.json"
	if *servedGolden {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("served read_relationships bytes changed:\n%s\nwant:\n%s", got, want)
	}
}

var servedGolden = flag.Bool("write-served-golden", false, "rewrite the served-vocabulary golden")
