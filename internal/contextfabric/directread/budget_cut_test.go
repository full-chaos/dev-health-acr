package directread

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// slowBudget is the one-fact-at-a-time cut: the reference applyBudget must
// match for every budget.
func slowBudget(response *FactsResponse, maxBytes int) {
	size := func() int {
		encoded, _ := json.Marshal(response)
		return len(encoded)
	}
	if size() <= maxBytes {
		return
	}
	truncation := &Truncation{TruncatedBy: TruncatedByMaxBytes}
	response.Truncation = truncation
	response.Status = StatusPartial
	for len(response.Facts) > 0 && size() > maxBytes {
		dropped := response.Facts[len(response.Facts)-1]
		response.Facts = response.Facts[:len(response.Facts)-1]
		truncation.FactsOmitted++
		for index := range response.Coverage {
			row := &response.Coverage[index]
			if row.Kind == dropped.Kind && row.Subject.Kind == dropped.Subject.Kind && row.Subject.CanonicalID == dropped.Subject.CanonicalID && row.Outcome != OutcomeWithheldBudget {
				row.Outcome = OutcomeWithheldBudget
			}
		}
		for _, root := range dropped.ReachedFrom {
			if reachedFactServed(response.Facts, dropped.Kind, root) {
				continue
			}
			for index := range response.Coverage {
				row := &response.Coverage[index]
				if row.Kind == dropped.Kind && row.Subject.Kind == root.Kind && row.Subject.CanonicalID == root.CanonicalID && servedOutcome(row.Outcome) {
					row.Outcome = OutcomeWithheldBudget
				}
			}
		}
	}
	truncation.CoverageOverBudget = size() > maxBytes
}

func budgetFixture() FactsResponse {
	response := FactsResponse{Facts: []ServedFact{}, Coverage: []CoverageRow{}}
	for root := 0; root < 4; root++ {
		response.Coverage = append(response.Coverage, CoverageRow{Kind: "blockers", Subject: RequestSubject{Kind: "team", CanonicalID: fmt.Sprintf("team:%d", root)}, Outcome: OutcomeFactServed})
	}
	for i := 0; i < 6; i++ {
		root := fmt.Sprintf("team:%d", i%4)
		response.Facts = append(response.Facts, ServedFact{
			Kind:        "blockers",
			Subject:     ServedSubject{Kind: "work_item", CanonicalID: fmt.Sprintf("work_item:%03d", i), Label: strings.Repeat("<x>", i%7)},
			Fields:      map[string]any{"n": fmt.Sprint(i), "note": strings.Repeat("é", i%11)},
			ReachedFrom: []ReachedFrom{{Kind: "team", CanonicalID: root, Basis: "direct"}},
			Provenance:  Provenance{Source: "s", SourceVersion: "v", SourceState: "available", NaturalKeys: []NaturalKey{}},
		})
	}
	return response
}

func TestApplyBudgetCutsExactlyLikeTheOneFactAtATimeCut(t *testing.T) {
	full, _ := json.Marshal(budgetFixture())
	empty := budgetFixture()
	empty.Facts = nil
	floor, _ := json.Marshal(empty)
	// Every integer budget from below the coverage-only size (every fact
	// cut, coverage over budget) to above the full size (nothing cut).
	for budget := len(floor) - 20; budget <= len(full)+10; budget++ {
		fast, slow := budgetFixture(), budgetFixture()
		applyBudget(&fast, budget)
		slowBudget(&slow, budget)
		if !reflect.DeepEqual(fast, slow) {
			t.Fatalf("budget %d: fast kept %d facts, slow %d; coverage %+v vs %+v", budget, len(fast.Facts), len(slow.Facts), fast.Coverage, slow.Coverage)
		}
	}
}
