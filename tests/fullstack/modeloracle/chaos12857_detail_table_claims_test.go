package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// CHAOS-12857: a declared detail table reaches a claim only when the claim names it with
// value null (CHAOS-12587). claim_detail_tables claims each named table from every request
// fact that lists it in detail_tables, beside the scalar claims, so a live proof can serve a
// sankey (repository_theme_breakdown) or a sunburst (subcategory_breakdown).
const detailTablesRequest = `{"subject_resolution":{"committed":[{"kind":"team","canonical_id":"team:platform","label":"Platform"}]},
"canonical_facts":[
 {"id":"fact_investment","kind":"investment","subject":{"kind":"team","canonical_id":"team:platform","label":"Platform"},"fields":{"attribution_basis":{"string":"owned_repository"}},"detail_tables":["repository_theme_breakdown","subcategory_breakdown"],"evidence_ref_ids":["ev_1"]},
 {"id":"fact_workload","kind":"workload","subject":{"kind":"team","canonical_id":"team:platform","label":"Platform"},"fields":{"backlog_size":{"integer":7}},"evidence_ref_ids":["ev_2"]}]}`

type oracleClaim struct {
	ClaimID string          `json:"claim_id"`
	FactID  string          `json:"fact_id"`
	Kind    string          `json:"kind"`
	Field   string          `json:"field"`
	Value   json.RawMessage `json:"value"`
	Subject struct {
		CanonicalID string `json:"canonical_id"`
	} `json:"subject"`
}

func detailClaimsOf(t *testing.T, plan *ServiceSynthesis) []string {
	t.Helper()
	reply, err := buildSynthesisReply(&ServiceQuestion{Synthesis: plan}, detailTablesRequest)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Claims []oracleClaim `json:"claimed_facts"`
	}
	if err := json.Unmarshal([]byte(reply), &out); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, claim := range out.Claims {
		got = append(got, claim.ClaimID+" "+claim.FactID+" "+claim.Kind+" "+claim.Subject.CanonicalID+" "+claim.Field+" "+string(claim.Value))
	}
	return got
}

func TestAPlanClaimsANamedDetailTableWithValueNull(t *testing.T) {
	plan := &ServiceSynthesis{Status: "complete", DirectJudgment: "j", CurrentState: "c", DeterministicAnswer: "d", ClaimDetailTables: []string{"repository_theme_breakdown"}}
	got := detailClaimsOf(t, plan)
	// The first-scalar claim stays, and the table's claim follows it: null, on the fact that
	// lists the table, never on a fact that does not.
	want := []string{
		`claim_oracle_01 fact_investment investment team:platform attribution_basis {"string":"owned_repository"}`,
		`claim_oracle_02 fact_investment investment team:platform repository_theme_breakdown {"null":true}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("claims:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// With claim_fields, the named scalars come first and the tables after them.
	plan.ClaimFields = []string{"backlog_size"}
	plan.ClaimDetailTables = []string{"subcategory_breakdown", "repository_theme_breakdown"}
	got = detailClaimsOf(t, plan)
	want = []string{
		`claim_oracle_01 fact_workload workload team:platform backlog_size {"integer":7}`,
		`claim_oracle_02 fact_investment investment team:platform subcategory_breakdown {"null":true}`,
		`claim_oracle_03 fact_investment investment team:platform repository_theme_breakdown {"null":true}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("claims:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAPlanNamingAnUnlistedDetailTableFailsLoudly(t *testing.T) {
	plan := &ServiceSynthesis{Status: "complete", DirectJudgment: "j", CurrentState: "c", DeterministicAnswer: "d", ClaimDetailTables: []string{"completion_distribution"}}
	if _, err := buildSynthesisReply(&ServiceQuestion{Synthesis: plan}, detailTablesRequest); err == nil || !strings.Contains(err.Error(), "completion_distribution") {
		t.Fatalf("a table no fact lists: err = %v, want a loud failure naming it", err)
	}
}

func TestAPlanWithABlankDetailTableIsRefusedAtLoad(t *testing.T) {
	plan := ServicePlan{Questions: []ServiceQuestion{{
		Question:       "Case sankey: how is the Platform team's effort split across its repositories?",
		Interpretation: json.RawMessage(`{"shape":"open"}`),
		Synthesis:      &ServiceSynthesis{Status: "complete", ClaimDetailTables: []string{" "}},
	}}}
	if err := plan.validate(); err == nil || !strings.Contains(err.Error(), "claim_detail_tables") {
		t.Fatalf("a blank table name: err = %v, want the plan refused", err)
	}
}
