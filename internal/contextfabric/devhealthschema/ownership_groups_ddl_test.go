package devhealthschema_test

import (
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

// migration147Statements reads ops migration 147 as merged (testdata, sha256
// 005adbb09f95…) and returns its statements without comment lines.
func migration147Statements(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/migration_147_project_team_ownership_groups.sql")
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "--") {
			kept = append(kept, line)
		}
	}
	var statements []string
	for _, statement := range strings.Split(strings.Join(kept, "\n"), ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

// The two tables of migration 147 equal their declaration, column by column
// and engine by engine (EngineFull adds the index_granularity the server
// reports), and the two views equal the copied constants byte for byte.
func TestOwnershipGroupsDDLMirrorsMigration147(t *testing.T) {
	statements := migration147Statements(t)
	if len(statements) != 4 {
		t.Fatalf("migration 147 holds %d statements, want 4 (two tables, two views)", len(statements))
	}
	for i, table := range devhealthschema.OwnershipGroupsTables {
		statement := statements[i]
		prefix := "CREATE TABLE IF NOT EXISTS " + table + "\n("
		if !strings.HasPrefix(statement, prefix) {
			t.Fatalf("statement %d is not the CREATE TABLE of %s: %.80q", i, table, statement)
		}
		columnsText, engineText, ok := strings.Cut(statement[len(prefix):], "\n)\nENGINE = ")
		if !ok {
			t.Fatalf("%s: no ENGINE clause after the column list", table)
		}
		declared := devhealthschema.ProductionColumns[table]
		var migrated []devhealthschema.Column
		for _, line := range strings.Split(strings.TrimSpace(columnsText), "\n") {
			name, typ, _ := strings.Cut(strings.TrimSuffix(strings.TrimSpace(line), ","), " ")
			migrated = append(migrated, devhealthschema.Column{Name: name, Type: typ})
		}
		if len(declared) != len(migrated) {
			t.Fatalf("%s: declaration has %d columns, migration 147 has %d", table, len(declared), len(migrated))
		}
		for j := range migrated {
			if declared[j].Name != migrated[j].Name || declared[j].Type != migrated[j].Type {
				t.Errorf("%s column %d: declaration %s %q, migration %s %q", table, j, declared[j].Name, declared[j].Type, migrated[j].Name, migrated[j].Type)
			}
		}
		engine := strings.Join(strings.Fields(engineText), " ") + " SETTINGS index_granularity = 8192"
		if got := devhealthschema.EngineFull[table]; got != engine {
			t.Errorf("%s: EngineFull %q, migration 147 %q", table, got, engine)
		}
	}
	if devhealthschema.OwnershipGroupsCopyViewDDL != statements[2] {
		t.Errorf("OwnershipGroupsCopyViewDDL differs from migration 147's copy view:\n got: %s\nwant: %s", devhealthschema.OwnershipGroupsCopyViewDDL, statements[2])
	}
	if devhealthschema.OwnershipGroupsRefreshViewDDL != statements[3] {
		t.Errorf("OwnershipGroupsRefreshViewDDL differs from migration 147's refresh view")
	}
	views := devhealthschema.OwnershipGroupsViewsDDL()
	if len(views) != 2 || views[0] != statements[2] || views[1] != statements[3] {
		t.Errorf("OwnershipGroupsViewsDDL is not the copy view then the refresh view")
	}
	for _, needle := range []string{"REFRESH EVERY 60 SECOND APPEND TO project_team_ownership_groups_log", "FROM project_team_ownership_groups_current FINAL"} {
		if !strings.Contains(statements[3], needle) {
			t.Errorf("migration 147's refresh view lost %q: the testdata is not the reviewed migration", needle)
		}
	}
}
