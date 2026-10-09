package falkorgraph

import (
	"context"
	"strings"
	"testing"
)

// A full k-NN fetch whose slots an inactive team used cannot claim to be complete:
// an active candidate may lie beyond the fetch.
func TestVectorSearchReportsTruncationWhenAnInactiveTeamUsedAFetchSlot(t *testing.T) {
	cases := []struct {
		name          string
		inactive      bool
		fullFetch     bool
		wantTruncated bool
		wantNodes     int
	}{
		{"inactive twin in a full fetch", true, true, true, 1},
		{"inactive twin, fetch not full", true, false, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
				if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
					return nil, nil
				}
				first := map[string]interface{}{propKind: "team", propCanonicalID: "team:jira:platform", propLabel: "Platform", propPropertyPrefix + "is_active": true}
				second := map[string]interface{}{propKind: "team", propCanonicalID: "team:platform", propLabel: "Platform", propPropertyPrefix + "is_active": !c.inactive}
				rows := []row{{"node": &node{Properties: first}, "score": 0.2}, {"node": &node{Properties: second}, "score": 0.21}}
				if !c.fullFetch {
					return rows[:2], nil
				}
				return rows, nil
			}}
			adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
			limit := 1
			if !c.fullFetch {
				limit = 5
			}
			nodes, truncated, err := adapter.vectorSearchNodes(context.Background(), "k", "org", []float32{1, 0, 0, 0}, 0.55, limit)
			if err != nil {
				t.Fatal(err)
			}
			if truncated != c.wantTruncated {
				t.Fatalf("truncated = %t, want %t", truncated, c.wantTruncated)
			}
			if len(nodes) != c.wantNodes {
				t.Fatalf("nodes = %d, want %d", len(nodes), c.wantNodes)
			}
		})
	}
}
