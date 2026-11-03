package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// CHAOS-12626: a comparison plan claims each side's named measures, copied from the
// request; with no claim_fields the oracle keeps its one first-scalar claim.
func TestAPlanClaimsTheNamedFieldsOfEveryFact(t *testing.T) {
	request := `{"subject_resolution":{"committed":[{"kind":"team","canonical_id":"team:a","label":"A"},{"kind":"team","canonical_id":"team:b","label":"B"}]},
	"canonical_facts":[
	 {"id":"fact_a","kind":"landscape","subject":{"kind":"team","canonical_id":"team:a","label":"A"},"fields":{"as_of_day":{"string":"2026-11-02"},"churn_loc_30d":{"integer":2400},"delivery_units_30d":{"integer":31}},"evidence_ref_ids":["ev_a"]},
	 {"id":"fact_b","kind":"landscape","subject":{"kind":"team","canonical_id":"team:b","label":"B"},"fields":{"as_of_day":{"string":"2026-07-12"},"churn_loc_30d":{"integer":900},"delivery_units_30d":{"integer":12}},"evidence_ref_ids":["ev_b"]}]}`
	plan := &ServiceSynthesis{Status: "complete", DirectJudgment: "j", CurrentState: "c", DeterministicAnswer: "d", ClaimFields: []string{"churn_loc_30d", "delivery_units_30d"}}
	reply, err := buildSynthesisReply(&ServiceQuestion{Synthesis: plan}, request)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Claims []struct {
			ClaimID string          `json:"claim_id"`
			FactID  string          `json:"fact_id"`
			Field   string          `json:"field"`
			Value   json.RawMessage `json:"value"`
			Subject struct {
				CanonicalID string `json:"canonical_id"`
			} `json:"subject"`
		} `json:"claimed_facts"`
	}
	if err := json.Unmarshal([]byte(reply), &out); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, claim := range out.Claims {
		got = append(got, claim.ClaimID+" "+claim.FactID+" "+claim.Subject.CanonicalID+" "+claim.Field+" "+string(claim.Value))
	}
	want := []string{
		`claim_oracle_01 fact_a team:a churn_loc_30d {"integer":2400}`,
		`claim_oracle_02 fact_a team:a delivery_units_30d {"integer":31}`,
		`claim_oracle_03 fact_b team:b churn_loc_30d {"integer":900}`,
		`claim_oracle_04 fact_b team:b delivery_units_30d {"integer":12}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("claims:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	plan.ClaimFields = []string{"cycle_p50_30d_hours"}
	if _, err := buildSynthesisReply(&ServiceQuestion{Synthesis: plan}, request); err == nil || !strings.Contains(err.Error(), "cycle_p50_30d_hours") {
		t.Fatalf("a field no fact holds: err = %v, want a loud failure naming it", err)
	}

	plan.ClaimFields = nil
	reply, err = buildSynthesisReply(&ServiceQuestion{Synthesis: plan}, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(reply), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Claims) != 1 || out.Claims[0].Field != "as_of_day" {
		t.Fatalf("no claim_fields: %+v, want the one first-scalar claim", out.Claims)
	}
}
