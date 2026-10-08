//go:build fixturegraph

package fixturegraph

import (
	"fmt"
	"strings"
	"testing"
)

// archivedOwner returns the team that owns an archived project through an open ownership row,
// and the names of the projects it owns and of those among them that the source archived.
func archivedOwner(t *testing.T) (teamID, teamName string, owned, archived map[string]bool) {
	t.Helper()
	org := sqlStr(orgID(t))
	rows := ch(t, fmt.Sprintf(`SELECT t.id, t.name FROM teams AS t FINAL
WHERE t.org_id = %[1]s AND t.id IN (SELECT team_id FROM team_project_ownership FINAL WHERE org_id = %[1]s AND valid_to IS NULL AND project_id IN (SELECT id FROM projects FINAL WHERE org_id = %[1]s AND is_active = 0))
ORDER BY t.id LIMIT 1`, org))
	if len(rows) != 1 {
		t.Fatalf("no team owns an archived project in the seeded worlds: %v", rows)
	}
	teamID, teamName = rows[0][0], rows[0][1]
	owned, archived = map[string]bool{}, map[string]bool{}
	for _, r := range ch(t, fmt.Sprintf(`SELECT name, toString(is_active) FROM projects FINAL WHERE org_id = %[1]s AND id IN (SELECT project_id FROM team_project_ownership FINAL WHERE org_id = %[1]s AND valid_to IS NULL AND team_id = %[2]s)`, org, sqlStr(teamID))) {
		owned[r[0]] = true
		if r[1] == "0" {
			archived[r[0]] = true
		}
	}
	if len(archived) == 0 || len(owned) == 0 {
		t.Fatalf("team %s owns %d projects, %d archived", teamID, len(owned), len(archived))
	}
	return teamID, teamName, owned, archived
}

func teamSubjectID(t *testing.T, c *client, name string) string {
	t.Helper()
	d, _ := c.call("find_subjects", doc{"query": name, "kinds": []string{"team"}})
	var ids []string
	for _, s := range list(d, "subjects") {
		if str(s, "label") == name && str(s, "kind") == "team" {
			ids = append(ids, str(s, "canonical_id"))
		}
	}
	if len(ids) != 1 {
		t.Fatalf("find_subjects %q returned %d teams named so, want 1: %v", name, len(ids), d)
	}
	return ids[0]
}

// Use-case: a team's projects. A project the source archived is still owned: the open
// ownership row decides, so find_subjects owned_by lists it.
func TestOwnedByListsAnArchivedOwnedProject(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("find_subjects")
	_, teamName, owned, _ := archivedOwner(t)
	d, raw := c.call("find_subjects", doc{"owned_by": teamSubjectID(t, c, teamName), "kinds": []string{"project"}, "limit": 200})
	got := map[string]bool{}
	for _, s := range list(d, "subjects") {
		if str(s, "kind") == "project" {
			got[str(s, "label")] = true
		}
	}
	if diffSets(owned, got) != diffSets(owned, owned) {
		t.Fatalf("owned_by projects of %s differ from the open ownership rows: %s\n%.1500s", teamName, diffSets(owned, got), raw)
	}
}

func teamProjectsInterpretation(team string) doc {
	return doc{
		"shape": "explicit_cohort", "requested_judgment": "list the projects the team owns",
		"subject_terms": []string{team}, "scope_anchor_term": team, "scope_anchor_kind": "team",
		"requested_subject_kind": "project", "time_context": doc{"axis": "current"},
		"fact_requirements": []doc{{"kind": "identity"}}, "clarification_needed": false,
		"question_frame": doc{
			"goals": []string{"assess_state"}, "temporal": "current",
			"subject_expression": doc{"kind": "children_of_scope", "anchor_terms": []string{team}, "member_kind": "project"},
		},
	}
}

// Use-case: "which projects does team T own?" serves every owned project, and a project the
// source archived says so on its row.
func TestTeamProjectsServeAnArchivedOwnedProjectFlaggedArchived(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("investigate_with_interpretation")
	_, teamName, owned, archived := archivedOwner(t)
	args := doc{
		"question":       fmt.Sprintf("Which projects does team %s own?", teamName),
		"interpretation": teamProjectsInterpretation(teamName),
		"contract":       c.interpretContract(),
		"synthesis":      "client",
		"budget":         doc{"max_cohort_members": 250, "max_evidence_refs": 500, "max_serialized_bytes": 1048576},
	}
	d, raw := c.call("investigate_with_interpretation", args)
	got := memberLabels(d)
	if diffSets(owned, got) != diffSets(owned, owned) {
		t.Fatalf("team %s projects differ from the open ownership rows: %s\n%.2500s", teamName, diffSets(owned, got), raw)
	}
	for _, mem := range list(structured(d), "cohort", "members") {
		label := str(mem, "subject", "label")
		flagged := false
		for _, r := range list(mem, "inclusion_reasons") {
			if s, _ := r.(string); strings.Contains(s, "archived") {
				flagged = true
			}
		}
		if flagged != archived[label] {
			t.Fatalf("project %q archived flag = %v, want %v: %v", label, flagged, archived[label], mem)
		}
	}
}
