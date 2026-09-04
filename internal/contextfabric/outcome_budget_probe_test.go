package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Measurement probe for the S7c phase-1 report: the CHAOS-4754 request shape.
// Single subject, no cohort, 18 resolution candidates, 12 single-table
// time_series claimed facts, 5 drivers. Prints the per-collection item split
// and the serialized byte size so the numbers in the report are MEASURED, not
// asserted from a hypothesis.
func TestProbe4754Shape(t *testing.T) {
	build := func(candidates, facts, drivers, rowsPerFact int) contractsv1.ContextFabricInvestigationResult {
		result := contractsv1.ContextFabricInvestigationResult{
			Status:              "complete",
			DirectJudgment:      "Delivery throughput for the team has held roughly flat across the window, with a mild decline in the most recent two periods.",
			CurrentState:        "Throughput is within the band observed over the full window.",
			DeterministicAnswer: "Throughput held roughly flat over the requested window.",
			StrongestPressures:  []string{},
			RemainingWork:       []contractsv1.ContextFabricFinding{},
			ReadinessGaps:       []contractsv1.ContextFabricFinding{},
			Conflicts:           []contractsv1.ContextFabricFinding{},
			Paths:               []contractsv1.ContextFabricRelationshipPath{},
			Limitations:         []string{},
			Warnings:            []string{},
			EvidenceRefIDs:      []string{},
		}
		for i := 0; i < candidates; i++ {
			result.SubjectResolution.Candidates = append(result.SubjectResolution.Candidates, contractsv1.ContextFabricSubjectCandidate{
				ReceiptID: "rcpt_candidate_000000" + string(rune('a'+i%26)),
				Subject: contractsv1.ContextFabricSubjectRef{
					Kind: "team", CanonicalID: "org:linear:TEAM" + string(rune('A'+i%26)), Label: "Team " + string(rune('A'+i%26)),
				},
				State:        "candidate",
				MatchReasons: []string{"Name matched the requested subject term."},
				Confidence:   0.62,
			})
		}
		for i := 0; i < facts; i++ {
			fact := contractsv1.ContextFabricClaimedFact{
				ClaimID: "claim_workload_00" + string(rune('a'+i%26)),
				Kind:    "workload",
				Subject: contractsv1.ContextFabricSubjectRef{Kind: "team", CanonicalID: "org:linear:CHAOS", Label: "CHAOS"},
				Field:   "throughput_items_closed",
				Value:   contractsv1.ContextFabricScalarValue{Number: ptrFloat(float64(12 + i))},
			}
			for r := 0; r < rowsPerFact; r++ {
				fact.TimeSeriesRows = append(fact.TimeSeriesRows, contractsv1.ContextFabricClaimedFactRow{
					Fields: map[string]contractsv1.ContextFabricScalarValue{
						"period": {String: ptrStr("2026-W" + string(rune('0'+(r/10)%10)) + string(rune('0'+r%10)))},
						"value":  {Number: ptrFloat(float64(8 + r))},
					},
				})
			}
			result.ClaimedFacts = append(result.ClaimedFacts, fact)
		}
		for i := 0; i < drivers; i++ {
			result.Drivers = append(result.Drivers, contractsv1.ContextFabricDriverJudgment{
				DriverID: "driver_00" + string(rune('a'+i%26)),
				Standing: "active", Category: "delivery_flow",
				Title:            "Cycle time widened in the most recent periods",
				Summary:          "Items closed per period fell while items opened held steady, widening the queue.",
				AffectedSubjects: []contractsv1.ContextFabricSubjectRef{{Kind: "team", CanonicalID: "org:linear:CHAOS", Label: "CHAOS"}},
				EvidenceRefIDs:   []string{"ev_0001", "ev_0002"},
				Derivation:       "fact_comparison", EpistemicStatus: "supported", Confidence: 0.71, Current: true,
			})
		}
		return result
	}

	type shape struct {
		name                                  string
		candidates, facts, drivers, rowsPerFa int
	}
	for _, s := range []shape{
		{"4754 BEFORE (as filed: 18 cand + 12 facts + 5 drivers)", 18, 12, 5, 12},
		{"4754 BEFORE, 26 rows/fact (all_time weekly)", 18, 12, 5, 26},
		{"AFTER candidates dropped to 0", 0, 12, 5, 12},
		{"AFTER candidates capped at 5", 5, 12, 5, 12},
		{"AFTER candidates 0 + drivers 3", 0, 12, 3, 12},
		{"minimal: 0 cand, 1 fact, 0 drivers", 0, 1, 0, 12},
	} {
		result := build(s.candidates, s.facts, s.drivers, s.rowsPerFa)
		m, err := contractsv1.MeasureContextFabricResponse(result)
		if err != nil {
			t.Fatalf("%s: measure: %v", s.name, err)
		}
		t.Logf("%-46s items{cand=%d drivers=%d facts=%d rw=%d gaps=%d confl=%d members=%d paths=%d} budgeted=%d bytes=%d",
			s.name, m.Items.Candidates, m.Items.Drivers, m.Items.ClaimedFacts, m.Items.RemainingWork,
			m.Items.ReadinessGaps, m.Items.Conflicts, m.Items.CohortMembers, m.Items.Paths,
			m.Items.Budgeted(), m.Bytes)
	}
}

func ptrFloat(v float64) *float64 { return &v }

func ptrStr(v string) *string { return &v }
