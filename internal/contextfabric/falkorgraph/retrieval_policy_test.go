package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

// TestRetrievalPolicyVersionBumpedByCHAOS3834 pins the CHAOS-3834 version
// bump literally: this is a fail-pre proof by construction -- run against
// the pre-CHAOS-3834 tree (RetrievalPolicyVersion = "rp1"), this test
// fails outright.
func TestRetrievalPolicyVersionBumpedByCHAOS3834(t *testing.T) {
	if RetrievalPolicyVersion != "rp2" {
		t.Fatalf("RetrievalPolicyVersion = %q, want rp2 (CHAOS-3834 added a calibrated policy-table entry, which per spec §6 T4's rule must bump the constant)", RetrievalPolicyVersion)
	}
}

// TestLookupRetrievalPolicy_KnownIdentityReturnsCalibratedEntry proves the
// shipped openai/text-embedding-3-large entry is reachable by exactly the
// identity string form the write path stamps (identity.String()+"#"+tag).
func TestLookupRetrievalPolicy_KnownIdentityReturnsCalibratedEntry(t *testing.T) {
	policy, ok := LookupRetrievalPolicy("openai/text-embedding-3-large#t2:r2000:b0:pnone")
	if !ok {
		t.Fatal("LookupRetrievalPolicy() ok = false, want true for the calibrated CHAOS-3834 entry")
	}
	if policy.SimilarityFloor <= 0 || policy.SimilarityFloor >= embedprovider.DefaultSimilarityFloor {
		t.Fatalf("SimilarityFloor = %v, want a recall-gate value strictly below the uncalibrated default %v", policy.SimilarityFloor, embedprovider.DefaultSimilarityFloor)
	}
	if policy.EfRuntime != 200 {
		t.Fatalf("EfRuntime = %d, want the CHAOS-3832 knee value 200", policy.EfRuntime)
	}
}

// TestLookupRetrievalPolicy_UnknownIdentityKeepsConservativeDefault proves
// an uncalibrated identity -- any identity string with no table entry, be
// it a different model, a different dimension, or a different composition
// tag on the SAME model -- reports found=false and the zero RetrievalPolicy,
// which every caller must treat as "change nothing".
func TestLookupRetrievalPolicy_UnknownIdentityKeepsConservativeDefault(t *testing.T) {
	for _, identity := range []string{
		"openai/text-embedding-3-small#t2:r2000:b0:pnone",
		"lmstudio/nomic-embed-text#t2:r2000:b0:pnomic",
		// Same model, DIFFERENT composition tag -- must NOT match the
		// calibrated entry; a policy is scoped to one exact composition.
		"openai/text-embedding-3-large#t3:r2000:b0:pnone",
		"openai/text-embedding-3-large#t2:r2000:b1:pnone",
		"none",
	} {
		policy, ok := LookupRetrievalPolicy(identity)
		if ok {
			t.Errorf("LookupRetrievalPolicy(%q) ok = true, want false (no calibrated entry)", identity)
		}
		if policy != (RetrievalPolicy{}) {
			t.Errorf("LookupRetrievalPolicy(%q) = %+v, want the zero value on a miss", identity, policy)
		}
	}
}

// TestCalibratedEntryKeyMatchesLiveCompositionTag pins the CHAOS-3835 contact
// fix: the shipped calibrated entry's key must be DERIVED from
// EmbedCompositionTag, the same authority composition.go's write path and
// read fence use, not a hand-typed literal that could silently drift out of
// sync with a future template-version bump (e.g. CHAOS-3835's t2 -> t3).
// This test recomputes the live tag independently and asserts the table
// actually has an entry under it -- if a future edit reverts
// calibratedIdentityText2Large to a literal, or the template version moves
// without this key following it, LookupRetrievalPolicy would report
// found=false and this test fails loudly instead of the identity silently
// falling back to uncalibrated defaults in production.
func TestCalibratedEntryKeyMatchesLiveCompositionTag(t *testing.T) {
	liveTag := EmbedCompositionTag(embedprovider.DefaultMaxTextRunes, false, "")
	wantKey := "openai/text-embedding-3-large#" + liveTag
	if calibratedIdentityText2Large != wantKey {
		t.Fatalf("calibratedIdentityText2Large = %q, want %q (derived from the live EmbedCompositionTag builder)", calibratedIdentityText2Large, wantKey)
	}
	if _, ok := LookupRetrievalPolicy(wantKey); !ok {
		t.Fatalf("LookupRetrievalPolicy(%q) ok = false, want true -- the calibrated entry must be reachable under the CURRENT composition tag, not a stale one", wantKey)
	}
}

func fakeEmbedderEnv(overrides map[string]string) func(string) (string, bool) {
	base := map[string]string{
		embedprovider.EnvBaseURL:   "https://embed.example/v1/",
		embedprovider.EnvProvider:  "openai",
		embedprovider.EnvModel:     "text-embedding-3-large",
		embedprovider.EnvDimension: "3072",
	}
	for k, v := range overrides {
		base[k] = v
	}
	return func(key string) (string, bool) { value, ok := base[key]; return value, ok }
}

