package falkorgraph

import (
	"bytes"
	"context"
	"log/slog"
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
// identity string form the write path stamps (identity.String()+"#"+tag) AT
// its measured dimension (3072). No opt-in flag gates this (codex round-8
// P1, REVISED -- chris overruled an initial env-flag ruling): the exact-
// identity pinning IS the safety mechanism, see LookupRetrievalPolicy's doc
// comment.
func TestLookupRetrievalPolicy_KnownIdentityReturnsCalibratedEntry(t *testing.T) {
	policy, ok := LookupRetrievalPolicy("openai/text-embedding-3-large#t2:r2000:b0:pnone", 3072)
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
// an uncalibrated (identity, dimension) pair -- a different model, a
// different composition tag on the SAME model, or (codex round-3 P1) the
// SAME identity+tag at a DIFFERENT dimension -- reports found=false and the
// zero RetrievalPolicy, which every caller must treat as "change nothing".
func TestLookupRetrievalPolicy_UnknownIdentityKeepsConservativeDefault(t *testing.T) {
	for _, tc := range []struct {
		identity  string
		dimension int
	}{
		{"openai/text-embedding-3-small#t2:r2000:b0:pnone", 3072},
		{"lmstudio/nomic-embed-text#t2:r2000:b0:pnomic", 768},
		// Same model, DIFFERENT composition tag -- must NOT match the
		// calibrated entry; a policy is scoped to one exact composition.
		{"openai/text-embedding-3-large#t3:r2000:b0:pnone", 3072},
		{"openai/text-embedding-3-large#t2:r2000:b1:pnone", 3072},
		// Same model, SAME composition tag, DIFFERENT dimension (codex
		// round-3 P1) -- a BYO endpoint truncating text-embedding-3-large
		// to 1536 via OpenAI's `dimensions` param must NOT silently
		// inherit the 3072-measured calibration.
		{"openai/text-embedding-3-large#t2:r2000:b0:pnone", 1536},
		{"none", 0},
	} {
		policy, ok := LookupRetrievalPolicy(tc.identity, tc.dimension)
		if ok {
			t.Errorf("LookupRetrievalPolicy(%q, %d) ok = true, want false (no calibrated entry)", tc.identity, tc.dimension)
		}
		if policy != (RetrievalPolicy{}) {
			t.Errorf("LookupRetrievalPolicy(%q, %d) = %+v, want the zero value on a miss", tc.identity, tc.dimension, policy)
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
// table still has a calibrated entry under that EXACT live string AT the
// measured dimension (3072, codex round-3 P1). Today (embedTextTemplateVersion
// == "t2") the live tag and the pinned literal agree, so this passes. The
// day a composition parameter changes -- CHAOS-3835's t2 -> t3
// template-version bump, or any future rune-cap/body-gate/prefix change --
// the live tag stops matching the pinned literal and this test fails
// LOUDLY at integration, exactly the point: it forces an explicit human
// decision (recalibrate against the new composition, or record an explicit
// inheritance decision as a new pinned entry) instead of the identity
// silently falling back to uncalibrated defaults (a silent miss) or
// silently inheriting an unvalidated calibration (a silent auto-inherit) --
// either of which this test now catches. The companion assertion below
// proves the SAME live identity+tag at a DIFFERENT dimension does NOT
// resolve -- the key-shape change itself is covered, not just the
// composition-tag drift the original test targeted.
func TestCalibratedEntryDriftsLoudlyWithCompositionTag(t *testing.T) {
	liveTag := EmbedCompositionTag(embedprovider.DefaultMaxTextRunes, false, "")
	liveIdentity := "openai/text-embedding-3-large#" + liveTag
	if _, ok := LookupRetrievalPolicy(liveIdentity, 3072); !ok {
		t.Fatalf("LookupRetrievalPolicy(%q, 3072) ok = false: the live composition tag no longer matches the CHAOS-3834 measurement-pinned entry (%q). "+
			"Composition changed -- recalibrate this identity against the new composition, or record an explicit inheritance decision as a new pinned entry. "+
			"Do not silently repoint the pinned key at the new tag.", liveIdentity, calibratedIdentityText2Large)
	}
	if _, ok := LookupRetrievalPolicy(liveIdentity, 1536); ok {
		t.Fatalf("LookupRetrievalPolicy(%q, 1536) ok = true, want false -- the calibrated entry is pinned to dimension 3072 and must not match a different width", liveIdentity)
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
// specifies. No opt-in flag gates this (codex round-8 P1, REVISED -- chris
// overruled an initial env-flag ruling): the exact-identity pinning IS the
// safety mechanism.
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

// TestEmbedderFromEnv_MismatchedDimensionKeepsConservativeDefaults is the
// codex round-3 P1 fix's end-to-end pinning test: the SAME provider/model/
// composition as the calibrated entry, but a DIFFERENT configured dimension
// (1536 instead of the measured 3072 -- e.g. a BYO endpoint truncating
// text-embedding-3-large via OpenAI's `dimensions` param). EmbedderFromEnv
// must NOT apply the 3072-measured tau/efRuntime to this deployment.
func TestEmbedderFromEnv_MismatchedDimensionKeepsConservativeDefaults(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvDimension: "1536",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.SimilarityFloor != embedprovider.DefaultSimilarityFloor {
		t.Fatalf("SimilarityFloor = %v, want the uncalibrated default %v -- the 3072-measured calibration must not apply at dimension 1536", options.SimilarityFloor, embedprovider.DefaultSimilarityFloor)
	}
	if options.EfRuntime != 0 {
		t.Fatalf("EfRuntime = %d, want 0 (no calibrated policy at this dimension => server default applies)", options.EfRuntime)
	}
	if options.OverFetchMultiplier != 0 {
		t.Fatalf("OverFetchMultiplier = %d, want 0", options.OverFetchMultiplier)
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

// captureDefaultSlog swaps slog.Default() for a handler writing to a buffer
// for the duration of fn, mirroring modelprovider's
// TestGenkitDebugLoggingNeverCarriesGenerationContentOnACRsPath -- ACR calls
// slog.Default() directly at every Warn call site in this package (reader.go,
// oracle.go, and now vector_projection.go), never slog.SetDefault, so this is
// the only way a test observes what gets logged.
func captureDefaultSlog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(previous)
	fn()
	return buf.String()
}

// TestEnsureVectorIndex_WarnsWhenExistingIndexEfRuntimeDiffersFromPolicy is
// the codex round-8 P2 Fix B(3) pinning test: FalkorDB's db.indexes()
// introspection already exposes the built efRuntime (indexStatus.HNSWOptions,
// conn.go), so a policy/actual mismatch on an EXISTING, OPERATIONAL index is
// detectable -- ensureVectorIndex must log a Warn about it (detection, never
// a compare-and-recreate) rather than staying silent about the gap the
// mandatory CHAOS-3832/3835 rebuild is meant to close.
func TestEnsureVectorIndex_WarnsWhenExistingIndexEfRuntimeDiffersFromPolicy(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{{
			Label: labelSubject, EntityType: "NODE", Status: "OPERATIONAL",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8), "efRuntime": int64(10)}},
		}}, nil
	}
	adapter := newFakeAdapter(t, fake)
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: make([]float32, 8)}, SimilarityFloor: 0.55, EfRuntime: 200})

	logged := captureDefaultSlog(t, func() {
		if err := adapter.ensureVectorIndex(context.Background(), "graphkey"); err != nil {
			t.Fatalf("ensureVectorIndex: %v", err)
		}
	})
	if !strings.Contains(logged, "efRuntime does not match") {
		t.Fatalf("log output = %q, want a Warn about the built index's efRuntime (10) disagreeing with the calibrated policy (200)", logged)
	}
}

// TestEnsureVectorIndex_NoWarnWhenExistingIndexEfRuntimeMatchesPolicy is the
// negative control: the SAME shape as the test above, but the built index's
// efRuntime already equals the policy's -- nothing to warn about.
func TestEnsureVectorIndex_NoWarnWhenExistingIndexEfRuntimeMatchesPolicy(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{{
			Label: labelSubject, EntityType: "NODE", Status: "OPERATIONAL",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8), "efRuntime": int64(200)}},
		}}, nil
	}
	adapter := newFakeAdapter(t, fake)
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: make([]float32, 8)}, SimilarityFloor: 0.55, EfRuntime: 200})

	logged := captureDefaultSlog(t, func() {
		if err := adapter.ensureVectorIndex(context.Background(), "graphkey"); err != nil {
			t.Fatalf("ensureVectorIndex: %v", err)
		}
	})
	if strings.Contains(logged, "efRuntime does not match") {
		t.Fatalf("log output = %q, want no efRuntime-mismatch Warn -- the built index already matches the policy", logged)
	}
}

// TestEnsureVectorIndex_NoWarnWhenNoCalibratedEfRuntimePolicy is the
// companion negative control for a.efRuntime==0 (no calibrated policy at
// all for this identity): nothing to compare against, so no Warn regardless
// of what the existing index reports.
func TestEnsureVectorIndex_NoWarnWhenNoCalibratedEfRuntimePolicy(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{{
			Label: labelSubject, EntityType: "NODE", Status: "OPERATIONAL",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8), "efRuntime": int64(10)}},
		}}, nil
	}
	adapter := newFakeAdapter(t, fake)
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: make([]float32, 8)}, SimilarityFloor: 0.55}) // EfRuntime: 0 -- no calibrated policy

	logged := captureDefaultSlog(t, func() {
		if err := adapter.ensureVectorIndex(context.Background(), "graphkey"); err != nil {
			t.Fatalf("ensureVectorIndex: %v", err)
		}
	})
	if strings.Contains(logged, "efRuntime does not match") {
		t.Fatalf("log output = %q, want no efRuntime-mismatch Warn -- a.efRuntime is 0 (no calibrated policy), nothing to compare against", logged)
	}
}
