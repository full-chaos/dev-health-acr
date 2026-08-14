package main

import (
	"testing"
)

// TestApplyMigrationPython_TemplatedAlterFromTableList mirrors
// 027_add_org_id_to_sorting_keys.py's real shape: a table-name list plus a loop that issues a
// templated ALTER TABLE `{table}` ADD COLUMN referencing that list's loop variable.
func TestApplyMigrationPython_TemplatedAlterFromTableList(t *testing.T) {
	schema := newCHSchema()
	schema.createTable("git_commits")
	schema.createTable("ci_pipeline_runs")
	// deployments is deliberately absent: the real migration also skips tables that do not
	// exist (_table_exists guards every ALTER), so the replay must not fabricate a table.

	source := `
TABLES_NEEDING_ORG_ID_COLUMN = [
    "git_commits",
    "ci_pipeline_runs",
    "deployments",
]

def upgrade(client):
    for table in TABLES_NEEDING_ORG_ID_COLUMN:
        if not _table_exists(client, table):
            continue
        client.command(
            f"ALTER TABLE ` + "`{table}`" + ` ADD COLUMN IF NOT EXISTS "
            f"org_id String DEFAULT 'default'"
        )
`
	unhandled := applyMigrationPython(schema, source, "027_add_org_id_to_sorting_keys.py")
	if len(unhandled) != 0 {
		t.Fatalf("expected no unhandled notes, got %v", unhandled)
	}
	if !schema.columnExists("git_commits", "org_id") {
		t.Fatal("git_commits should have gained org_id")
	}
	if !schema.columnExists("ci_pipeline_runs", "org_id") {
		t.Fatal("ci_pipeline_runs should have gained org_id")
	}
	if schema.tableExists("deployments") {
		t.Fatal("deployments was never created in this schema and must not be fabricated")
	}
}

// TestApplyMigrationPython_LiteralAlter mirrors 028_add_release_ref_to_deployments.py's real
// shape: non-parameterized, hardcoded ALTER TABLE statements inside triple-quoted strings.
func TestApplyMigrationPython_LiteralAlter(t *testing.T) {
	schema := newCHSchema()
	schema.createTable("deployments")

	source := `
def upgrade(client):
    client.command(
        """
        ALTER TABLE deployments
        ADD COLUMN IF NOT EXISTS release_ref String DEFAULT ''
        """
    )
    client.command(
        """
        ALTER TABLE deployments
        ADD COLUMN IF NOT EXISTS release_ref_confidence Float64 DEFAULT 0.0
        """
    )
`
	unhandled := applyMigrationPython(schema, source, "028_add_release_ref_to_deployments.py")
	if len(unhandled) != 0 {
		t.Fatalf("expected no unhandled notes, got %v", unhandled)
	}
	if !schema.columnExists("deployments", "release_ref") {
		t.Fatal("deployments should have gained release_ref")
	}
	if !schema.columnExists("deployments", "release_ref_confidence") {
		t.Fatal("deployments should have gained release_ref_confidence")
	}
}

// TestApplyMigrationPython_LiteralDropColumn exercises the DROP half of the literal form.
func TestApplyMigrationPython_LiteralDropColumn(t *testing.T) {
	schema := newCHSchema()
	cols := schema.createTable("widgets")
	cols["legacy_field"] = true

	source := `
def upgrade(client):
    client.command("""ALTER TABLE widgets DROP COLUMN IF EXISTS legacy_field""")
`
	unhandled := applyMigrationPython(schema, source, "099_drop_legacy_field.py")
	if len(unhandled) != 0 {
		t.Fatalf("expected no unhandled notes, got %v", unhandled)
	}
	if schema.columnExists("widgets", "legacy_field") {
		t.Fatal("legacy_field should have been dropped")
	}
}

// TestApplyMigrationPython_UnattributableTemplatedAlterIsReported is the case the team lead
// asked for explicitly: a templated ALTER TABLE the parser cannot attribute to a table list
// must be reported as unhandled, never silently ignored.
func TestApplyMigrationPython_UnattributableTemplatedAlterIsReported(t *testing.T) {
	schema := newCHSchema()
	// No preceding "for x in SOME_LIST:" loop and no SOME_LIST assignment at all -- the
	// parser has nothing to attribute this templated ALTER to.
	source := `
def upgrade(client):
    client.command(
        f"ALTER TABLE ` + "`{table}`" + ` ADD COLUMN IF NOT EXISTS mystery_column String"
    )
`
	unhandled := applyMigrationPython(schema, source, "099_mystery.py")
	if len(unhandled) == 0 {
		t.Fatal("expected an unhandled note for a templated ALTER with no attributable table list")
	}
	if unhandled[0].File != "099_mystery.py" {
		t.Fatalf("unhandled note should name the file, got %+v", unhandled[0])
	}
	if unhandled[0].Table != "" {
		t.Fatalf("an unattributable templated ALTER has no known table, got %q", unhandled[0].Table)
	}
}

