package falkorgraph

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func censusFrame(expression contextfabric.SubjectExpression) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: expression,
		Temporal:          contextfabric.TemporalIntentCurrent,
		Version:           contextfabric.QuestionFrameVersion,
	}
}

// TestCohortExactNameCensusEligibility pins CHAOS-4622 remainder's own
// precedence table, independent of any DiscoverContext fixture wiring --
// the pure function is the unit these mutation-proof, so a future edit that
// swaps a condition or a return value fails here first, fast.
//
// SEAM 7 (CHAOS-4736) re-keyed the table from Shape onto the frame's subject
// expression. Every row below is the SAME case the Shape-keyed version
// tested, restated in the union's vocabulary -- the rows are deliberately
// kept one-for-one so the re-keying can be read as a substitution rather
// than as a new table whose coverage nobody checked. The one added row is
// the outcome the old gate could not produce at all.
func TestCohortExactNameCensusEligibility(t *testing.T) {
	discoveredKind := censusFrame(contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
	})
	groupedMembers := censusFrame(contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionGroupedMembers,
		Grouped: &contextfabric.GroupedSetExpression{
			GroupKind: contextfabric.SubjectTeam, MemberKind: contextfabric.SubjectProject,
		},
	})
	namedSubject := censusFrame(contextfabric.SubjectExpression{
		Kind:  contextfabric.SubjectExpressionNamed,
		Named: &contextfabric.NamedSubjectExpression{Terms: []string{"platform"}},
	})
	organizationScope := censusFrame(contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionOrganizationScope,
		Org:  &contextfabric.OrganizationScopeExpression{},
	})

	tests := []struct {
		name                string
		frame               *contextfabric.QuestionFrame
		scopeAnchorResolved bool
		wantEligible        bool
		wantBasis           CohortExactNameCensusBasis
	}{
		{
			name:                "discovered_kind always eligible regardless of anchor",
			frame:               discoveredKind,
			scopeAnchorResolved: false,
			wantEligible:        true,
			wantBasis:           CohortExactNameCensusBasisDiscoveredKind,
		},
		{
			name:                "discovered_kind eligible even when anchor happens to be set",
			frame:               discoveredKind,
			scopeAnchorResolved: true,
			wantEligible:        true,
			wantBasis:           CohortExactNameCensusBasisDiscoveredKind,
		},
		{
			// CHAOS-4622's remainder fix itself. Its ROOT CAUSE was that
			// Shape was unstable -- a bare-kind-noun cohort survey landed
			// on explicit_cohort some replicates and discovered_cohort
			// others. Reading the union removes the instability at the
			// source; the anchor half of the rule survives unchanged,
			// because "was a subject NAMED" is a different question that
			// the anchor signal still answers.
			name:                "cohort variant with anchor unset is eligible (the fix)",
			frame:               groupedMembers,
			scopeAnchorResolved: false,
			wantEligible:        true,
			wantBasis:           CohortExactNameCensusBasisAnchorUnset,
		},
		{
			// CHAOS-4395's original carve-out, preserved: a genuinely-named
			// cohort ("compare the frontend and backend teams") must never
			// receive the org-wide census.
			name:                "cohort variant with anchor set is NOT eligible (the guard)",
			frame:               groupedMembers,
			scopeAnchorResolved: true,
			wantEligible:        false,
			wantBasis:           CohortExactNameCensusBasisAnchorSet,
		},
		{
			name:                "named_subject is not this gate's concern, no basis reported",
			frame:               namedSubject,
			scopeAnchorResolved: false,
			wantEligible:        false,
			wantBasis:           "",
		},
		{
			// The old table's `open` row. `open` was the Shape a grouped
			// cohort question emitted on the rig, and this gate correctly
			// had nothing to say about it -- but for the wrong reason, since
			// `open` says nothing about topology either way. The frame-side
			// equivalent of "not a set" is organization_scope with nothing
			// to enumerate.
			name:                "organization_scope is not this gate's concern, no basis reported",
			frame:               organizationScope,
			scopeAnchorResolved: true,
			wantEligible:        false,
			wantBasis:           "",
		},
		{
			// ADDED BY SEAM 7, and unreachable in the Shape-keyed version:
			// Shape is always present, so the old gate always had an answer
			// even when that answer came from the least stable field in the
			// interpretation. With no frame there is no topology, and the
			// gate denies and says so.
			name:                "absent frame denies with its own basis",
			frame:               nil,
			scopeAnchorResolved: false,
			wantEligible:        false,
			wantBasis:           CohortExactNameCensusBasisFrameAbsent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEligible, gotBasis := cohortExactNameCensusEligibility(tt.frame, tt.scopeAnchorResolved)
			if gotEligible != tt.wantEligible || gotBasis != tt.wantBasis {
				t.Fatalf("cohortExactNameCensusEligibility(frame, %v) = (%v, %q), want (%v, %q)",
					tt.scopeAnchorResolved, gotEligible, gotBasis, tt.wantEligible, tt.wantBasis)
			}
		})
	}
}
