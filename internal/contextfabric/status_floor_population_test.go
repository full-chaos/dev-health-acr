package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestServedStatusOverTruncatedPopulationWhenModelSaysNoMatch(t *testing.T) {
	cases := []struct {
		name     string
		members  int
		complete bool
		trunc    bool
		want     InvestigationStatus
	}{
		{"truncated population, no member carried", 0, false, true, InvestigationDegraded},
		{"truncated population, members carried", 3, false, true, InvestigationPartial},
		{"complete population, no member carried", 0, true, false, InvestigationNoMatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := newCountingEngine(t, censusCohort(SubjectTeam, tc.members, tc.complete, tc.trunc), countingFrame(SubjectTeam), &recordingTelemetry{})
			inner := engine.synthesizer
			engine.synthesizer = synthesizerFunc(func(ctx context.Context, p storage.Principal, in SynthesisInput) (InvestigationResult, error) {
				draft, err := inner.Synthesize(ctx, p, in)
				draft.Status = InvestigationNoMatch
				return draft, err
			})
			request := validInvestigationRequestWithConfirmedWindow()
			request.RequestID = "request_50210001"
			request.Question = "count question"
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("Investigate: %v", err)
			}
			if result.Status != tc.want {
				t.Fatalf("status = %q, want %q", result.Status, tc.want)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("served result invalid: %v", err)
			}
			if result.Completeness.TerminalStatus != result.Status {
				t.Fatalf("completeness terminal status %q does not follow served status %q", result.Completeness.TerminalStatus, result.Status)
			}
			if said := hasLimitation(result.Limitations, truncatedPopulationNoMemberLimitation); said != (tc.want == InvestigationDegraded) {
				t.Fatalf("truncated-population limitation present = %v for status %q", said, result.Status)
			}
			if withheld := hasLimitation(result.Limitations, synthesisNarrativeWithheldLimitation); withheld != (tc.want != InvestigationNoMatch) {
				t.Fatalf("withheld limitation present = %v for status %q", withheld, result.Status)
			}
		})
	}
}

func TestPopulationOutcomeStatusFloorKeysOnRowAttributesNotObligation(t *testing.T) {
	row := func(obligation string, outcome contractsv1.ContextFabricPlanRequirementOutcome, cause contractsv1.ContextFabricCoverageDetailCode, served int) RequirementOutcomeRow {
		return RequirementOutcomeRow{
			Obligation: obligation, Stage: contractsv1.ContextFabricOutcomeStageAssembledResult,
			Outcome: outcome, CauseCoverage: cause, Served: served, Declared: served,
		}
	}
	truncated := contractsv1.ContextFabricCoverageDetailPopulationTruncated
	narrowed := contractsv1.ContextFabricRequirementNarrowed
	cases := []struct {
		name string
		row  RequirementOutcomeRow
		want InvestigationStatus
	}{
		{"count, zero served", row(string(ObligationCount), narrowed, truncated, 0), InvestigationDegraded},
		{"read population, zero read", row("read_population", narrowed, truncated, 0), InvestigationDegraded},
		{"read population, rows read", row("read_population", narrowed, truncated, 4), InvestigationNoMatch},
		{"zero served, other cause", row(string(ObligationCount), narrowed, contractsv1.ContextFabricCoverageDetailFactNarrowed, 0), InvestigationNoMatch},
		{"zero served, satisfied", row(string(ObligationCount), contractsv1.ContextFabricRequirementSatisfied, truncated, 0), InvestigationNoMatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := affirmationResult()
			r.Status = InvestigationNoMatch
			r.Completeness.Outcomes = []RequirementOutcomeRow{tc.row}
			applyPopulationOutcomeStatusFloor(&r)
			if r.Status != tc.want {
				t.Fatalf("status = %q, want %q", r.Status, tc.want)
			}
		})
	}
}
