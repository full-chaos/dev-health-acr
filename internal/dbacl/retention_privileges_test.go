package dbacl

import (
	"strings"
	"testing"
)

// CHAOS-12244: the retention job runs as the runtime role, so the runtime
// declaration holds every privilege the job's statements need.
func TestRuntimeGrantsHoldTheRetentionPrivileges(t *testing.T) {
	if len(RetentionPrivileges) == 0 {
		t.Fatal("RetentionPrivileges is empty: nothing would be checked")
	}
	for _, missing := range missingPrivileges(RuntimeGrants, RetentionPrivileges) {
		t.Errorf("RuntimeGrants lacks %s, which the retention job needs", missing)
	}
}

// The check itself: a missing table privilege and a missing column are both
// reported, and a table SELECT covers every column.
func TestMissingPrivilegesReportsATableAndAColumn(t *testing.T) {
	needs := []Grant{{Table: "t", Privileges: []string{"DELETE"}, SelectColumns: []string{"a", "b"}}}
	cases := map[string]struct {
		held []Grant
		want string
	}{
		"all held":              {held: []Grant{{Table: "t", Privileges: []string{"DELETE"}, SelectColumns: []string{"a", "b"}}}},
		"a table SELECT":        {held: []Grant{{Table: "t", Privileges: []string{"SELECT", "DELETE"}}}},
		"a column missing":      {held: []Grant{{Table: "t", Privileges: []string{"DELETE"}, SelectColumns: []string{"a"}}}, want: "SELECT on t.b"},
		"a privilege missing":   {held: []Grant{{Table: "t", SelectColumns: []string{"a", "b"}}}, want: "DELETE on t"},
		"another table's grant": {held: []Grant{{Table: "u", Privileges: []string{"SELECT", "DELETE"}}}, want: "DELETE on t,SELECT on t.a,SELECT on t.b"},
	}
	for name, c := range cases {
		if got := strings.Join(missingPrivileges(c.held, needs), ","); got != c.want {
			t.Errorf("%s: missing %q, want %q", name, got, c.want)
		}
	}
}

func missingPrivileges(held, needs []Grant) []string {
	privileges := map[string]map[string]bool{}
	columns := map[string]map[string]bool{}
	for _, grant := range held {
		if privileges[grant.Table] == nil {
			privileges[grant.Table], columns[grant.Table] = map[string]bool{}, map[string]bool{}
		}
		for _, privilege := range grant.Privileges {
			privileges[grant.Table][privilege] = true
		}
		for _, column := range grant.SelectColumns {
			columns[grant.Table][column] = true
		}
	}
	var missing []string
	for _, need := range needs {
		for _, privilege := range need.Privileges {
			if !privileges[need.Table][privilege] {
				missing = append(missing, privilege+" on "+need.Table)
			}
		}
		for _, column := range need.SelectColumns {
			if !privileges[need.Table]["SELECT"] && !columns[need.Table][column] {
				missing = append(missing, "SELECT on "+need.Table+"."+column)
			}
		}
	}
	return missing
}

// CHAOS-3859, CHAOS-12244: the clarification selections are capture-only for
// the runtime (migration 0016). The runtime reads none of their content: no
// table SELECT, and a column SELECT on exactly the two columns the retention
// DELETE's WHERE reads. No grant mixes a table SELECT with a column list.
func TestClarificationSelectionsStayUnreadableByTheRuntime(t *testing.T) {
	found := false
	for _, grant := range RuntimeGrants {
		for _, privilege := range grant.Privileges {
			if privilege == "SELECT" && len(grant.SelectColumns) > 0 {
				t.Errorf("%s: a table SELECT beside a column list grants every column", grant.Table)
			}
		}
		if grant.Table != "context_fabric_clarification_selections" {
			continue
		}
		found = true
		for _, privilege := range grant.Privileges {
			if privilege == "SELECT" || privilege == "UPDATE" {
				t.Errorf("the runtime role must not hold %s on the clarification selections", privilege)
			}
		}
		if got, want := strings.Join(grant.SelectColumns, ","), "org_id,prior_result_id"; got != want {
			t.Errorf("clarification selections: SELECT columns %q, want %q (the retention DELETE's WHERE)", got, want)
		}
	}
	if !found {
		t.Fatal("RuntimeGrants has no clarification selections row")
	}
}
