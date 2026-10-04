package directread

import (
	"encoding/json"
	"strings"
	"testing"
)

// The link tier is served on LINKS_PULL_REQUEST edges only, from the stored
// property_link_provenance, and only when it is in the closed set.
func TestLinkTierServedOnLinksPullRequestOnly(t *testing.T) {
	cases := []struct {
		name         string
		relation     string
		attrs        map[string]interface{}
		wantTier     string
		wantUnserved int
	}{
		{"native", "LINKS_PULL_REQUEST", map[string]interface{}{"property_link_provenance": "native"}, "native", 0},
		{"explicit_text", "LINKS_PULL_REQUEST", map[string]interface{}{"property_link_provenance": "explicit_text"}, "explicit_text", 0},
		{"heuristic", "LINKS_PULL_REQUEST", map[string]interface{}{"property_link_provenance": "heuristic"}, "heuristic", 0},
		{"missing tier is not invented, and is counted", "LINKS_PULL_REQUEST", nil, "", 1},
		{"tier outside the closed set is not served, and is counted", "LINKS_PULL_REQUEST", map[string]interface{}{"property_link_provenance": "guess"}, "", 1},
		{"non-string tier is not served, and is counted", "LINKS_PULL_REQUEST", map[string]interface{}{"property_link_provenance": int64(3)}, "", 1},
		{"other type never carries a tier", "OWNED_BY_TEAM", map[string]interface{}{"property_link_provenance": "native"}, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
			graph.edges = []EdgeCandidate{edgeBetween("rel-1", tc.relation, repoA, teamT, tc.attrs)}
			reader, recorder := newRelReader(graph, nil)
			response, err := reader.Read(relCtx("link-tier-"+tc.name), restrictedA, RelationshipsRequest{Subject: RelationshipsSubject{Kind: string(repoA.Kind), CanonicalID: repoA.CanonicalID}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Edges) != 1 {
				t.Fatalf("edge not served: %+v", response)
			}
			if got := response.Edges[0].Provenance.LinkTier; got != tc.wantTier {
				t.Fatalf("link_tier = %q, want %q", got, tc.wantTier)
			}
			if got := recorder.reads[0].LinkTierUnserved; got != tc.wantUnserved {
				t.Fatalf("LinkTierUnserved = %d, want %d", got, tc.wantUnserved)
			}
			encoded, err := json.Marshal(response.Edges[0].Provenance)
			if err != nil {
				t.Fatal(err)
			}
			if has := strings.Contains(string(encoded), `"link_tier"`); has != (tc.wantTier != "") {
				t.Fatalf("link_tier key present = %v, want %v: %s", has, tc.wantTier != "", encoded)
			}
		})
	}
}
