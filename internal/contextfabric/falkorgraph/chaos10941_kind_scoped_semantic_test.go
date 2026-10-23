package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func chaos10941Row(canonicalID, label string, vector []float64) row {
	return row{"n": &node{Properties: map[string]interface{}{
		propKind: "repository", propCanonicalID: canonicalID, propLabel: label, propEmbedding: vector,
	}}}
}

func chaos10941Adapter(t *testing.T, fake *fakeConn, embedder *stubEmbedder) *Adapter {
	t.Helper()
	return vectorAdapter(t, fake, embedder, 0.55)
}

const chaos10941Question = "self-hosted CI executor setup repository"

func TestChaos10941KindScopedSemantic_RanksByCosineAboveFloorOnly(t *testing.T) {
	t.Parallel()
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return []row{
			chaos10941Row("repository:mid", "mid", []float64{0.6, 0.8, 0, 0}),
			chaos10941Row("repository:near", "near", []float64{1, 0, 0, 0}),
			chaos10941Row("repository:far", "far", []float64{0, 1, 0, 0}),
		}, nil
	}}
	adapter := chaos10941Adapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}})
	got, truncated, degraded, err := adapter.kindScopedSemanticSearchNodes(context.Background(), "k", "org-1", chaos10941Question, contextfabric.SubjectRepository, 20, temporalFilter{})
	if err != nil || truncated || degraded {
		t.Fatalf("err=%v truncated=%v degraded=%v", err, truncated, degraded)
	}
	if len(got) != 2 || got[0].Attributes[propCanonicalID] != "repository:near" || got[1].Attributes[propCanonicalID] != "repository:mid" {
		t.Fatalf("got %d candidates, want near then mid (far is below the floor): %#v", len(got), got)
	}
	for _, c := range got {
		if c.Mechanism != contextfabric.MatchVector || c.VectorSimilarity == nil {
			t.Fatalf("candidate lacks vector mechanism/similarity: %#v", c)
		}
	}
}

func TestChaos10941KindScopedSemantic_QueryCarriesExactTemporalPredicateAndIdentityFence(t *testing.T) {
	t.Parallel()
	var gotCypher string
	var gotParams map[string]interface{}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		gotCypher, gotParams = cypher, params
		if !readOnly {
			t.Fatal("semantic census must be read-only")
		}
		return nil, nil
	}}
	adapter := chaos10941Adapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}})
	temporal := temporalFilter{active: true, startNs: 100, endNs: 200}
	if _, _, _, err := adapter.kindScopedSemanticSearchNodes(context.Background(), "k", "org-1", chaos10941Question, contextfabric.SubjectRepository, 20, temporal); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotCypher, propValidFromNs) || !strings.Contains(gotCypher, propValidToNs) {
		t.Fatalf("query has no validity predicate, a node outside the window could be offered: %s", gotCypher)
	}
	if gotParams[temporalParamStart] != int64(100) || gotParams[temporalParamEnd] != int64(200) {
		t.Fatalf("temporal params not bound: %#v", gotParams)
	}
	if identity, _ := gotParams["identity"].(string); identity != adapter.stampedEmbedderIdentity(adapter.embedder.Identity()) {
		t.Fatalf("embedder identity fence not bound: %#v", gotParams["identity"])
	}
	if gotParams["kind"] != "repository" || gotParams["org"] != "org-1" {
		t.Fatalf("org/kind not bound: %#v", gotParams)
	}
}

func TestChaos10941KindScopedSemantic_UnservedKindOrNoEmbedderRunsNoQuery(t *testing.T) {
	t.Parallel()
	calls := 0
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		calls++
		return nil, nil
	}}
	adapter := chaos10941Adapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}})
	if got, _, degraded, err := adapter.kindScopedSemanticSearchNodes(context.Background(), "k", "org-1", chaos10941Question, contextfabric.SubjectPullRequest, 20, temporalFilter{}); err != nil || degraded || len(got) != 0 {
		t.Fatalf("pull_request: got=%v degraded=%v err=%v", got, degraded, err)
	}
	bare := newFakeAdapter(t, fake)
	if got, _, degraded, err := bare.kindScopedSemanticSearchNodes(context.Background(), "k", "org-1", chaos10941Question, contextfabric.SubjectRepository, 20, temporalFilter{}); err != nil || degraded || len(got) != 0 {
		t.Fatalf("no embedder: got=%v degraded=%v err=%v", got, degraded, err)
	}
	if calls != 0 {
		t.Fatalf("%d graph queries ran, want none", calls)
	}
}

func TestChaos10941KindScopedSemantic_OverCapAndEmbedErrorDegradeWithoutCandidates(t *testing.T) {
	t.Parallel()
	over := make([]row, exactNameCandidateQueryLimit+1)
	for i := range over {
		over[i] = chaos10941Row("repository:x", "x", []float64{1, 0, 0, 0})
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return over, nil
	}}
	adapter := chaos10941Adapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}})
	if got, _, degraded, err := adapter.kindScopedSemanticSearchNodes(context.Background(), "k", "org-1", chaos10941Question, contextfabric.SubjectRepository, 20, temporalFilter{}); err != nil || !degraded || len(got) != 0 {
		t.Fatalf("over cap: got=%d degraded=%v err=%v", len(got), degraded, err)
	}
	failing := chaos10941Adapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}, err: errors.New("embed down")})
	if got, _, degraded, err := failing.kindScopedSemanticSearchNodes(context.Background(), "k", "org-1", chaos10941Question, contextfabric.SubjectRepository, 20, temporalFilter{}); err != nil || !degraded || len(got) != 0 {
		t.Fatalf("embed error: got=%d degraded=%v err=%v", len(got), degraded, err)
	}
}
