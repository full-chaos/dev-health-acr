package orgpurge

import (
	"reflect"
	"testing"
)

func TestPrivilegesAreDerivedFromTheStatementsRunIssues(t *testing.T) {
	got := Privileges()
	for table, want := range map[string][]string{
		"client_credentials":               {"DELETE", "SELECT"},
		"audit_events":                     {"INSERT", "SELECT"},
		"context_fabric_org_purge_markers": {"INSERT", "SELECT", "UPDATE"},
		"agent_episodes":                   {"DELETE", "SELECT"},
		"oauth_device_grants":              {"SELECT"}, // removed by cascade, counted by the dry run
	} {
		if !reflect.DeepEqual(got[table], want) {
			t.Errorf("%s: privileges = %v, want %v", table, got[table], want)
		}
	}
	if _, ok := got["context_fabric_org_purge_markers"]; !ok {
		t.Fatal("the marker upsert is not in the derived set")
	}
	for _, table := range PurgedTables() {
		var delete bool
		for _, privilege := range got[table] {
			delete = delete || privilege == "DELETE"
		}
		if !delete {
			t.Errorf("%s is purged but the derived set has no DELETE on it", table)
		}
	}
	for _, table := range CascadedTables() {
		if got[table] == nil {
			t.Errorf("%s is read by the cascade counts but is not in the derived set", table)
		}
	}
}