// TestEmbedderFromEnv_CalibratedIdentityOverridesDefaults wires
// LookupRetrievalPolicy end to end through EmbedderFromEnv: a deployment
// whose provider/model/composition resolve to the calibrated identity gets
// the policy's tau and efRuntime instead of embedprovider's single global
// defaults, with K left at "unchanged" (0) exactly as the shipped entry
// specifies.
func TestEmbedderFromEnv_CalibratedIdentityOverridesDefaults(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(nil))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.SimilarityFloor != 0.30 {
		t.Fatalf("SimilarityFloor = %v, want the calibrated 0.30, not the uncalibrated default %v", options.SimilarityFloor, embedprovider.DefaultSimilarityFloor)
	}
	if options.EfRuntime != 200 {
		t.Fatalf("EfRuntime = %d, want the calibrated 200", options.EfRuntime)
	}
	if options.OverFetchMultiplier != 0 {
		t.Fatalf("OverFetchMultiplier = %d, want 0 (K unchanged per the shipped entry)", options.OverFetchMultiplier)
	}
}

// TestEmbedderFromEnv_UncalibratedIdentityKeepsConservativeDefaults proves
// an identity with no policy entry (a different model here) is completely
// unaffected by CHAOS-3834's wiring: SimilarityFloor stays whatever
// embedprovider resolved (its own default here), and the two new fields
// stay zero.
func TestEmbedderFromEnv_UncalibratedIdentityKeepsConservativeDefaults(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvModel: "text-embedding-3-small",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.SimilarityFloor != embedprovider.DefaultSimilarityFloor {
		t.Fatalf("SimilarityFloor = %v, want the uncalibrated default %v unchanged", options.SimilarityFloor, embedprovider.DefaultSimilarityFloor)
	}
	if options.EfRuntime != 0 {
		t.Fatalf("EfRuntime = %d, want 0 (no calibrated policy => server default applies)", options.EfRuntime)
	}
	if options.OverFetchMultiplier != 0 {
		t.Fatalf("OverFetchMultiplier = %d, want 0", options.OverFetchMultiplier)
	}
}

// TestEnsureVectorIndex_AppliesPolicyEfRuntimeOnlyAtCreate proves the
// documented RetrievalPolicy.EfRuntime contract: a NEW (absent) index picks
// up the calibrated efRuntime in its CREATE OPTIONS clause, but an EXISTING
// index at the right dimension is left completely untouched -- no CREATE,
// no DROP -- regardless of what a.efRuntime is. FalkorDB's pinned module has
// no per-query efRuntime (CHAOS-3832 §7 D3), so there is nothing else this
// value could apply to; a running organization's index only ever changes
// efRuntime through the explicit operational rebuild path (recreateVectorIndexWithOptions),
// never as a side effect of ensureVectorIndex noticing a policy value.
func TestEnsureVectorIndex_AppliesPolicyEfRuntimeOnlyAtCreate(t *testing.T) {
	t.Run("absent index: efRuntime is written into CREATE OPTIONS", func(t *testing.T) {
		var createdCypher string
		fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.Contains(cypher, "CREATE VECTOR INDEX") {
				createdCypher = cypher
			}
			return nil, nil
		}}
		fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
			if createdCypher == "" {
				return nil, nil // absent until created
			}
			return []indexStatus{{
				Label: labelSubject, EntityType: "NODE", Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
			}}, nil
		}
		adapter := newFakeAdapter(t, fake)
		adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: make([]float32, 8)}, SimilarityFloor: 0.55, EfRuntime: 200})
		if err := adapter.ensureVectorIndex(context.Background(), "graphkey"); err != nil {
			t.Fatalf("ensureVectorIndex: %v", err)
		}
		if !strings.Contains(createdCypher, "efRuntime:200") {
			t.Fatalf("CREATE VECTOR INDEX cypher = %q, want it to carry efRuntime:200", createdCypher)
		}
	})

	t.Run("existing index at the right dimension: untouched regardless of policy efRuntime", func(t *testing.T) {
		var wrote bool
		fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.Contains(cypher, "CREATE VECTOR INDEX") || strings.Contains(cypher, "DROP VECTOR INDEX") {
				wrote = true
			}
			return nil, nil
		}}
		fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
			return []indexStatus{{
				Label: labelSubject, EntityType: "NODE", Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
			}}, nil
		}
		adapter := newFakeAdapter(t, fake)
		adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: make([]float32, 8)}, SimilarityFloor: 0.55, EfRuntime: 200})
		if err := adapter.ensureVectorIndex(context.Background(), "graphkey"); err != nil {
			t.Fatalf("ensureVectorIndex: %v", err)
		}
		if wrote {
			t.Fatal("an already-existing, matching-dimension index must not be recreated just because a.efRuntime is set -- that is the operator-driven rebuild's job, not ensureVectorIndex's")
		}
	})
}
