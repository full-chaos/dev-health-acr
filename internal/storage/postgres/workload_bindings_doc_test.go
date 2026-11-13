package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The operator procedure for acr.workload_bindings lives in docs/operations.md
// ("Workload bindings: add, move and disable"). The table has no write path in
// code, so the doc is the only description of its columns. These tests keep the
// doc's column table and role list equal to the migration SQL, with no database.

const bindingsDocHeading = "### Workload bindings: add, move and disable"

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// bindingsMigrationColumns returns the column names of CREATE TABLE
// acr.workload_bindings in migration 0030; bindingsAddedColumns adds the
// columns later migrations add.
func bindingsMigrationColumns(t *testing.T, sql string) []string {
	t.Helper()
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS acr.workload_bindings (")
	if start < 0 {
		t.Fatal("migration 0030: CREATE TABLE acr.workload_bindings not found")
	}
	body := sql[start:]
	end := strings.Index(body, "\n);")
	if end < 0 {
		t.Fatal("migration 0030: end of the table not found")
	}
	var cols []string
	for _, line := range strings.Split(body[:end], "\n")[1:] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		first := strings.Fields(line)[0]
		switch strings.ToUpper(first) {
		case "CHECK", "UNIQUE", "PRIMARY", "FOREIGN", "CONSTRAINT":
			continue
		}
		cols = append(cols, first)
	}
	sort.Strings(cols)
	return cols
}

func bindingsDocSection(t *testing.T, doc string) string {
	t.Helper()
	start := strings.Index(doc, bindingsDocHeading)
	if start < 0 {
		t.Fatalf("docs/operations.md: %q not found", bindingsDocHeading)
	}
	rest := doc[start+len(bindingsDocHeading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

var docColumnRow = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|")

func TestWorkloadBindingsDocColumnsMatchMigration(t *testing.T) {
	want := append(bindingsMigrationColumns(t, readRepoFile(t, "migrations/postgres/0030_workload_token_exchange.sql")), bindingsAddedColumns(t)...)
	sort.Strings(want)
	section := bindingsDocSection(t, readRepoFile(t, "docs/operations.md"))
	var got []string
	for _, m := range docColumnRow.FindAllStringSubmatch(section, -1) {
		got = append(got, m[1])
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("docs/operations.md workload bindings columns = %v, migration 0030 columns = %v", got, want)
	}
}

func TestWorkloadBindingsDocRolesMatchMigration(t *testing.T) {
	// The newest migration that sets the constraint holds the live role set.
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "postgres", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob migrations: %v (%d files)", err, len(files))
	}
	sort.Strings(files)
	check := regexp.MustCompile(`ck_acr_workload_bindings_role CHECK \(role IN \(([^)]*)\)\)`)
	var m []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if found := check.FindStringSubmatch(string(raw)); found != nil {
			m = found
		}
	}
	if m == nil {
		t.Fatal("no migration sets ck_acr_workload_bindings_role")
	}
	var roles []string
	for _, r := range strings.Split(m[1], ",") {
		roles = append(roles, strings.Trim(strings.TrimSpace(r), "'"))
	}
	section := bindingsDocSection(t, readRepoFile(t, "docs/operations.md"))
	row := regexp.MustCompile("(?m)^\\| `role` \\| (.*) \\|$").FindStringSubmatch(section)
	if row == nil {
		t.Fatal("docs/operations.md: no role row")
	}
	// The role list is the first sentence of the cell; later sentences name code.
	list := row[1]
	if i := strings.Index(list, ". "); i >= 0 {
		list = list[:i]
	}
	for _, r := range roles {
		if !strings.Contains(list, "`"+r+"`") {
			t.Fatalf("docs/operations.md role row %q does not name the role %q the migration allows", list, r)
		}
	}
	if n := strings.Count(list, "`") / 2; n != len(roles) {
		t.Fatalf("docs/operations.md role list %q names %d roles, the migration allows %d (%v)", list, n, len(roles), roles)
	}
}

func TestWorkloadBindingsDocNamesTheUniqueKey(t *testing.T) {
	sql := readRepoFile(t, "migrations/postgres/0070_workload_bindings_org_key.sql")
	if !strings.Contains(sql, "uq_acr_workload_bindings_identity_org") {
		t.Fatal("migration 0070 no longer names uq_acr_workload_bindings_identity_org")
	}
	section := bindingsDocSection(t, readRepoFile(t, "docs/operations.md"))
	if !strings.Contains(section, "uq_acr_workload_bindings_identity_org") {
		t.Fatal("docs/operations.md does not name the unique key")
	}
}

// bindingsAddedColumns returns the columns every later migration adds to
// acr.workload_bindings with ALTER TABLE ... ADD COLUMN.
func bindingsAddedColumns(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "migrations", "postgres")
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob migrations: %v (%d files)", err, len(files))
	}
	alter := regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?acr\.workload_bindings\s+([^;]*)`)
	add := regexp.MustCompile(`(?i)ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_]+)`)
	var cols []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range alter.FindAllStringSubmatch(string(raw), -1) {
			for _, m := range add.FindAllStringSubmatch(stmt[1], -1) {
				cols = append(cols, m[1])
			}
		}
	}
	return cols
}

func TestWorkloadBindingsAddedColumnsAreSeen(t *testing.T) {
	if got := strings.Join(bindingsAddedColumns(t), ","); got != "org_scope" {
		t.Fatalf("columns later migrations add to acr.workload_bindings = %q, want \"org_scope\" (migration 0072); a new column needs its doc row", got)
	}
}

func TestWorkloadBindingsDocNamesTheLiveOrgsConstraints(t *testing.T) {
	sql := readRepoFile(t, "migrations/postgres/0072_workload_binding_live_orgs_scope.sql")
	section := bindingsDocSection(t, readRepoFile(t, "docs/operations.md"))
	for _, name := range []string{"ck_acr_workload_bindings_org_scope", "ck_acr_workload_bindings_live_orgs_revoke", "uq_acr_workload_bindings_live_orgs_identity"} {
		if !strings.Contains(sql, name) {
			t.Fatalf("migration 0072 no longer names %s", name)
		}
		if !strings.Contains(section, name) {
			t.Fatalf("docs/operations.md does not name %s", name)
		}
	}
}
