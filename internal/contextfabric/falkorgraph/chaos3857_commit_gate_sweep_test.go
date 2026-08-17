package falkorgraph

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// This file's tests are additive-only (CHAOS-3857 gate-threshold sweep
// parameterization): they exercise the THREE new commit-gate env vars
// (EnvCommitLoneFloor/EnvCommitTopFloor/EnvCommitTopGap, retrieval_policy.go)
// and EnvVectorMarginCommitThreshold, on top of the EXISTING
// EmbedderFromEnv/fakeEmbedderEnv fixtures (retrieval_policy_test.go) -- no
// existing test in this package is modified. See resolution_gate_policy_test.go
// (graphrank) for the companion proofs: ResolveFromMergedCandidates is
// byte-identical to ResolveFromMergedCandidatesWithGate(...,
// DefaultCommitGatePolicy()), and the EVALUATOR's own fail-closed behavior
// for an invalid policy (this file only proves the ENV BOUNDARY's loud
// rejection -- sol review F1 asked for both layers tested independently).

// TestExplicitEnvFloat pins explicitEnvFloat's CORRECTED contract (config.go,
// sol review F2): unset/blank is silently "not an override" (0, false, nil);
// a SET, non-blank value that fails to parse (or parses to NaN/Inf) is a
// loud ERROR (0, false, err), never a silent fallback -- matching
// EnvSimilarityFloor's own precedent exactly (garbage there aborts
// composition via embedprovider.ConfigFromEnv's envFloat, never falls back).
func TestExplicitEnvFloat(t *testing.T) {
	lookup := func(key string) (string, bool) {
		switch key {
		case "SET_VALID":
			return "0.65", true
		case "SET_BLANK":
			return "   ", true
		case "SET_INVALID":
			return "not-a-number", true
		case "SET_NAN":
			return "NaN", true
		default:
			return "", false
		}
	}
	for _, tc := range []struct {
		name      string
		key       string
		wantValue float64
		wantOK    bool
		wantErr   bool
	}{
		{"unset", "SET_MISSING", 0, false, false},
		{"blank", "SET_BLANK", 0, false, false},
		{"invalid", "SET_INVALID", 0, false, true},
		{"nan", "SET_NAN", 0, false, true},
		{"valid", "SET_VALID", 0.65, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, ok, err := explicitEnvFloat(lookup, tc.key)
			if (err != nil) != tc.wantErr {
				t.Fatalf("explicitEnvFloat(%q) err = %v, want error=%v", tc.key, err, tc.wantErr)
			}
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
// touch. 0.60 is deliberately still <= the default TopFloor (0.88), so
// Validate() accepts it (LoneFloor must not exceed TopFloor).
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
// compose independently when all are set (and the combination is valid).
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

// TestEmbedderFromEnv_GarbageCommitGateEnvErrorsLoudly is sol review F2's
// core proof: a set, non-blank, unparseable commit-gate value must fail
// EmbedderFromEnv outright (composition aborts at startup), never silently
// fall back to the calibrated default the way an earlier version of this
// parameterization did.
func TestEmbedderFromEnv_GarbageCommitGateEnvErrorsLoudly(t *testing.T) {
	_, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvCommitLoneFloor: "not-a-number",
	}))
	if err == nil {
		t.Fatal("EmbedderFromEnv() error = nil, want a loud error for a garbage commit-gate env value")
	}
	if !strings.Contains(err.Error(), EnvCommitLoneFloor) {
		t.Fatalf("EmbedderFromEnv() error = %v, want it to name %s", err, EnvCommitLoneFloor)
	}
}

// TestEmbedderFromEnv_OutOfRangeCommitGateEnvErrorsLoudly proves a
// parseable-but-out-of-range value (>1) is ALSO a loud error, not merely
// unparseable strings -- CommitGatePolicy.Validate()'s (0, 1] bound is
// enforced at this boundary too.
func TestEmbedderFromEnv_OutOfRangeCommitGateEnvErrorsLoudly(t *testing.T) {
	_, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvCommitTopFloor: "1.5",
	}))
	if err == nil {
		t.Fatal("EmbedderFromEnv() error = nil, want a loud error for TopFloor > 1")
	}
}

