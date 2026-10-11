//go:build fixturegraph

package fixturegraph

import (
	"fmt"
	"strings"
	"testing"
)

// Use-case: "which team owns project P?" names the team of the open ownership row.
func TestProjectOwnerQuestionNamesTheOwningTeam(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("investigate_with_interpretation")
	org := sqlStr(orgID(t))
	rows := ch(t, fmt.Sprintf(`SELECT p.name, t.name FROM team_project_ownership AS o FINAL
INNER JOIN projects AS p FINAL ON p.org_id = o.org_id AND p.id = o.project_id
INNER JOIN teams AS t FINAL ON t.org_id = o.org_id AND t.id = o.team_id
WHERE o.org_id = %[1]s AND o.valid_to IS NULL AND p.is_active = 1 AND t.is_active = 1
AND o.project_id IN (SELECT project_id FROM team_project_ownership FINAL WHERE org_id = %[1]s AND valid_to IS NULL GROUP BY project_id HAVING uniqExact(team_id) = 1)
ORDER BY p.name LIMIT 1`, org))
	if len(rows) != 1 {
		t.Fatalf("no active project with exactly one open owner in the seeded worlds: %v", rows)
	}
	project, team := rows[0][0], rows[0][1]
	args := doc{
		"question":        fmt.Sprintf("Which team owns the project %s?", project),
		"expected_kinds":  []string{"project"},
		"evidence_window": doc{"relative_id": "trailing_365d"},
		"interpretation": doc{
			"shape": "single_subject", "requested_judgment": "the team that owns the project",
			"subject_terms": []string{project}, "requested_subject_kind": "project",
			"time_context": doc{"axis": "current"}, "fact_requirements": []doc{{"kind": "identity"}},
			"clarification_needed": false,
		},
		"contract":  c.interpretContract(),
		"synthesis": "client",
	}
	d, raw := c.call("investigate_with_interpretation", args)
	if str(structured(d), "status") == "clarification_required" {
		for _, o := range list(structured(d), "structure_needs", "kind_options") {
			if str(o, "kind") == "project" {
				rid := str(structured(d), "result_id")
				args["parent_result_id"] = rid
				args["prior_kind_receipts"] = []doc{{"result_id": rid, "receipt_id": str(o, "receipt_id")}}
				d, raw = c.call("investigate_with_interpretation", args)
				break
			}
		}
	}
	if !strings.Contains(raw, team) {
		t.Fatalf("answer for project %q never names its owning team %q: status=%s\n%.3000s", project, team, str(structured(d), "status"), raw)
	}
}
