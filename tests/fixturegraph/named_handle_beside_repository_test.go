//go:build fixturegraph

package fixturegraph

import (
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
	ask := func(t *testing.T, question, kind string) (any, string) {
		t.Helper()
		interpretation := doc{
			"shape": "single_subject", "requested_judgment": "state of the subject",
			"subject_terms": []string{one}, "time_context": doc{"axis": "current"},
			"fact_requirements": []doc{{"kind": "status"}}, "clarification_needed": false,
		}
		if kind != "" {
			interpretation["requested_subject_kind"] = kind
		}
		args := doc{"question": question, "interpretation": interpretation, "contract": c.interpretContract(), "synthesis": "client", "evidence_window": doc{"relative_id": "trailing_365d"}}
		d, raw := c.call("investigate_with_interpretation", args)
		s := structured(d)
		for _, option := range list(s, "structure_needs", "candidate_options") {
			if str(option, "subject", "kind") != "repository" && !strings.Contains(str(option, "label"), one) {
				continue
			}
			rid := str(s, "result_id")
			args["parent_result_id"] = rid
			args["prior_subject_receipts"] = []doc{{"result_id": rid, "receipt_id": str(option, "receipt_id")}}
			d, raw = c.call("investigate_with_interpretation", args)
			return structured(d), raw
		}
		return s, raw
	}
	committed := func(s any) []string {
		kinds := []string{}
		for _, subject := range list(s, "committed_subjects") {
			kinds = append(kinds, str(subject, "kind"))
		}
		return kinds
	}
	limitations := func(s any) string { return fmt.Sprint(get(s, "limitations")) }
	question := func(number string) string {
		return fmt.Sprintf("What is the state of pull request %s in %s?", number, one)
	}
	t.Run("a lone subject commits beside the repository", func(t *testing.T) {
		s, raw := ask(t, question(lone), "pull_request")
		if got := committed(s); !slices.Contains(got, "pull_request") {
			t.Fatalf("committed = %v, want the pull request: %.3000s", got, raw)
		}
	})
	for name, number := range map[string]string{"no such number": "987654", "a number only the other repository holds": elsewhere} {
		t.Run(name, func(t *testing.T) {
			s, raw := ask(t, question(number), "pull_request")
			if got := committed(s); len(got) != 0 {
				t.Fatalf("committed = %v, want nothing: %.3000s", got, raw)
			}
			if !strings.Contains(limitations(s), "of the requested scope") {
				t.Fatalf("limitations = %s, want the repository's sentence: %.3000s", limitations(s), raw)
			}
		})
	}
	t.Run("a question about the repository keeps its answer", func(t *testing.T) {
		s, raw := ask(t, fmt.Sprintf("How is %s doing since pull request 987654 merged?", one), "repository")
		if got := committed(s); !slices.Equal(got, []string{"repository"}) {
			t.Fatalf("committed = %v, want the repository alone: %.3000s", got, raw)
		}
	})
}
