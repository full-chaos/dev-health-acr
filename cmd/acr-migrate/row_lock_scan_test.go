package main

import (
	"reflect"
	"sort"
	"testing"
)

// lockedTables names the tables whose rows a statement locks. FOR UPDATE
// without OF locks every table of the statement; FOR UPDATE OF a, b locks only
// the tables those aliases (or names) refer to: a table read in a subquery of
// the same statement is not locked and needs no UPDATE.
func TestLockedTablesFollowTheOfClause(t *testing.T) {
	for name, tc := range map[string]struct {
		statement string
		want      []string
	}{
		"no OF locks every table": {
			"SELECT 1 FROM acr.client_credentials cc JOIN acr.device_authorizations d ON d.x = cc.y FOR UPDATE SKIP LOCKED",
			[]string{"client_credentials", "device_authorizations"},
		},
		"OF an alias locks that table only": {
			"SELECT 1 FROM acr.client_credentials cc WHERE NOT EXISTS (SELECT 1 FROM acr.credential_lifecycle_outbox o WHERE o.aggregate_id = cc.credential_id) FOR UPDATE OF cc SKIP LOCKED",
			[]string{"client_credentials"},
		},
		"OF the subquery alias locks the outbox": {
			"SELECT 1 FROM acr.client_credentials cc WHERE EXISTS (SELECT 1 FROM acr.credential_lifecycle_outbox o WHERE o.aggregate_id = cc.credential_id FOR UPDATE OF o)",
			[]string{"credential_lifecycle_outbox"},
		},
		"OF a table name without an alias": {
			"SELECT 1 FROM acr.device_authorizations FOR NO KEY UPDATE OF device_authorizations",
			[]string{"device_authorizations"},
		},
		"AS aliases": {
			"SELECT 1 FROM acr.client_credentials AS cc JOIN acr.device_authorizations AS d ON true FOR UPDATE OF d",
			[]string{"device_authorizations"},
		},
		"OF two aliases": {
			"SELECT 1 FROM acr.client_credentials cc JOIN acr.device_authorizations d ON true FOR UPDATE OF cc, d",
			[]string{"client_credentials", "device_authorizations"},
		},
		"a statement without a row lock": {
			"SELECT 1 FROM acr.client_credentials cc",
			nil,
		},
	} {
		got := lockedTables(tc.statement)
		sort.Strings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: lockedTables = %v, want %v", name, got, tc.want)
		}
	}
}
