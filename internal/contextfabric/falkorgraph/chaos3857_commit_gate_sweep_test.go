package falkorgraph

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// This file's tests are additive-only (CHAOS-3857 gate-threshold sweep
// parameterization): they exercise the THREE new env vars
// (EnvCommitLoneFloor/EnvCommitTopFloor/EnvCommitTopGap,
// retrieval_policy.go) and EnvVectorMarginCommitThreshold, on top of the
// EXISTING EmbedderFromEnv/fakeEmbedderEnv fixtures (retrieval_policy_test.go)
// -- no existing test in this package is modified. See
// resolution_gate_policy_test.go (graphrank) for the companion proof that
// ResolveFromMergedCandidates itself is byte-identical to
// ResolveFromMergedCandidatesWithGate(..., DefaultCommitGatePolicy()).

// TestExplicitEnvFloat pins explicitEnvFloat's contract (config.go): unset,
// blank, and unparseable all report (0, false) -- only a genuinely set,
// non-blank, valid float reports (parsed, true). This is the same
// "explicit override wins" precedent EnvSimilarityFloor established
// (vector.go's explicitFloorSet), generalized to a reusable helper.
func TestExplicitEnvFloat(t *testing.T) {
	lookup := func(key string) (string, bool) {
		switch key {
		case "SET_VALID":
			return "0.65", true
		case "SET_BLANK":
			return "   ", true
		case "SET_INVALID":
			return "not-a-number", true
		default:
			return "", false
		}
	}
	for _, tc := range []struct {
		name      string
		key       string
		wantValue float64
		wantOK    bool
	}{
		{"unset", "SET_MISSING", 0, false},
		{"blank", "SET_BLANK", 0, false},
		{"invalid", "SET_INVALID", 0, false},
		{"valid", "SET_VALID", 0.65, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, ok := explicitEnvFloat(lookup, tc.key)
			if ok != tc.wantOK || (ok && value != tc.wantValue) {
				t.Fatalf("explicitEnvFloat(%q) = (%v, %v), want (%v, %v)", tc.key, value, ok, tc.wantValue, tc.wantOK)
			}
		})
	}
}

// TestEmbedderFromEnv_NoCommitGateOverrideLeavesPolicyAtZero is the
// byte-identical-behavior proof for the DEFAULT (no env vars set) case:
// options.CommitGatePolicy must stay at its zero value so
// attachEmbedder/reader.go's existing fallback to
// graphrank.DefaultCommitGatePolicy() applies exactly as it did before this
// parameterization existed.
func TestEmbedderFromEnv_NoCommitGateOverrideLeavesPolicyAtZero(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(nil))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.CommitGatePolicy != (graphrank.CommitGatePolicy{}) {
		t.Fatalf("CommitGatePolicy = %+v, want the zero value when no CHAOS-3857 env var is set", options.CommitGatePolicy)
	}
}

// TestEmbedderFromEnv_PartialCommitGateOverrideKeepsOtherKnobsAtDefault
// proves the per-knob independence CommitGatePolicy's own doc comment
// promises: overriding ONLY LoneFloor must leave TopFloor/TopGap at their
// calibrated defaults (0.88/0.12), never at zero -- a zero TopFloor/TopGap
// would make the top-of-two gate auto-commit on almost any pair, silently
// widening wrong-commit exposure on an axis the operator never asked to
// touch.
func TestEmbedderFromEnv_PartialCommitGateOverrideKeepsOtherKnobsAtDefault(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvCommitLoneFloor: "0.60",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	want := graphrank.CommitGatePolicy{LoneFloor: 0.60, TopFloor: 0.88, TopGap: 0.12}
	if options.CommitGatePolicy != want {
		t.Fatalf("CommitGatePolicy = %+v, want %+v (only LoneFloor overridden, TopFloor/TopGap at calibrated defaults)", options.CommitGatePolicy, want)
	}
}

// TestEmbedderFromEnv_AllThreeCommitGateVarsOverride proves all three knobs
// compose independently when all are set.
func TestEmbedderFromEnv_AllThreeCommitGateVarsOverride(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvCommitLoneFloor: "0.60",
		EnvCommitTopFloor:  "0.80",
		EnvCommitTopGap:    "0.05",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	want := graphrank.CommitGatePolicy{LoneFloor: 0.60, TopFloor: 0.80, TopGap: 0.05}
	if options.CommitGatePolicy != want {
		t.Fatalf("CommitGatePolicy = %+v, want %+v", options.CommitGatePolicy, want)
	}
}

// TestEmbedderFromEnv_BlankCommitGateEnvIsNotExplicit mirrors
// TestEmbedderFromEnv_BlankSimilarityFloorEnvIsNotExplicit for the new
// vars: a blank (set-but-whitespace) value must not count as an override.
func TestEmbedderFromEnv_BlankCommitGateEnvIsNotExplicit(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvCommitLoneFloor: "   ",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.CommitGatePolicy != (graphrank.CommitGatePolicy{}) {
		t.Fatalf("CommitGatePolicy = %+v, want the zero value -- a blank env var is not an explicit override", options.CommitGatePolicy)
	}
}

// TestEmbedderFromEnv_VectorMarginOverrideInstallsAtCalibratedTau proves
// EnvVectorMarginCommitThreshold replaces the shipped M when the effective
// floor equals the calibrated tau (0.30 for the fixture identity) -- the
// same population the tau-equality guard already lets M install for.
func TestEmbedderFromEnv_VectorMarginOverrideInstallsAtCalibratedTau(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvVectorMarginCommitThreshold: "0.10",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.VectorMarginCommitThreshold != 0.10 {
		t.Fatalf("VectorMarginCommitThreshold = %v, want the explicit override 0.10", options.VectorMarginCommitThreshold)
	}
	if options.CalibratedTopK != 20 {
		t.Fatalf("CalibratedTopK = %d, want the calibrated 20 (unaffected by the M override)", options.CalibratedTopK)
	}
}

// TestEmbedderFromEnv_VectorMarginOverrideDoesNotForceInstallOnTauDivergence
// proves the override does NOT bypass the existing tau-equality guard: with
// an explicit SimilarityFloor that diverges from the calibrated 0.30, M
// (and CalibratedTopK) must stay at their disabled zero value even though
// EnvVectorMarginCommitThreshold is also set -- exactly the guarantee
// EnvVectorMarginCommitThreshold's own doc comment makes (retrieval_policy.go).
func TestEmbedderFromEnv_VectorMarginOverrideDoesNotForceInstallOnTauDivergence(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvSimilarityFloor: "0.81",
		EnvVectorMarginCommitThreshold:   "0.10",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.VectorMarginCommitThreshold != 0 {
		t.Fatalf("VectorMarginCommitThreshold = %v, want 0 (disabled) -- an explicit M override must not reinstall M once the effective floor has diverged from the calibrated tau", options.VectorMarginCommitThreshold)
	}
	if options.CalibratedTopK != 0 {
		t.Fatalf("CalibratedTopK = %d, want 0 (disabled) for the same reason", options.CalibratedTopK)
	}
}
