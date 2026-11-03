package directread_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// CHAOS-12819: when every candidate of a root refuses at the same stage, the
// reply is the refusal of the candidate whose pinned scope level is the
// query's own, not the first candidate's. On the analytics root the ORG
// entries refuse a TEAM or REPO scope with "scope level TEAM is served by
// investmentBreakdownTeam"; when that entry has already refused the query for
// another reason, naming it again sends the client in a circle.
func TestATieIsAnsweredByTheCandidateOfTheQuerysScope(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	query := func(scope, measure, extra string) string {
		batch := `{breakdowns: [{dimension: THEME, measure: ` + measure + extra + `, dateRange: {startDate: "2026-09-01", endDate: "2026-09-28"}}]`
		if scope != "" {
			batch += `, filters: {scope: ` + scope + `}`
		}
		return `{ analytics(batch: ` + batch + `}) { breakdowns { dimension measure items { key value } } } }`
	}
	team := `{level: TEAM, ids: ["` + opTeamT + `"]}`
	repo := `{level: REPO, ids: ["` + opRepo(opRepoA) + `"]}`
	for _, c := range []struct {
		label, scope, measure, extra string
		code                         directread.RefusalCode
		pathSuffix                   string
	}{
		{"TEAM_measure_not_served", team, "CHURN_LOC", "", directread.RefusalVariableNotAllowed, "breakdowns[*].measure"},
		{"TEAM_topN_out_of_range", team, "COUNT", ", topN: 500", directread.RefusalVariableOutOfRange, "breakdowns[*].topN"},
		{"REPO_measure_not_served", repo, "CHURN_LOC", "", directread.RefusalVariableNotAllowed, "breakdowns[*].measure"},
		// The ORG query already got its own entry's refusal (the first
		// candidate is the ORG entry); it must stay so.
		{"ORG_measure_not_served", "", "CHURN_LOC", "", directread.RefusalVariableNotAllowed, "breakdowns[*].measure"},
	} {
		t.Run(c.label, func(t *testing.T) {
			h.listener.reset()
			resp := h.run(t, opUnrestricted(opOrgA), query(c.scope, c.measure, c.extra), nil)
			h.wantRefused(t, resp, c.code)
			if !strings.HasSuffix(resp.Refusal.Path, c.pathSuffix) {
				t.Fatalf("refusal path %q, want the path of the cause (…%s); reason %q", resp.Refusal.Path, c.pathSuffix, resp.Refusal.Reason)
			}
			if strings.Contains(resp.Refusal.Reason, "is served by") || strings.Contains(resp.Refusal.Reason, "comes from read_facts") {
				t.Fatalf("the reply names another entry instead of the cause: %q", resp.Refusal.Reason)
			}
		})
	}
}