// TestApplyMigrationPython_LoopOverUndefinedListIsReported covers a variant of the
// cannot-attribute path: a `for table in SOME_LIST:` loop precedes the templated ALTER, but
// SOME_LIST itself was never assigned anywhere in the file (e.g. imported from another
// module) -- nearestLoopList finds a name, but lists[name] has no entry.
func TestApplyMigrationPython_LoopOverUndefinedListIsReported(t *testing.T) {
	schema := newCHSchema()
	schema.createTable("git_commits")
	source := `
from shared_migration_lists import TABLES_FROM_ELSEWHERE

def upgrade(client):
    for table in TABLES_FROM_ELSEWHERE:
        client.command(
            f"ALTER TABLE ` + "`{table}`" + ` ADD COLUMN IF NOT EXISTS org_id String DEFAULT 'default'"
        )
`
	unhandled := applyMigrationPython(schema, source, "099_imported_list.py")
	if len(unhandled) == 0 {
		t.Fatal("expected an unhandled note: the loop's list name is never assigned in this file")
	}
	if schema.columnExists("git_commits", "org_id") {
		t.Fatal("must not guess at the effect of an unattributable templated ALTER")
	}
}

// TestApplyMigrationPython_MalformedContinuationIsReported covers the other half of the
// cannot-attribute path: the table list resolves fine, but the column name that should follow
// on the wrapped f-string continuation cannot be parsed (e.g. it is not the next token at
// all), so columnAfterTemplatedAlter returns "".
func TestApplyMigrationPython_MalformedContinuationIsReported(t *testing.T) {
	schema := newCHSchema()
	schema.createTable("git_commits")
	source := `
TABLES_NEEDING_ORG_ID_COLUMN = [
    "git_commits",
]

def upgrade(client):
    for table in TABLES_NEEDING_ORG_ID_COLUMN:
        client.command(
            f"ALTER TABLE ` + "`{table}`" + ` ADD COLUMN IF NOT EXISTS "
            + build_column_clause_from_config()
        )
`
	unhandled := applyMigrationPython(schema, source, "099_dynamic_column.py")
	if len(unhandled) == 0 {
		t.Fatal("expected an unhandled note: the column name is not a parseable literal token")
	}
	if schema.columnExists("git_commits", "build_column_clause_from_config") {
		t.Fatal("must not misparse a function call as a column name")
	}
}

// TestApplyMigrationPython_MultipleListsAttributeIndependently guards the "nearest preceding
// loop" attribution logic against leaking across two different table lists / loops in the
// same file -- a plausible shape (027 itself has multiple loops over different lists for its
// rebuild steps, even though only one issues a templated ALTER).
func TestApplyMigrationPython_MultipleListsAttributeIndependently(t *testing.T) {
	schema := newCHSchema()
	schema.createTable("git_commits")
	schema.createTable("deployments")

	source := `
TABLES_NEEDING_ORG_ID = [
    "git_commits",
]

TABLES_NEEDING_RELEASE_REF = [
    "deployments",
]

def upgrade(client):
    for table in TABLES_NEEDING_ORG_ID:
        client.command(
            f"ALTER TABLE ` + "`{table}`" + ` ADD COLUMN IF NOT EXISTS org_id String DEFAULT 'default'"
        )
    for table in TABLES_NEEDING_RELEASE_REF:
        client.command(
            f"ALTER TABLE ` + "`{table}`" + ` ADD COLUMN IF NOT EXISTS release_ref String DEFAULT ''"
        )
`
	unhandled := applyMigrationPython(schema, source, "099_two_lists.py")
	if len(unhandled) != 0 {
		t.Fatalf("expected no unhandled notes, got %v", unhandled)
	}
	if !schema.columnExists("git_commits", "org_id") {
		t.Fatal("git_commits should have gained org_id from the first loop")
	}
	if schema.columnExists("git_commits", "release_ref") {
		t.Fatal("git_commits must not gain release_ref: it was never in TABLES_NEEDING_RELEASE_REF")
	}
	if !schema.columnExists("deployments", "release_ref") {
		t.Fatal("deployments should have gained release_ref from the second loop")
	}
	if schema.columnExists("deployments", "org_id") {
		t.Fatal("deployments must not gain org_id: it was never in TABLES_NEEDING_ORG_ID")
	}
}

