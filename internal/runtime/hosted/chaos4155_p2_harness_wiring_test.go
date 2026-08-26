package hosted_test

import (
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
)

// TestWireProductionEnv_ConfirmedKindVectorCensusMaxComparisons_TrialPrefixedInputReachesCensusConfig
// is CHAOS-4155 Phase 2's harness-enablement red-first proof.
//
// Background: #267 (CHAOS-4155 Phase 1) shipped the shadow kind-scoped
// vector completeness census gated behind a single production env var,
// falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons
// (ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS), but
// never added a wireProductionEnv set() line for it -- unlike every other
// production ACR_CONTEXT_FABRIC_* knob this shared trial harness function
// wires (see the ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED and
// falkorgraph.EnvCommitLoneFloor-and-friends precedents just above/below
// this one). Two independent facts combine to make the census
// unreachable from any trial run before this fix:
//
//  1. clearAmbientACREnv unconditionally strips every ACR_-prefixed
//     ambient env var that is neither ACR_TEST_TRIAL_*-prefixed nor on
//     acrEnvIsolationAllowlist -- and this var is neither, so a bare
//     operator export of ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS
//     before launching a trial script is wiped before the resolution
//     engine ever starts.
//  2. wireProductionEnv had no set() call re-deriving it from an
//     ACR_TEST_TRIAL_-prefixed source the way every other trial-input
//     knob is threaded, so there was no surviving path left either.
//
// This test proves both halves directly: an ambient (bare-prefixed) value
// is gone after wireProductionEnv runs, and the ACR_TEST_TRIAL_-prefixed
// value is what the census's own config key reads back as -- RED before
// the fix (the key reads back empty, not the trial-prefixed value), GREEN
// after (the one set() line added below the falkorgraph.EnvCommitLoneFloor
// block in wireProductionEnv).
func TestWireProductionEnv_ConfirmedKindVectorCensusMaxComparisons_TrialPrefixedInputReachesCensusConfig(t *testing.T) {
	// stubOtherRequiredTrialInputs sets every OTHER wireProductionEnv-required
	// ACR_TEST_TRIAL_ input -- none of these are exercised (wireProductionEnv
	// is pure env-var wiring, no I/O), they only need to be non-empty so
	// requireEnv doesn't Fatalf. modelOverridden=true skips the
	// MODEL/_API_KEY requirement entirely (chaos3884_replay_harness_test.go's
	// own precedent for a non-model-path caller).
	stubOtherRequiredTrialInputs := func(t *testing.T) {
		t.Helper()
		for key, value := range map[string]string{
			"ACR_TEST_TRIAL_POSTGRES_DSN":    "postgres://stub/stub",
			"ACR_TEST_TRIAL_CLICKHOUSE_DSN":  "clickhouse://stub/stub",
			"ACR_TEST_TRIAL_FALKOR_ADDR":     "stub:6379",
			"ACR_TEST_TRIAL_EMBED_MODEL":     "text-embedding-3-large",
			"ACR_TEST_TRIAL_EMBED_DIMENSION": "3072",
			"ACR_TEST_TRIAL_EMBED_API_KEY":   "stub-key",
		} {
			t.Setenv(key, value)
		}
	}

	t.Run("trial-prefixed input reaches the census config key", func(t *testing.T) {
		stubOtherRequiredTrialInputs(t)

		// Ambient (bare-prefixed) leak: simulates an operator's own leftover
		// export in the launching shell, or a stray direnv-loaded value --
		// exactly the class clearAmbientACREnv exists to neutralize.
		t.Setenv(falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons, "999999")
		// The explicit, trial-prefixed source: what a CHAOS-4155 Phase 2
		// measurement run actually sets to turn the shadow arm on.
		const wantMaxComparisons = "12345"
		t.Setenv("ACR_TEST_TRIAL_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS", wantMaxComparisons)

		wireProductionEnv(t, true)

		got := os.Getenv(falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons)
		if got != wantMaxComparisons {
			t.Fatalf("after wireProductionEnv, %s = %q, want %q (the trial-prefixed input) -- the ambient value (999999) must never survive, and the trial-prefixed value must reach the census's own config key",
				falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons, got, wantMaxComparisons)
		}
	})

	// codex R1 (Low, confirmed): the case above proves the trial-prefixed
	// input WINS over an ambient leak, but never independently proves an
	// ambient-only leak is actually stripped when the trial-prefixed input
	// is unset (a run that never opts in must NOT silently inherit
	// whatever the calling shell happened to have exported). This
	// subtest closes that gap.
	t.Run("ambient-only leak is stripped when the trial-prefixed input is unset", func(t *testing.T) {
		stubOtherRequiredTrialInputs(t)

		t.Setenv(falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons, "999999")
		// Deliberately NOT setting ACR_TEST_TRIAL_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS.

		wireProductionEnv(t, true)

		if got := os.Getenv(falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons); got != "" {
			t.Fatalf("after wireProductionEnv with no trial-prefixed input, %s = %q, want empty -- an ambient-only leak (999999) must never survive clearAmbientACREnv on its own",
				falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons, got)
		}
	})
}
