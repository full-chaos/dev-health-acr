package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestCoverageClampsTrackTheContractRatherThanMirrorIt is the behavioural
// evidence behind reading the two coverage bounds from contracts/v1
// instead of restating them (CHAOS-3746).
//
// Asserting the constants are equal would be a tautology once they are
// derived. What matters is the consequence: a reason longer than the
// contract allows must be clamped to EXACTLY the contract's limit, so the
// composed coverage still validates. Under the old hand-copied literal
// that held only while nobody changed the contract, and the failure was
// severe out of proportion to its cause -- an over-long explanation fails
// ContextFabricCoverage.Validate, which fails the whole investigation.
//
// The lengths here are computed from the contract constants, so this test
// does not need updating when a bound moves; it re-derives and still
// proves the same property.
func TestCoverageClampsTrackTheContractRatherThanMirrorIt(t *testing.T) {
	reasonLimit := contractsv1.ContextFabricSourceObservationReasonMaxLength
	degradedLimit := contractsv1.ContextFabricCoverageDegradedReasonMaxLength

	bundle := CanonicalFactBundle{}
	// Well past both limits, and degrading, so both the reason and the
	// composed degraded entry are exercised in one call.
	appendFactCoverage(&bundle, FactStatus, SourceUnavailable, nil, "", strings.Repeat("r", reasonLimit+500))

	if len(bundle.Coverage.Sources) != 1 {
		t.Fatalf("expected one coverage source, got %d", len(bundle.Coverage.Sources))
	}
	if got := len([]rune(bundle.Coverage.Sources[0].Reason)); got != reasonLimit {
		t.Errorf("clamped reason is %d runes, want the contract's %d", got, reasonLimit)
	}
	if len(bundle.Coverage.DegradedReasons) != 1 {
		t.Fatalf("expected one degraded reason, got %d", len(bundle.Coverage.DegradedReasons))
	}
	if got := len([]rune(bundle.Coverage.DegradedReasons[0])); got != degradedLimit {
		t.Errorf("clamped degraded reason is %d runes, want the contract's %d", got, degradedLimit)
	}

	// The point of the clamp: what it produces must survive the validator
	// it was clamped for.
	coverage := contractsv1.ContextFabricCoverage{
		Sources:         bundle.Coverage.Sources,
		Partial:         bundle.Coverage.Partial,
		DegradedReasons: bundle.Coverage.DegradedReasons,
	}
	if err := coverage.Validate(); err != nil {
		t.Errorf("clamped coverage does not validate, so the clamp failed the whole investigation it exists to save: %v", err)
	}
}
