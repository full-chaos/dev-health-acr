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

// TestCalibratedEntryDriftsLoudlyWithCompositionTag is the codex round-1 P2
// REPLACEMENT for the CHAOS-3835-contact fix's original approach (deriving
// the entry key from EmbedCompositionTag so it auto-followed a template-
// version bump). Auto-following was reversed: this table's calibration is
// measurement-pinned to t2's composed text specifically (see
// calibratedIdentityText2Large's doc comment), so auto-rekeying onto an
// un-measured future composition would silently apply numbers that were
// never validated against it -- trading a silent MISS for an equally silent,
// unvalidated auto-INHERIT.
//
// So the pinned literal does NOT move with the live tag. This test is the
// loud tripwire instead: it independently recomputes what
// EmbedCompositionTag currently produces for this identity and asserts the
// table still has a calibrated entry under that EXACT live string. Today
// (embedTextTemplateVersion == "t2") the live tag and the pinned literal
// agree, so this passes. The day a composition parameter changes -- CHAOS-
// 3835's t2 -> t3 template-version bump, or any future rune-cap/body-gate/
// prefix change -- the live tag stops matching the pinned literal and this
// test fails LOUDLY at integration, exactly the point: it forces an
// explicit human decision (recalibrate against the new composition, or
// record an explicit inheritance decision as a new pinned entry) instead of
// the identity silently falling back to uncalibrated defaults (a silent
// miss) or silently inheriting an unvalidated calibration (a silent
// auto-inherit) -- either of which this test now catches.
func TestCalibratedEntryDriftsLoudlyWithCompositionTag(t *testing.T) {
	liveTag := EmbedCompositionTag(embedprovider.DefaultMaxTextRunes, false, "")
	liveIdentity := "openai/text-embedding-3-large#" + liveTag
	if _, ok := LookupRetrievalPolicy(liveIdentity); !ok {
		t.Fatalf("LookupRetrievalPolicy(%q) ok = false: the live composition tag no longer matches the CHAOS-3834 measurement-pinned entry (%q). "+
			"Composition changed -- recalibrate this identity against the new composition, or record an explicit inheritance decision as a new pinned entry. "+
			"Do not silently repoint the pinned key at the new tag.", liveIdentity, calibratedIdentityText2Large)
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

// TestEmbedderFromEnv_ExplicitSimilarityFloorSurvivesCalibratedTableMatch is
// the codex round-1 P1 fix's pinning test (vector.go's EmbedderFromEnv): an
// operator-set ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR must survive a
// calibrated-identity match, not be silently discarded by the table's
// default. The calibrated table is a DEFAULT per knob, not a forced
// override -- it fills in only where the operator supplied no explicit value
// for that specific knob; measurement-integrity-critical for live harnesses
// that pin their own floor. Mutation check: reverting EmbedderFromEnv's
// explicit-value guard to an unconditional `options.SimilarityFloor =
// policy.SimilarityFloor` makes this test observe 0.30 instead of 0.81 and
// fail.
func TestEmbedderFromEnv_ExplicitSimilarityFloorSurvivesCalibratedTableMatch(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvSimilarityFloor: "0.81",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.SimilarityFloor != 0.81 {
		t.Fatalf("SimilarityFloor = %v, want the explicitly configured 0.81 to survive the calibrated-identity match untouched, not the table's 0.30", options.SimilarityFloor)
	}
	// OverFetchMultiplier and EfRuntime have NO env-configurable source
	// anywhere in embedprovider -- there is no operator value to preserve
	// for either, so the calibrated table stays authoritative for both even
	// when the floor is explicitly pinned.
	if options.EfRuntime != 200 {
		t.Fatalf("EfRuntime = %d, want the calibrated 200 (unaffected by the floor override)", options.EfRuntime)
	}
	if options.OverFetchMultiplier != 0 {
		t.Fatalf("OverFetchMultiplier = %d, want 0 (K unchanged, unaffected by the floor override)", options.OverFetchMultiplier)
	}
}

// TestEmbedderFromEnv_BlankSimilarityFloorEnvIsNotExplicit proves a blank
// (set-but-whitespace) env var does not count as an operator override,
// mirroring embedprovider's own envFloat definition of "explicit" (set AND
// non-blank) -- so the calibrated-identity match still applies the table
// default in that case, exactly like the unset case.
func TestEmbedderFromEnv_BlankSimilarityFloorEnvIsNotExplicit(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvSimilarityFloor: "   ",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.SimilarityFloor != 0.30 {
		t.Fatalf("SimilarityFloor = %v, want the calibrated 0.30 -- a blank env var is not an explicit override", options.SimilarityFloor)
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
