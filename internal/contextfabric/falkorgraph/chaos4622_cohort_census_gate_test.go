package falkorgraph

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestCohortExactNameCensusEligibility pins CHAOS-4622 remainder's own
// precedence table, independent of any DiscoverContext fixture wiring --
// the pure function is the unit these mutation-proof, so a future edit that
// swaps a condition or a return value fails here first, fast.
func TestCohortExactNameCensusEligibility(t *testing.T) {
	tests := []struct {
		name                string
		shape               contextfabric.InvestigationShape
		scopeAnchorResolved bool
		wantEligible        bool
		wantBasis           CohortExactNameCensusBasis
	}{
		{
			name:                "discovered_cohort always eligible regardless of anchor",
			shape:               contextfabric.ShapeDiscoveredCohort,
			scopeAnchorResolved: false,
			wantEligible:        true,
			wantBasis:           CohortExactNameCensusBasisDiscoveredCohortShape,
		},
		{
			name:                "discovered_cohort eligible even when anchor happens to be set",
			shape:               contextfabric.ShapeDiscoveredCohort,
			scopeAnchorResolved: true,
			wantEligible:        true,
			wantBasis:           CohortExactNameCensusBasisDiscoveredCohortShape,
		},
		{
			// This is CHAOS-4622's remainder fix itself: a bare-kind-noun
			// cohort survey that landed on explicit_cohort (Shape's own
			// instability) with no named subject must still get the
			// reliable census, not the noisy fulltext-only fallback.
			name:                "explicit_cohort with anchor unset is eligible (the fix)",
			shape:               contextfabric.ShapeExplicitCohort,
			scopeAnchorResolved: false,
			wantEligible:        true,
			wantBasis:           CohortExactNameCensusBasisAnchorUnset,
		},
		{
			// This is CHAOS-4395's original carve-out, preserved: a
			// genuinely-named cohort ("compare the frontend and backend
			// teams") must never receive the org-wide census.
			name:                "explicit_cohort with anchor set is NOT eligible (the guard)",
			shape:               contextfabric.ShapeExplicitCohort,
			scopeAnchorResolved: true,
			wantEligible:        false,
			wantBasis:           CohortExactNameCensusBasisAnchorSet,
		},
		{
			name:                "single_subject is not this gate's concern, no basis reported",
			shape:               contextfabric.ShapeSingleSubject,
			scopeAnchorResolved: false,
			wantEligible:        false,
			wantBasis:           "",
		},
		{
			name:                "open shape is not this gate's concern, no basis reported",
			shape:               contextfabric.ShapeOpen,
			scopeAnchorResolved: true,
			wantEligible:        false,
			wantBasis:           "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEligible, gotBasis := cohortExactNameCensusEligibility(tt.shape, tt.scopeAnchorResolved)
			if gotEligible != tt.wantEligible || gotBasis != tt.wantBasis {
				t.Fatalf("cohortExactNameCensusEligibility(%v, %v) = (%v, %q), want (%v, %q)",
					tt.shape, tt.scopeAnchorResolved, gotEligible, gotBasis, tt.wantEligible, tt.wantBasis)
			}
		})
	}
}
