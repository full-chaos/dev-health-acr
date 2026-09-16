package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The observation-cover line's own Outcome and Truncated/Failed/Narrowed
// counters tell a genuinely absent source apart from a merely truncated one
// -- `health` unavailable from no_data and `health` narrowed by a
// truncation log DIFFERENT numbers, without a join against the store, even
// though both share observed_kinds=1 served_kinds=0. These pin that.

func TestObservationCoverEventCarriesTheRowsOwnOutcome(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	workload := contractsv1.ContextFabricFactWorkload
	requirement := readRequirement(CompletionQuantifierAtLeastOne)
	threshold, ok := readQuantifierThreshold(requirement.Quantifier)
	if !ok {
		t.Fatal("at_least_one is not a recognised read quantifier")
	}

	for _, tc := range []struct {
		name          string
		coverage      Coverage
		wantOutcome   contractsv1.ContextFabricPlanRequirementOutcome
		wantTruncated int
		wantFailed    int
	}{
		{
			name:        "satisfied: lossless evidence carries no loss counters",
			coverage:    factCoverage(health, SourceAvailable, workload, SourceAvailable),
			wantOutcome: contractsv1.ContextFabricRequirementSatisfied,
		},
		{
			name:          "narrowed by truncation: outcome AND the truncated count both name it",
			coverage:      factCoverage(health, SourceTruncated),
			wantOutcome:   contractsv1.ContextFabricRequirementNarrowed,
			wantTruncated: 1,
		},
		{
			name:        "unavailable: genuinely absent, no truncated count to confuse it with a narrowing",
			coverage:    factCoverage(health, SourceNoData, workload, SourceNoData),
			wantOutcome: contractsv1.ContextFabricRequirementUnavailable,
			wantFailed:  2,
		},
		{
			name:        "unavailable: nothing was planned for either kind at all",
			coverage:    factCoverage(),
			wantOutcome: contractsv1.ContextFabricRequirementUnavailable,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			evidence := evaluateReadRequirement(requirement, tc.coverage, nil)
			row, ok, cover := readRequirementOutcomeRow(requirement, threshold, evidence, readPopulationEvidence{})
			if !ok {
				t.Fatal("readRequirementOutcomeRow published no row")
			}
			if row.Outcome != tc.wantOutcome {
				t.Fatalf("row.Outcome = %q, want %q", row.Outcome, tc.wantOutcome)
			}
			if cover == nil {
				t.Fatal("readRequirementOutcomeRow returned a nil cover event")
			}
			if cover.Outcome != tc.wantOutcome {
				t.Fatalf("cover.Outcome = %q, want %q -- the same decision this call just made for the row", cover.Outcome, tc.wantOutcome)
			}
			if cover.Truncated != tc.wantTruncated {
				t.Fatalf("cover.Truncated = %d, want %d", cover.Truncated, tc.wantTruncated)
			}
			if cover.Failed != tc.wantFailed {
				t.Fatalf("cover.Failed = %d, want %d", cover.Failed, tc.wantFailed)
			}
		})
	}
}

// TestReusedObservationCoverEventCarriesTheStoredOutcome pins the reused-
// answer twin: a STORED document's own assembled-result row names the
// outcome, never re-derived from the reused evidence.
func TestReusedObservationCoverEventCarriesTheStoredOutcome(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	requirement := contractsv1.ContextFabricPlanRequirement{
		Requirement: "state/subject/team", Obligation: string(ObligationState),
		Role: string(SubjectRoleSubject), Subject: SubjectTeam, Kind: string(ObligationKindRead),
		FactKinds: []FactKind{contractsv1.ContextFabricFactHealth}, Scope: string(CompletionScopeSingleSubject),
		Quantifier: string(CompletionQuantifierAtLeastOne),
	}
	result := InvestigationResult{
		AnswerPlan: &AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{requirement}},
		Coverage:   factCoverage(health, SourceTruncated),
		Completeness: contractsv1.ContextFabricAnswerCompleteness{
			Outcomes: []contractsv1.ContextFabricPlanRequirementOutcomeRow{{
				Requirement: requirement.Requirement, Obligation: requirement.Obligation,
				Stage: contractsv1.ContextFabricOutcomeStageAssembledResult,
				Outcome: contractsv1.ContextFabricRequirementNarrowed, Impact: contractsv1.ContextFabricAnswerImpactDepth,
				CauseCoverage: contractsv1.ContextFabricCoverageDetailFactNarrowed, Served: 0, Declared: 1,
			}},
		},
	}
	events := reusedObservationCoverEvents(result, nil)
	if len(events) != 1 {
		t.Fatalf("%d reused observation-cover events, want exactly 1: %+v", len(events), events)
	}
	if events[0].Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("Outcome = %q, want narrowed -- the stored document's own row, not a re-derived value", events[0].Outcome)
	}
	if !events[0].Reused {
		t.Fatal("Reused is false on a reused-answer cover event")
	}
}