// TestApplyMigrationPython_NoDDLIsSilentlyFine covers a migration with no schema-shape effect
// at all (e.g. a pure data backfill) -- it must not be reported as unhandled just because it
// exists.
func TestApplyMigrationPython_NoDDLIsSilentlyFine(t *testing.T) {
	schema := newCHSchema()
	source := `
def upgrade(client):
    client.command("INSERT INTO some_log_table (event) VALUES ('backfill ran')")
`
	unhandled := applyMigrationPython(schema, source, "048_seed_legacy_membership_run.py")
	if len(unhandled) != 0 {
		t.Fatalf("expected no unhandled notes for a migration with no ALTER/CREATE/DROP TABLE DDL, got %v", unhandled)
	}
}

// TestReplayMigrationsDir_PythonAndSQLInterleaved is the end-to-end shape: a .sql CREATE
// TABLE, a .py migration adding org_id via the table-list convention (027's shape), and a
// later .sql ALTER -- all replayed together in lexical filename order, matching how
// 027_add_org_id_to_sorting_keys.py actually sits between .sql migrations in the real ops
// directory.
// devhealthschema:not-a-production-replica this DDL is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestReplayMigrationsDir_PythonAndSQLInterleaved(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "000_create.sql", `
CREATE TABLE IF NOT EXISTS git_commits (
    repo_id UUID,
    hash String,
    committer_when DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree ORDER BY (repo_id, hash);
`)
	writeMigration(t, dir, "027_add_org_id.py", `
TABLES_NEEDING_ORG_ID_COLUMN = [
    "git_commits",
]

def upgrade(client):
    for table in TABLES_NEEDING_ORG_ID_COLUMN:
        client.command(
            f"ALTER TABLE `+"`{table}`"+` ADD COLUMN IF NOT EXISTS org_id String DEFAULT 'default'"
        )
`)
	writeMigration(t, dir, "029_more_columns.sql", `
ALTER TABLE git_commits ADD COLUMN IF NOT EXISTS branch Nullable(String);
`)

	schema, unhandled, err := replayMigrationsDir(dir)
	if err != nil {
		t.Fatalf("replayMigrationsDir: %v", err)
	}
	if len(unhandled) != 0 {
		t.Fatalf("expected no unhandled notes, got %v", unhandled)
	}
	for _, col := range []string{"repo_id", "hash", "committer_when", "org_id", "branch"} {
		if !schema.columnExists("git_commits", col) {
			t.Fatalf("expected git_commits.%s to exist after the full replay", col)
		}
	}
}

// TestApplyMigrationPython_LiteralRenameColumn is the Python half of Codex finding 12's
// RENAME COLUMN ask.
func TestApplyMigrationPython_LiteralRenameColumn(t *testing.T) {
	schema := newCHSchema()
	cols := schema.createTable("git_commits")
	cols["author_when"] = true

	source := `
def upgrade(client):
    client.command("""ALTER TABLE git_commits RENAME COLUMN IF EXISTS author_when TO committer_when""")
`
	unhandled := applyMigrationPython(schema, source, "099_rename.py")
	if len(unhandled) != 0 {
		t.Fatalf("expected no unhandled notes, got %v", unhandled)
	}
	if schema.columnExists("git_commits", "author_when") {
		t.Fatal("author_when should have been renamed away")
	}
	if !schema.columnExists("git_commits", "committer_when") {
		t.Fatal("committer_when should exist after the rename")
	}
}

// TestApplyMigrationPython_TemplatedRenameColumn covers the templated (list-attributed) form.
func TestApplyMigrationPython_TemplatedRenameColumn(t *testing.T) {
	schema := newCHSchema()
	cols := schema.createTable("git_commits")
	cols["author_when"] = true

	source := `
TABLES_TO_RENAME = [
    "git_commits",
]

def upgrade(client):
    for table in TABLES_TO_RENAME:
        client.command(
            f"ALTER TABLE ` + "`{table}`" + ` RENAME COLUMN IF EXISTS author_when TO committer_when"
        )
`
	unhandled := applyMigrationPython(schema, source, "099_templated_rename.py")
	if len(unhandled) != 0 {
		t.Fatalf("expected no unhandled notes, got %v", unhandled)
	}
	if schema.columnExists("git_commits", "author_when") {
		t.Fatal("author_when should have been renamed away")
	}
	if !schema.columnExists("git_commits", "committer_when") {
		t.Fatal("committer_when should exist after the rename")
	}
}

// TestApplyMigrationPython_LiteralNonShadowCreateAndDropTableReported is the CREATE/DROP TABLE
// half of Codex finding 12: a literal, real (non-shadow) table name in a CREATE/DROP TABLE
// statement is DDL this file does not interpret at all and must be reported, not ignored.
// devhealthschema:not-a-production-replica this DDL is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestApplyMigrationPython_LiteralNonShadowCreateAndDropTableReported(t *testing.T) {
	schema := newCHSchema()
	source := `
def upgrade(client):
    client.command("""CREATE TABLE some_real_table (id UUID) ENGINE = MergeTree ORDER BY id""")
    client.command("""DROP TABLE some_other_real_table""")
`
	unhandled := applyMigrationPython(schema, source, "099_real_tables.py")
	var sawCreate, sawDrop bool
	for _, u := range unhandled {
		if u.Table == "some_real_table" {
			sawCreate = true
		}
		if u.Table == "some_other_real_table" {
			sawDrop = true
		}
	}
	if !sawCreate {
		t.Fatalf("expected a reported note for CREATE TABLE some_real_table, got %v", unhandled)
	}
	if !sawDrop {
		t.Fatalf("expected a reported note for DROP TABLE some_other_real_table, got %v", unhandled)
	}
}

// TestApplyMigrationPython_ShadowTableRebuildIsNotReported is the companion case: the
// well-established shadow-table rebuild pattern (SHOW CREATE TABLE + a "<table>_new" copy +
// EXCHANGE TABLES), used by several real ops migrations, must not generate noise -- it is
// column-set-neutral by construction and its DDL is only visible as runtime-templated
// f-strings this file cannot statically resolve to a literal name anyway.
// devhealthschema:not-a-production-replica this DDL is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestApplyMigrationPython_ShadowTableRebuildIsNotReported(t *testing.T) {
	schema := newCHSchema()
	schema.createTable("work_items")
	source := `
def upgrade(client):
    shadow = "work_items_new"
    client.command(f"DROP TABLE IF EXISTS ` + "`{shadow}`" + `")
    client.command(new_ddl)
    client.command(f"EXCHANGE TABLES ` + "`work_items`" + ` AND ` + "`{shadow}`" + `")
    client.command(f"DROP TABLE ` + "`{shadow}`" + `")
`
	unhandled := applyMigrationPython(schema, source, "099_shadow_rebuild.py")
	if len(unhandled) != 0 {
		t.Fatalf("expected no unhandled notes for the shadow-table rebuild pattern, got %v", unhandled)
	}
}

// TestApplyMigrationPython_DocstringProseNotMisreadAsDDL is a regression test for the false
// positives found while validating against the real ops migrations: long prose docstrings
// (module-level, as in migration 027/042, and function-level, as in migration 010)
// describing the shadow-table algorithm in narrative form -- "DROP TABLE if concurrent
// access fails", "4. CREATE TABLE table_new ..." -- must not be misread as real DDL.
// devhealthschema:not-a-production-replica this DDL is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestApplyMigrationPython_DocstringProseNotMisreadAsDDL(t *testing.T) {
	schema := newCHSchema()
	schema.createTable("repos")

	moduleDocstring := `"""Migration 099: rebuild repos.

This uses the shadow-table pattern:
    1. SHOW CREATE TABLE to get full DDL
    2. CREATE TABLE repos_new with the new ORDER BY
    3. DROP TABLE IF EXISTS repos_new on any retry
    4. EXCHANGE TABLES repos AND repos_new
"""

def upgrade(client):
    client.command(f"DROP TABLE IF EXISTS ` + "`{shadow}`" + `")
`
	if unhandled := applyMigrationPython(schema, moduleDocstring, "099_module_docstring.py"); len(unhandled) != 0 {
		t.Fatalf("module docstring prose was misread as DDL: %v", unhandled)
	}

	// devhealthschema:not-a-production-replica this is PROSE inside a Python docstring fixture, the
	// exact false positive this test pins -- it renders no table and reads no
	// schema, and it sits past the window of the marker above.
	functionDocstring := `
import logging

def upgrade(client):
    """
    Renames a column for all tables.
    Uses dynamic SHOW CREATE TABLE to preserve schema, then performs
    atomic EXCHANGE TABLES migration. DROP TABLE IF EXISTS the shadow
    copy if a previous attempt was interrupted.
    """
    client.command(f"DROP TABLE IF EXISTS ` + "`{shadow}`" + `")
`
	if unhandled := applyMigrationPython(schema, functionDocstring, "099_function_docstring.py"); len(unhandled) != 0 {
		t.Fatalf("function docstring prose was misread as DDL: %v", unhandled)
	}
}