// TestEmbedderFromEnv_CrossFieldInvalidCommitGateErrorsLoudlyNamingBothFields
// is sol review F1's exact partial-zero scenario, now caught at the ENV
// boundary rather than only inside the evaluator: LoneFloor=0.95 alone,
// with TopFloor left at its calibrated default 0.88, produces a resolved
// policy where LoneFloor > TopFloor -- individually each field is in
// (0, 1], so only the CROSS-FIELD check catches it. The error must name
// both fields (sol review F2's explicit requirement).
func TestEmbedderFromEnv_CrossFieldInvalidCommitGateErrorsLoudlyNamingBothFields(t *testing.T) {
	_, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvCommitLoneFloor: "0.95",
	}))
	if err == nil {
		t.Fatal("EmbedderFromEnv() error = nil, want a loud error: LoneFloor=0.95 exceeds the default TopFloor=0.88")
	}
	if !strings.Contains(err.Error(), "LoneFloor") || !strings.Contains(err.Error(), "TopFloor") {
		t.Fatalf("EmbedderFromEnv() error = %v, want it to name BOTH LoneFloor and TopFloor", err)
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
// proves the NUMERIC override does NOT bypass the existing tau-equality
// guard: with an explicit SimilarityFloor that diverges from the
// calibrated 0.30, M (and CalibratedTopK) must stay at their disabled zero
// value even though EnvVectorMarginCommitThreshold is also set.
func TestEmbedderFromEnv_VectorMarginOverrideDoesNotForceInstallOnTauDivergence(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvSimilarityFloor: "0.81",
		EnvVectorMarginCommitThreshold:   "0.10",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.VectorMarginCommitThreshold != 0 {
		t.Fatalf("VectorMarginCommitThreshold = %v, want 0 (disabled) -- a NUMERIC M override must not reinstall M once the effective floor has diverged from the calibrated tau", options.VectorMarginCommitThreshold)
	}
	if options.CalibratedTopK != 0 {
		t.Fatalf("CalibratedTopK = %d, want 0 (disabled) for the same reason", options.CalibratedTopK)
	}
}

// TestEmbedderFromEnv_VectorMarginZeroIsRejected is sol review F2b's
// exact ruling: M=0 as a NUMERIC value is the "always-fire footgun" (a
// margin is never negative by construction, so a 0 threshold rescues
// every corroborated, in-envelope case) -- it must be REJECTED, not
// silently accepted as "disabled".
func TestEmbedderFromEnv_VectorMarginZeroIsRejected(t *testing.T) {
	_, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvVectorMarginCommitThreshold: "0",
	}))
	if err == nil {
		t.Fatal(`EmbedderFromEnv() error = nil, want a loud error for EnvVectorMarginCommitThreshold=0 (use "disabled" instead)`)
	}
}

// TestEmbedderFromEnv_VectorMarginNegativeIsRejected proves the rejection
// covers the whole non-positive range, not just the literal 0.
func TestEmbedderFromEnv_VectorMarginNegativeIsRejected(t *testing.T) {
	_, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvVectorMarginCommitThreshold: "-0.01",
	}))
	if err == nil {
		t.Fatal("EmbedderFromEnv() error = nil, want a loud error for a negative EnvVectorMarginCommitThreshold")
	}
}

// TestEmbedderFromEnv_VectorMarginDisabledSentinelTurnsCarveOutFullyOff is
// sol review F2b's true "carve-out off" isolation cell: the literal string
// "disabled" must zero VectorMarginCommitThreshold UNCONDITIONALLY --
// proved here at the calibrated tau (0.30, where a numeric override WOULD
// install), the case most likely to accidentally leave M installed if the
// unconditional-disable wiring were ever narrowed to only the
// tau-divergent branch.
func TestEmbedderFromEnv_VectorMarginDisabledSentinelTurnsCarveOutFullyOff(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		EnvVectorMarginCommitThreshold: "disabled",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.VectorMarginCommitThreshold != 0 {
		t.Fatalf(`VectorMarginCommitThreshold = %v, want 0 -- "disabled" must turn the carve-out fully off even at the calibrated tau`, options.VectorMarginCommitThreshold)
	}
}
