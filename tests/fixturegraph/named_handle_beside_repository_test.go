//go:build fixturegraph

package fixturegraph

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A question that names a handle beside a repository is answered about that handle,
// or says why not (CHAOS-12523). The fixture names a project and a repository alike, so
// the service asks first, and a client confirms the repository receipt: that is the
// exact-hint path, as a real client takes it.
func TestANamedHandleBesideARepository(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("investigate_with_interpretation")
	one, two := slugs(t)
	org := sqlStr(orgID(t))
	lone := chScalar(t, fmt.Sprintf(`SELECT toString(number) FROM git_pull_requests FINAL WHERE org_id = %[1]s AND repo_id = (SELECT id FROM repos FINAL WHERE org_id = %[1]s AND repo = %[2]s) AND number NOT IN (SELECT number FROM git_pull_requests FINAL WHERE org_id = %[1]s AND repo_id = (SELECT id FROM repos FINAL WHERE org_id = %[1]s AND repo = %[3]s)) ORDER BY number DESC LIMIT 1`, org, sqlStr(one), sqlStr(two)))
	elsewhere := chScalar(t, fmt.Sprintf(`SELECT toString(number) FROM git_pull_requests FINAL WHERE org_id = %[1]s AND repo_id = (SELECT id FROM repos FINAL WHERE org_id = %[1]s AND repo = %[2]s) ORDER BY number DESC LIMIT 1`, org, sqlStr(two)))
	if lone == "" || elsewhere == "" {
		t.Fatalf("the worlds hold no such numbers: %q %q", lone, elsewhere)
	}
	ask := func(t *testing.T, question, kind string) (doc, string) {
		t.Helper()
		interpretation := doc{
			"shape": "single_subject", "requested_judgment": "state of the subject",
			"subject_terms": []string{one}, "time_context": doc{"axis": "current"},
			"fact_requirements": []doc{{"kind": "status"}}, "clarification_needed": false,
			"question_frame": doc{
				"goals": []string{"assess_state"}, "temporal": "current",
				"subject_expression": doc{"kind": "named_subject", "terms": []string{one}},
			},
		}
		if kind != "" {
			interpretation["requested_subject_kind"] = kind
		}
		args := doc{"question": question, "interpretation": interpretation, "contract": c.interpretContract(), "synthesis": "client", "evidence_window": doc{"relative_id": "trailing_365d"}}
		d, raw := c.call("investigate_with_interpretation", args)
		s := structured(d)
		for _, option := range list(s, "structure_needs", "candidate_options") {
			if str(option, "kind") != "repository" {
				continue
			}
			rid := str(s, "result_id")
			args["parent_result_id"] = rid
			args["prior_candidate_receipts"] = []doc{{"result_id": rid, "receipt_id": str(option, "receipt_id")}}
			d, raw = c.call("investigate_with_interpretation", args)
			return d, raw
		}
		return d, raw
	}
	committed := func(s any) []string {
		kinds := []string{}
		for _, subject := range list(s, "committed_subjects") {
			kinds = append(kinds, str(subject, "kind"))
		}
		return kinds
	}
	// On a client synthesis turn a statistical commit waits for the answer to affirm
	// it, so the subjects the service committed are read from the synthesis input.
	toSynthesis := func(d doc) string {
		encoded, _ := json.Marshal(synthesisInputOf(d))
		return string(encoded)
	}
	limitations := func(s any) string { return fmt.Sprint(get(s, "limitations")) }
	question := func(number string) string {
		return fmt.Sprintf("What is the state of pull request %s in %s?", number, one)
	}
	t.Run("a lone subject commits beside the repository", func(t *testing.T) {
		d, raw := ask(t, question(lone), "pull_request")
		if got := toSynthesis(d); !strings.Contains(got, "pull_request:") || !strings.Contains(got, "repository:") {
			t.Fatalf("synthesis input holds %d bytes and not both subjects: %.3000s", len(got), raw)
		}
	})
	// Both worlds hold every number of the second, so no number lives in the second
	// alone: the real-store test pins a number another repository holds.
	t.Run("no such number", func(t *testing.T) {
		d, raw := ask(t, question("987654"), "pull_request")
		s := structured(d)
		if got := committed(s); len(got) != 0 {
			t.Fatalf("committed = %v, want nothing: %.3000s", got, raw)
		}
		if !strings.Contains(limitations(s), "of the requested scope") {
			t.Fatalf("limitations = %s, want the repository's sentence: %.3000s", limitations(s), raw)
		}
	})
	t.Run("a question about the repository keeps its answer", func(t *testing.T) {
		d, raw := ask(t, fmt.Sprintf("How is %s doing since pull request 987654 merged?", one), "repository")
		if got := committed(structured(d)); !slices.Equal(got, []string{"repository"}) {
			t.Fatalf("committed = %v, want the repository alone: %.3000s", got, raw)
		}
	})
}
