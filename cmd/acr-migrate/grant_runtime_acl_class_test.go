package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/dbacl"
	"github.com/stretchr/testify/require"
)

var (
	createTablePattern       = regexp.MustCompile(`(?i)CREATE TABLE(?: IF NOT EXISTS)?\s+acr\.([a-z0-9_]+)`)
	readinessPrivilegePatten = regexp.MustCompile(`has_table_privilege\(current_user, 'acr\.([a-z0-9_]+)', '([A-Z]+)'\)`)
)

func migrationTables(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../../migrations/postgres/*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, files, "no migration files found")
	seen := map[string]bool{}
	for _, file := range files {
		_, err := strconv.Atoi(strings.SplitN(filepath.Base(file), "_", 2)[0])
		require.NoError(t, err, file)
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, match := range createTablePattern.FindAllStringSubmatch(string(body), -1) {
			seen[match[1]] = true
		}
	}
	tables := make([]string, 0, len(seen))
	for table := range seen {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

// TestRuntimeGrantDeclaration_coversEveryMigrationTable is the class test: every
// table a migration creates is declared in internal/dbacl (and so granted on
// both the compose and Kubernetes paths) or is named there as having no runtime
// access, with a reason. There is no allowlist: a new table with neither fails.
func TestRuntimeGrantDeclaration_coversEveryMigrationTable(t *testing.T) {
	declared := map[string]bool{}
	for _, table := range dbacl.Tables() {
		declared[table] = true
	}
	created := map[string]bool{}
	var ungranted, withoutReason, bothWays []string
	for _, table := range migrationTables(t) {
		created[table] = true
		reason, noAccess := dbacl.NoRuntimeAccess[table]
		switch {
		case declared[table] && noAccess:
			bothWays = append(bothWays, table)
		case !declared[table] && !noAccess:
			ungranted = append(ungranted, table)
		case noAccess && strings.TrimSpace(reason) == "":
			withoutReason = append(withoutReason, table)
		}
	}
	var stale []string
	for table := range declared {
		if table != "schema_migrations" && !created[table] {
			stale = append(stale, table)
		}
	}
	for table := range dbacl.NoRuntimeAccess {
		if !created[table] {
			stale = append(stale, table)
		}
	}
	sort.Strings(stale)
	require.Empty(t, ungranted, "tables created by a migration that internal/dbacl neither grants to the runtime role nor lists in NoRuntimeAccess")
	require.Empty(t, bothWays, "tables both declared and listed as no runtime access")
	require.Empty(t, withoutReason, "NoRuntimeAccess entries need a reason")
	require.Empty(t, stale, "internal/dbacl names tables no migration creates")
}

// TestRuntimeGrantDeclaration_includesWhatTheReadinessCheckAsserts keeps the
// readiness check's privilege assertions inside the declaration: a privilege the
// check demands that the runtime role is never granted would fail readiness.
func TestRuntimeGrantDeclaration_includesWhatTheReadinessCheckAsserts(t *testing.T) {
	source, err := os.ReadFile("../../internal/runtime/hosted/postgres_readiness.go")
	require.NoError(t, err)
	matches := readinessPrivilegePatten.FindAllStringSubmatch(string(source), -1)
	require.NotEmpty(t, matches, "no privilege assertions parsed from the readiness check")
	declared := map[string]bool{}
	for _, grant := range dbacl.RuntimeGrants {
		for _, privilege := range grant.Privileges {
			declared[grant.Table+"/"+privilege] = true
		}
	}
	var missing []string
	for _, match := range matches {
		if !declared[match[1]+"/"+match[2]] {
			missing = append(missing, match[2]+" on "+match[1])
		}
	}
	require.Empty(t, missing, "the readiness check asserts privileges the declaration does not grant")
}

// TestRuntimeGrantDeclaration_runtimeRoleHoldsExactlyTheDeclaration runs the
// Kubernetes path on a fresh database without default privileges and compares,
// for every table a migration creates and every table privilege, what the
// runtime role holds with what internal/dbacl declares, under each feature
// setting.
func TestRuntimeGrantDeclaration_runtimeRoleHoldsExactlyTheDeclaration(t *testing.T) {
	for name, features := range map[string]dbacl.Features{
		"no optional feature":    {},
		"every optional feature": {dbacl.EpisodeWriteback: true, dbacl.WorkloadTokenExchange: true},
	} {
		features := features
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_declaration_test", Features: features})
			want := map[string]map[string]bool{}
			for _, statement := range dbacl.GrantStatements(dbacl.QuoteIdent(database.Role), features) {
				if want[statement.Table] == nil {
					want[statement.Table] = map[string]bool{}
				}
				for _, privilege := range statement.Privileges {
					want[statement.Table][privilege] = true
				}
			}
			var mismatches []string
			for _, table := range migrationTables(t) {
				for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
					var has bool
					require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, $2, $3)`, database.Role, "acr."+table, privilege).Scan(&has))
					if has != want[table][privilege] {
						mismatches = append(mismatches, privilege+" on "+table+": holds "+strconv.FormatBool(has)+", declared "+strconv.FormatBool(want[table][privilege]))
					}
				}
			}
			require.Empty(t, mismatches)
			for _, grant := range dbacl.RuntimeGrants {
				if !features[grant.Feature] && grant.Feature != dbacl.AlwaysOn {
					continue
				}
				for _, sequence := range grant.Sequences {
					var has bool
					require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_sequence_privilege($1, $2, 'USAGE')`, database.Role, "acr."+sequence).Scan(&has))
					require.True(t, has, "USAGE on acr.%s", sequence)
				}
			}
		})
	}
}
