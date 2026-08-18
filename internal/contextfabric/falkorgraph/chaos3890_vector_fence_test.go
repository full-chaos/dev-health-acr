package falkorgraph

import (
	"context"
	"strings"
	"testing"
)

// CHAOS-3890 (audit H4): ensureVectorReadable's read-path fence collapsed
// 5+ distinct causes -- index absent, index not operational, dimension
// mismatch, stale-or-foreign embedder_identity, a query error inspecting
// any of the above -- into one bare bool, surfaced only as a generic
// RecordVectorRetrievalDegraded(orgID) with no reason. A lexical-only pool
// caused by a WRONG embedder identity (foreign, untrustworthy vectors)
// therefore looked identical, on that one signal, to "vectors healthy,
// genuinely nothing there" -- degraded fires the same way for either.
//
// This test proves the two are now distinguishable through
// RecordVectorFence's reason enum, without changing hybridSearchNodes'
// returned candidates/degraded value at all (PURE ADDITIVE).

// TestVectorFence_IdentityMismatchDistinguishableFromGenuineNoSignal is the
// core CHAOS-3890 pin.
func TestVectorFence_IdentityMismatchDistinguishableFromGenuineNoSignal(t *testing.T) {
	t.Run("wrong embedder identity", func(t *testing.T) {
		fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.Contains(cypher, propEmbedderIdentity) {
				// A node stamped with a DIFFERENT embedder identity exists --
				// the fence's own "foreign vectors" finding.
				return []row{{"n.canonical_id": "p9"}}, nil
			}
			// The base fulltext query and the vector query itself: no rows
			// either way, since the fence must reject BEFORE the vector
			// query ever runs.
			return nil, nil
		}}
		fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
			return []indexStatus{operationalVectorIndex(8)}, nil
		}
		telemetry := &recordingTelemetry{}
		adapter := vectorAdapterWithTelemetry(t, fake, &stubEmbedder{vector: make([]float32, 8)}, telemetry)

		candidates, _, degraded, err := adapter.hybridSearchNodes(context.Background(), "k", "org-1", "auth", 5, &resolutionFence{}, temporalFilter{})
		if err != nil {
			t.Fatalf("hybridSearchNodes() error = %v", err)
		}
		if !degraded {
			t.Fatal("a fence rejected on identity mismatch must still report degraded=true (unchanged existing behavior)")
		}
		if len(candidates) != 0 {
			t.Fatalf("candidates = %#v, want none (vector arm never ran, lexical arm found nothing)", candidates)
		}
		if len(telemetry.vectorFences) != 1 {
			t.Fatalf("vectorFences = %#v, want exactly 1 recorded fence check", telemetry.vectorFences)
		}
		if got := telemetry.vectorFences[0].result; got != VectorFenceIdentityMismatch {
			t.Fatalf("RecordVectorFence result = %q, want %q -- this is the distinguishing signal RecordVectorRetrievalDegraded alone could never carry", got, VectorFenceIdentityMismatch)
		}
	})

	t.Run("genuinely no vector signal", func(t *testing.T) {
		fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			// The identity probe finds nothing foreign: the fence passes.
			// The vector search itself (a real, healthy call) also finds
			// nothing -- there is simply no close-enough neighbor in the
			// corpus for this query. This is NOT a degradation.
			return nil, nil
		}}
		fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
			return []indexStatus{operationalVectorIndex(8)}, nil
		}
		telemetry := &recordingTelemetry{}
		adapter := vectorAdapterWithTelemetry(t, fake, &stubEmbedder{vector: make([]float32, 8)}, telemetry)

		candidates, _, degraded, err := adapter.hybridSearchNodes(context.Background(), "k", "org-1", "auth", 5, &resolutionFence{}, temporalFilter{})
		if err != nil {
			t.Fatalf("hybridSearchNodes() error = %v", err)
		}
		if degraded {
			t.Fatal("a passed fence with a genuinely empty vector result must NOT report degraded=true")
		}
		if len(candidates) != 0 {
			t.Fatalf("candidates = %#v, want none", candidates)
		}
		if len(telemetry.vectorFences) != 1 {
			t.Fatalf("vectorFences = %#v, want exactly 1 recorded fence check", telemetry.vectorFences)
		}
		if got := telemetry.vectorFences[0].result; got != VectorFenceOK {
			t.Fatalf("RecordVectorFence result = %q, want %q -- the fence itself is healthy here, unlike the identity-mismatch case above", got, VectorFenceOK)
		}
		if telemetry.vectorFences[0].memoized {
			t.Fatal("the first fence check in a fresh resolutionFence must not report memoized=true")
		}
	})
}

// TestVectorFence_MemoizedAcrossTermsInOneResolution pins the OTHER half of
// the vector_fence signal's contract: a resolution's SECOND (and later)
// Search call reuses the SAME probe verdict (resolutionFence's own
// documented cost bound), and RecordVectorFence must say so via
// memoized=true rather than silently reporting a "fresh" probe that never
// actually ran again.
func TestVectorFence_MemoizedAcrossTermsInOneResolution(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	telemetry := &recordingTelemetry{}
	adapter := vectorAdapterWithTelemetry(t, fake, &stubEmbedder{vector: make([]float32, 8)}, telemetry)

	fence := &resolutionFence{}
	for _, term := range []string{"auth", "login", "session"} {
		if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org-1", term, 5, fence, temporalFilter{}); err != nil {
			t.Fatalf("hybridSearchNodes(%q): %v", term, err)
		}
	}

	if len(telemetry.vectorFences) != 3 {
		t.Fatalf("vectorFences = %#v, want exactly 3 (one per term, all reported)", telemetry.vectorFences)
	}
	if telemetry.vectorFences[0].memoized {
		t.Fatal("the FIRST term's fence check must report memoized=false (it actually probed)")
	}
	for i, rec := range telemetry.vectorFences[1:] {
		if !rec.memoized {
			t.Fatalf("term %d's fence check = %#v, want memoized=true (reused the first probe)", i+1, rec)
		}
		if rec.result != VectorFenceOK {
			t.Fatalf("term %d's fence result = %q, want %q (same memoized verdict as the first)", i+1, rec.result, VectorFenceOK)
		}
	}
}
