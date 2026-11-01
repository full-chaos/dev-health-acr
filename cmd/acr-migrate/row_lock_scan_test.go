package main

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
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

var (
	rowLockClause = regexp.MustCompile(`(?is)\bFOR\s+(?:NO\s+KEY\s+UPDATE|UPDATE|KEY\s+SHARE|SHARE)\b(?:\s+OF\s+([a-z0-9_,\s]+?))?(?:\s+(?:NOWAIT|SKIP\s+LOCKED)\b|\s*$|\s*\))`)
	tableAlias    = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+acr\.([a-z0-9_]+)(?:\s+(?:AS\s+)?([a-z_][a-z0-9_]*))?`)
	notAnAlias    = map[string]bool{"where": true, "join": true, "on": true, "inner": true, "left": true, "right": true, "cross": true, "full": true, "using": true, "group": true, "order": true, "limit": true, "for": true, "union": true, "returning": true, "set": true, "natural": true, "lateral": true}
)

// lockedTables returns the tables whose rows the statement locks: with an OF
// clause only the tables the named aliases (or names) refer to, without one
// every table the statement reads. A statement with no row lock locks none.
func lockedTables(statement string) []string {
	lock := rowLockClause.FindStringSubmatch(statement)
	if lock == nil {
		return nil
	}
	named := map[string]bool{}
	for _, name := range strings.FieldsFunc(strings.ToLower(lock[1]), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		named[name] = true
	}
	var tables []string
	for _, match := range tableAlias.FindAllStringSubmatch(statement, -1) {
		table, alias := strings.ToLower(match[1]), strings.ToLower(match[2])
		if notAnAlias[alias] {
			alias = ""
		}
		if len(named) == 0 || named[table] && alias == "" || alias != "" && named[alias] {
			tables = append(tables, table)
		}
	}
	return tables
}

// A table the OF clause names only by its alias still needs UPDATE declared:
// the scanner resolves the alias to the table before it checks the grants.
func TestAnAliasNamedInTheOfClauseResolvesToItsTableAndNeedsUpdate(t *testing.T) {
	updatable := map[string]bool{"client_credentials": true}
	for name, tc := range map[string]struct {
		statement string
		want      []string
	}{
		"a locked table without UPDATE named by its alias": {
			"SELECT 1 FROM acr.schema_migrations m WHERE true FOR UPDATE OF m",
			[]string{"schema_migrations"},
		},
		"the same through AS": {
			"SELECT 1 FROM acr.schema_migrations AS m FOR SHARE OF m",
			[]string{"schema_migrations"},
		},
		"a locked table with UPDATE": {
			"SELECT 1 FROM acr.client_credentials cc FOR UPDATE OF cc SKIP LOCKED",
			nil,
		},
		"an unlocked table read beside a locked one": {
			"SELECT 1 FROM acr.client_credentials cc WHERE EXISTS (SELECT 1 FROM acr.schema_migrations m) FOR UPDATE OF cc",
			nil,
		},
	} {
		got := tablesMissingUpdate(tc.statement, updatable)
		sort.Strings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: tablesMissingUpdate = %v, want %v", name, got, tc.want)
		}
	}
}

// tablesMissingUpdate names the tables a statement row-locks that the runtime
// grant declaration does not give UPDATE.
func tablesMissingUpdate(statement string, updatable map[string]bool) []string {
	var missing []string
	for _, table := range lockedTables(statement) {
		if !updatable[table] {
			missing = append(missing, table)
		}
	}
	return missing
}
