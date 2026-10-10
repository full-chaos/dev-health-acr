//go:build fixturegraph

package fixturegraph

import (
	"fmt"
	"strings"
	"testing"
)

// supersededTwin returns the inactive copy of the archived-project owner: the same team
// name under another id, is_active = 0.
func supersededTwin(t *testing.T) (activeID, inactiveID, name string) {
	t.Helper()
	_, name, _, _ = archivedOwner(t)
	org := sqlStr(orgID(t))
	rows := ch(t, fmt.Sprintf(`SELECT id, toString(is_active) FROM teams FINAL WHERE org_id = %s AND name = %s ORDER BY id`, org, sqlStr(name)))
	for _, r := range rows {
		if r[1] == "0" {
			inactiveID = r[0]
		} else {
			activeID = r[0]
		}
	}
	if activeID == "" || inactiveID == "" {
		t.Fatalf("team %q has no active+inactive pair in the seeded rows: %v", name, rows)
	}
	return activeID, inactiveID, name
}

// Use-case: a team is one subject. A superseded (inactive) row of the same name is never a
// second subject, never offered by find_subjects.
func TestFindSubjectsOffersTheActiveTeamOnly(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("find_subjects")
	_, inactiveID, name := supersededTwin(t)
	d, raw := c.call("find_subjects", doc{"query": name, "kinds": []string{"team"}})
	var same int
	for _, s := range list(d, "subjects") {
		if str(s, "label") == name {
			same++
		}
		if strings.Contains(str(s, "canonical_id"), inactiveID) {
			t.Fatalf("find_subjects offered the inactive team %s: %.1500s", inactiveID, raw)
		}
	}
	if same != 1 {
		t.Fatalf("find_subjects %q returned %d teams of that name, want 1: %.1500s", name, same, raw)
	}
}

// Use-case: "which projects does team T own?" serves the cohort although a superseded row
// carries the same name.
func TestTeamProjectsServeWithASupersededTwin(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("investigate_with_interpretation")
	_, _, name := supersededTwin(t)
	_, _, owned, _ := archivedOwner(t)
	d, raw := c.call("investigate_with_interpretation", doc{
		"question":       fmt.Sprintf("Which projects does team %s own?", name),
		"interpretation": teamProjectsInterpretation(name),
		"contract":       c.interpretContract(),
		"synthesis":      "client",
		"budget":         doc{"max_cohort_members": 250, "max_evidence_refs": 500, "max_serialized_bytes": 1048576},
	})
	got := memberLabels(d)
	if diffSets(owned, got) != diffSets(owned, owned) {
		t.Fatalf("team %s projects differ from the open ownership rows: %s\n%.2500s", name, diffSets(owned, got), raw)
	}
}
