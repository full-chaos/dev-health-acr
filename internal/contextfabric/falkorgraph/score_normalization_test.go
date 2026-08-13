package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// TestFulltextSearchScoreOrderingSurvivesConfidence is the AC-3778-0 probe
// for drift item D11: a strictly more relevant RediSearch full-text hit must
// never end up with a lower graphrank.ResultConfidence than a less relevant
// hit from the SAME result set. Scores here (4.0, 1.5) are realistic
// RediSearch magnitudes -- docs/design/context-fabric-falkordb-adapter.md
// §6.2, verified live: 'goroutine' -> 4, 'payment|retry' -> 4.5/0.5 -- both
// exceed 1, exactly the range where the raw write at queries.go:62 plus
// graphrank.ResultConfidence's `1/score` fallback arm inverts order: a
// score of 4.0 currently normalizes to 0.25, a weaker 1.5 to 0.667.
//
// Before the fix this test FAILS: strongConfidence (0.25) < weakConfidence
// (0.667). After the fix, fulltextSearchNodes must normalize scores into a
// documented, bounded band (a CandidateNode.Relevance the adapter itself
// declares as already-usable-as-confidence) before graphrank ever sees them,
// so the stronger hit's confidence is never lower.
func TestFulltextSearchScoreOrderingSurvivesConfidence(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return []row{
			{"node": &node{Properties: map[string]interface{}{propKind: "work_item", propCanonicalID: "strong", propLabel: "Strong hit"}}, "score": 4.0},
			{"node": &node{Properties: map[string]interface{}{propKind: "work_item", propCanonicalID: "weak", propLabel: "Weak hit"}}, "score": 1.5},
		}, nil
	}}
	adapter := newFakeAdapter(t, fake)

	candidates, _, err := adapter.fulltextSearchNodes(context.Background(), "test-key", "org-1", "release", 10)
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("fulltextSearchNodes() returned %d candidates, want 2", len(candidates))
	}

	var strong, weak graphrank.CandidateNode
	var foundStrong, foundWeak bool
	for _, c := range candidates {
		switch c.Name {
		case "Strong hit":
			strong, foundStrong = c, true
		case "Weak hit":
			weak, foundWeak = c, true
		}
	}
	if !foundStrong || !foundWeak {
		t.Fatalf("fulltextSearchNodes() candidates = %#v, want both Strong hit and Weak hit", candidates)
	}

	strongConfidence := graphrank.ResultConfidence(strong.Relevance, strong.Score)
	weakConfidence := graphrank.ResultConfidence(weak.Relevance, weak.Score)
	if strongConfidence < weakConfidence {
		t.Fatalf("score-ladder inversion (drift D11): raw score 4.0 -> confidence %v, LOWER than raw score 1.5 -> confidence %v",
			strongConfidence, weakConfidence)
	}
}
