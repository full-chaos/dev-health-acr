package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CHAOS-12809: a fact read can hold one fact per scope (a team's workload read holds one
// per work scope), and claim_fields claims the named field on every one of them. With
// claim_first_only a plan claims each named field once, from the first fact in request
// order that holds it as a scalar, so a live proof can claim exactly one scope. The plan
// is read through loadPlan, the path a live oracle uses, so the wire name is pinned too.
func TestAPlanClaimsEachNamedFieldFromItsFirstFactOnly(t *testing.T) {
	request := `{"subject_resolution":{"committed":[{"kind":"team","canonical_id":"team:a","label":"A"}]},
	"canonical_facts":[
	 {"id":"fact_h","kind":"health","subject":{"kind":"team","canonical_id":"team:a","label":"A"},"fields":{"compounding_risk":{"number":0.57}},"evidence_ref_ids":["ev_h"]},
	 {"id":"fact_w1","kind":"workload","subject":{"kind":"team","canonical_id":"team:a","label":"A"},"fields":{"backlog_size":{"integer":10}},"evidence_ref_ids":["ev_w1"]},
	 {"id":"fact_w2","kind":"workload","subject":{"kind":"team","canonical_id":"team:a","label":"A"},"fields":{"backlog_size":{"integer":122},"in_progress":{"integer":3}},"evidence_ref_ids":["ev_w2"]},
	 {"id":"fact_w3","kind":"workload","subject":{"kind":"team","canonical_id":"team:a","label":"A"},"fields":{"backlog_size":{"integer":12},"in_progress":{"integer":7}},"evidence_ref_ids":["ev_w3"]}]}`

	claims := func(t *testing.T, synthesis map[string]any) []string {
		t.Helper()
		plan, err := loadPlan(writeClaimPlan(t, synthesis))
		if err != nil {
			t.Fatalf("loadPlan: %v", err)
		}
		reply, err := buildSynthesisReply(&plan.Service.Questions[0], request)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Claims []struct {
				ClaimID string          `json:"claim_id"`
				FactID  string          `json:"fact_id"`
				Field   string          `json:"field"`
				Value   json.RawMessage `json:"value"`
			} `json:"claimed_facts"`
		}
		if err := json.Unmarshal([]byte(reply), &out); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, claim := range out.Claims {
			got = append(got, claim.ClaimID+" "+claim.FactID+" "+claim.Field+" "+string(claim.Value))
		}
		return got
	}
	check := func(t *testing.T, name string, got, want []string) {
		t.Helper()
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: claims:\n%s\nwant:\n%s", name, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}

	check(t, "one field, first fact only", claims(t, map[string]any{"claim_fields": []string{"backlog_size"}, "claim_first_only": true}), []string{
		`claim_oracle_01 fact_w1 backlog_size {"integer":10}`,
	})
	// Each field from ITS first holder: in_progress is not on fact_w1.
	check(t, "two fields, each from its own first holder", claims(t, map[string]any{"claim_fields": []string{"backlog_size", "in_progress"}, "claim_first_only": true}), []string{
		`claim_oracle_01 fact_w1 backlog_size {"integer":10}`,
		`claim_oracle_02 fact_w2 in_progress {"integer":3}`,
	})
	// Off, the plan keeps claiming the field on every fact that holds it.
	check(t, "claim_first_only off", claims(t, map[string]any{"claim_fields": []string{"backlog_size"}}), []string{
		`claim_oracle_01 fact_w1 backlog_size {"integer":10}`,
		`claim_oracle_02 fact_w2 backlog_size {"integer":122}`,
		`claim_oracle_03 fact_w3 backlog_size {"integer":12}`,
	})

	// First-only still fails loudly on a named field that no fact holds as a scalar.
	plan, err := loadPlan(writeClaimPlan(t, map[string]any{"claim_fields": []string{"backlog_size", "cycle_p50_30d_hours"}, "claim_first_only": true}))
	if err != nil {
		t.Fatalf("loadPlan: %v", err)
	}
	if _, err := buildSynthesisReply(&plan.Service.Questions[0], request); err == nil || !strings.Contains(err.Error(), "cycle_p50_30d_hours") {
		t.Fatalf("first-only with a field no fact holds: err = %v, want a loud failure naming it", err)
	}

	// claim_first_only names no field of its own: without claim_fields it would silently
	// fall back to the first-scalar claim, so the plan is refused at load, naming it.
	if _, err := loadPlan(writeClaimPlan(t, map[string]any{"claim_first_only": true})); err == nil || !strings.Contains(err.Error(), "claim_first_only") {
		t.Fatalf("claim_first_only without claim_fields: err = %v, want a load failure naming claim_first_only", err)
	}
}

// writeClaimPlan writes a minimal service plan whose one question carries the given
// synthesis fields on top of a valid synthesis.
func writeClaimPlan(t *testing.T, extra map[string]any) string {
	t.Helper()
	synthesis := map[string]any{"status": "complete", "direct_judgment": "j", "current_state": "c", "deterministic_answer": "d"}
	for key, value := range extra {
		synthesis[key] = value
	}
	plan := map[string]any{
		"schema_version": "fullstack_model_plan.v1", "task_id": "chaos-12809", "goal": "claim one scope", "repository_slug": "full-chaos/dev-health-acr",
		"service": map[string]any{"questions": []any{map[string]any{
			"question":       "When will the team finish its open work?",
			"interpretation": map[string]any{"shape": "single_subject"},
			"synthesis":      synthesis,
		}}},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
