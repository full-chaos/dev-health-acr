package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMigration(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// devhealthschema:not-a-production-replica this DDL is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestReplayMigrationsDir_CreateAddDropColumnDropTable(t *testing.T) {
	dir := t.TempDir()
	// Lexical order matters: 000 creates, 001 adds a column, 002 drops a different column,
	// 003 drops the table entirely (mirrors 068_drop_legacy_incidents.sql's shape).
	writeMigration(t, dir, "000_create.sql", `
CREATE TABLE IF NOT EXISTS widgets (
    id UUID,
    name String,
    org_id String,
    created_at DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree ORDER BY (id);
`)
	writeMigration(t, dir, "001_add_column.sql", `
ALTER TABLE widgets ADD COLUMN IF NOT EXISTS tags Array(String);
`)
	writeMigration(t, dir, "002_drop_column.sql", `
ALTER TABLE widgets DROP COLUMN IF EXISTS name;
`)
	writeMigration(t, dir, "003_drop_table.sql", `
-- widgets_legacy was never created in this fixture; make sure dropping an unknown table
-- doesn't blow up the replay.
DROP TABLE IF EXISTS widgets_legacy;
`)

	schema, _, err := replayMigrationsDir(dir)
	if err != nil {
		t.Fatalf("replayMigrationsDir: %v", err)
	}
	if !schema.tableExists("widgets") {
		t.Fatal("widgets should exist after CREATE TABLE")
	}
	if !schema.columnExists("widgets", "org_id") {
		t.Fatal("org_id should exist (from CREATE TABLE)")
	}
	if !schema.columnExists("widgets", "tags") {
		t.Fatal("tags should exist (from ADD COLUMN)")
	}
	if schema.columnExists("widgets", "name") {
		t.Fatal("name should have been removed by DROP COLUMN")
	}
	if schema.tableExists("widgets_legacy") {
		t.Fatal("widgets_legacy should not exist")
	}
}

// devhealthschema:not-a-production-replica this DDL is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestReplayMigrationsDir_DropTableRemovesAllColumns(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "000_create.sql", `
CREATE TABLE IF NOT EXISTS incidents (
    id UUID,
    repo_id UUID,
    status String
) ENGINE = ReplacingMergeTree ORDER BY (id);
`)
	writeMigration(t, dir, "001_drop.sql", `DROP TABLE IF EXISTS incidents;`)

	schema, _, err := replayMigrationsDir(dir)
	if err != nil {
		t.Fatalf("replayMigrationsDir: %v", err)
	}
	if schema.tableExists("incidents") {
		t.Fatal("incidents should not exist after DROP TABLE (CHAOS-3062 shape)")
	}
}

// devhealthschema:not-a-production-replica this DDL is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestReplayMigrationsDir_MultipleClausesInOneAlter(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "000_create.sql", `
CREATE TABLE IF NOT EXISTS repos (
    id UUID,
    repo String
) ENGINE = ReplacingMergeTree ORDER BY (id);
`)
	writeMigration(t, dir, "001_alter.sql", `
ALTER TABLE repos
    ADD COLUMN IF NOT EXISTS org_id String,
    ADD COLUMN IF NOT EXISTS provider String,
    DROP COLUMN IF EXISTS repo;
`)

	schema, _, err := replayMigrationsDir(dir)
	if err != nil {
		t.Fatalf("replayMigrationsDir: %v", err)
	}
	for _, col := range []string{"id", "org_id", "provider"} {
		if !schema.columnExists("repos", col) {
			t.Fatalf("expected column %s to exist", col)
		}
	}
	if schema.columnExists("repos", "repo") {
		t.Fatal("repo should have been dropped by the same ALTER TABLE statement")
	}
}

// TestReplayMigrationsDir_RenameColumn is Codex finding 12: RENAME COLUMN previously fell
// into the neutral default branch of applyAlterTable, which left the OLD name in the schema
// and never added the NEW one -- a seed using either name would be graded against the wrong
// truth (the old name incorrectly still "exists"; the new name incorrectly does not).
// devhealthschema:not-a-production-replica this DDL is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestReplayMigrationsDir_RenameColumn(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "000_create.sql", `
CREATE TABLE IF NOT EXISTS git_commits (
    repo_id UUID,
    hash String,
    author_when DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree ORDER BY (repo_id, hash);
`)
	writeMigration(t, dir, "001_rename.sql", `
ALTER TABLE git_commits RENAME COLUMN IF EXISTS author_when TO committer_when;
`)

	schema, _, err := replayMigrationsDir(dir)
	if err != nil {
		t.Fatalf("replayMigrationsDir: %v", err)
	}
	if schema.columnExists("git_commits", "author_when") {
		t.Fatal("author_when should have been renamed away")
	}
	if !schema.columnExists("git_commits", "committer_when") {
		t.Fatal("committer_when should exist after the rename")
	}
	if !schema.columnExists("git_commits", "repo_id") || !schema.columnExists("git_commits", "hash") {
		t.Fatal("the rename must not disturb unrelated columns")
	}
}

// TestVerifySeedAgainstSchema_BadColumnAndBadArity is the case the team lead asked for
// explicitly: an INSERT referencing a column the effective schema does not have (the actual
// CHAOS-3065 bug -- org_id on a table that never gained it), and an INSERT whose VALUES
// tuple arity does not match its column list.
func TestVerifySeedAgainstSchema_BadColumnAndBadArity(t *testing.T) {
	schema := newCHSchema()
	widgets := schema.createTable("git_commits")
	for _, c := range []string{"repo_id", "hash", "committer_when"} {
		widgets[c] = true
	}
	// git_commits deliberately has no org_id, matching the real CHAOS-3065 bug shape.

	seed := `
-- bad column: org_id does not exist on git_commits
INSERT INTO git_commits
    (repo_id, hash, committer_when, org_id)
VALUES
    ('00000000-0000-0000-0000-000000000001', 'deadbeef', '2026-01-01 00:00:00.000', '__ORG_ID__');

-- bad arity: 3 columns declared, second tuple supplies 4 values
INSERT INTO git_commits
    (repo_id, hash, committer_when)
VALUES
    ('00000000-0000-0000-0000-000000000001', 'deadbeef', '2026-01-01 00:00:00.000'),
    ('00000000-0000-0000-0000-000000000001', 'cafef00d', '2026-01-01 00:00:00.000', 'extra');
`
	issues, err := verifySeedAgainstSchema(schema, seed, "seed.sql")
	if err != nil {
		t.Fatalf("verifySeedAgainstSchema: %v", err)
	}

	var sawBadColumn, sawBadArity bool
	for _, issue := range issues {
		if issue.Column == "org_id" {
			sawBadColumn = true
		}
		if issue.Tuple == 2 && issue.Wanted == 3 && issue.Got == 4 {
			sawBadArity = true
		}
		t.Logf("issue: %s", issue.String())
	}
	if !sawBadColumn {
		t.Fatal("expected an issue for git_commits.org_id (does not exist)")
	}
	if !sawBadArity {
		t.Fatal("expected an arity mismatch on the second VALUES tuple")
	}
}

func TestVerifySeedAgainstSchema_CleanSeedProducesNoIssues(t *testing.T) {
	schema := newCHSchema()
	repos := schema.createTable("repos")
	for _, c := range []string{"id", "repo", "ref", "created_at", "settings", "tags", "last_synced", "org_id", "provider"} {
		repos[c] = true
	}
	seed := `
INSERT INTO repos
    (id, repo, ref, created_at, settings, tags, last_synced, org_id, provider)
VALUES
    ('00000000-3065-4000-8000-000000000001', 'example-org/widget-service', 'main',
     '2026-01-01 00:00:00.000', NULL, NULL, '2026-01-14 12:00:00.000', '__ORG_ID__', 'synthetic');
`
	issues, err := verifySeedAgainstSchema(schema, seed, "seed.sql")
	if err != nil {
		t.Fatalf("verifySeedAgainstSchema: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %+v", issues)
	}
}

func TestVerifySeedAgainstSchema_UnknownTable(t *testing.T) {
	schema := newCHSchema()
	issues, err := verifySeedAgainstSchema(schema, `INSERT INTO ghost (a, b) VALUES (1, 2);`, "seed.sql")
	if err != nil {
		t.Fatalf("verifySeedAgainstSchema: %v", err)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].String(), "does not exist") {
		t.Fatalf("expected a single 'table does not exist' issue, got %+v", issues)
	}
}

// devhealthschema:not-a-production-replica this DDL is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestRunVerifySeedSchema_EndToEnd(t *testing.T) {
	migrations := t.TempDir()
	writeMigration(t, migrations, "000_create.sql", `
CREATE TABLE IF NOT EXISTS git_commits (
    repo_id UUID,
    hash String,
    committer_when DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree ORDER BY (repo_id, hash);
`)
	seedDir := t.TempDir()
	writeFile(t, filepath.Join(seedDir, "001_seed.sql"), `
INSERT INTO git_commits (repo_id, hash, committer_when, org_id)
VALUES ('00000000-0000-0000-0000-000000000001', 'deadbeef', '2026-01-01 00:00:00.000', '__ORG_ID__');
`)

	code := runVerifySeedSchema([]string{"--seed-dir", seedDir, "--migrations-dir", migrations})
	if code == 0 {
		t.Fatal("expected a nonzero exit for a seed file referencing a nonexistent org_id column")
	}

	// A clean seed against the same schema must pass.
	writeFile(t, filepath.Join(seedDir, "001_seed.sql"), `
INSERT INTO git_commits (repo_id, hash, committer_when)
VALUES ('00000000-0000-0000-0000-000000000001', 'deadbeef', '2026-01-01 00:00:00.000');
`)
	if code := runVerifySeedSchema([]string{"--seed-dir", seedDir, "--migrations-dir", migrations}); code != 0 {
		t.Fatalf("expected a clean seed to pass, got exit code %d", code)
	}
}

// TestRunVerifySeedSchema_FailsClosedWhenUnhandledDDLTouchesSeededTable: DDL the replay cannot
// attribute to a table (a RENAME TABLE delta) must fail the run, not print a warning -- the
// replay's picture of the seeded table's columns may be wrong, which is exactly the condition
// that let a seed pass here and fail against the real schema.
// devhealthschema:not-a-production-replica this layout is INPUT to the migration parser under test,
// never a fixture any Context Fabric reader queries. The
// table names are incidental; the test asserts how statements are replayed.
func TestRunVerifySeedSchema_FailsClosedWhenUnhandledDDLTouchesSeededTable(t *testing.T) {
	migrations := t.TempDir()
	if err := os.MkdirAll(filepath.Join(migrations, "baseline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(migrations, "sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	baseline := `{"objects":[{"name":"git_commits","create":"CREATE TABLE git_commits (` + "`repo_id`" + ` UUID, ` + "`hash`" + ` String) ENGINE = ReplacingMergeTree ORDER BY (repo_id, hash)"}]}`
	if err := os.WriteFile(filepath.Join(migrations, "baseline", "head.json"), []byte(baseline), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMigration(t, filepath.Join(migrations, "sql"), "100_mystery.sql", "RENAME TABLE git_commits TO git_commits_old;")
	seedDir := t.TempDir()
	writeFile(t, filepath.Join(seedDir, "001_seed.sql"), `
INSERT INTO git_commits (repo_id, hash) VALUES ('r', 'h');
`)

	code := runVerifySeedSchema([]string{"--seed-dir", seedDir, "--migrations-dir", migrations})
	if code == 0 {
		t.Fatal("expected verify-seed-schema to fail closed: the unattributed DDL's affected table is unknown")
	}
}

// devhealthschema:not-a-production-replica this layout is INPUT to the baseline-replay parser
// under test; the test asserts baseline objects and deltas are both replayed.
func TestReplayMigrationsDir_ChmigrateLayoutReplaysBaselineThenDeltas(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "baseline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	baseline := `{"objects":[{"name":"widgets","create":"CREATE TABLE widgets (` + "`id`" + ` UUID, ` + "`org_id`" + ` String) ENGINE = MergeTree ORDER BY id"}]}`
	if err := os.WriteFile(filepath.Join(dir, "baseline", "head.json"), []byte(baseline), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMigration(t, filepath.Join(dir, "sql"), "100_add.sql", "ALTER TABLE widgets ADD COLUMN IF NOT EXISTS ordering_contract UInt8;")
	schema, unhandled, err := replayMigrationsDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(unhandled) != 0 {
		t.Fatalf("unhandled DDL: %v", unhandled)
	}
	issues, err := verifySeedAgainstSchema(schema, "INSERT INTO widgets (id, org_id, ordering_contract) VALUES ('a', 'b', 2);", "s.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("baseline column or delta column missing: %v", issues)
	}
	issues, err = verifySeedAgainstSchema(schema, "INSERT INTO widgets (id, nope) VALUES ('a', 'b');", "s.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) == 0 {
		t.Fatal("a column absent from baseline and deltas must be reported")
	}
}

// devhealthschema:not-a-production-replica this baseline is INPUT to the replay parser under test;
// the test asserts a RENAME TABLE delta is reported rather than ignored.
func TestReplayMigrationsDir_ChmigrateRenameIsReportedNotIgnored(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"baseline", "sql"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	baseline := `{"objects":[{"name":"widgets","create":"CREATE TABLE widgets (` + "`id`" + ` UUID) ENGINE = MergeTree ORDER BY id"}]}`
	if err := os.WriteFile(filepath.Join(dir, "baseline", "head.json"), []byte(baseline), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMigration(t, filepath.Join(dir, "sql"), "100_rename.sql", "RENAME TABLE widgets TO widgets_v2;")
	_, unhandled, err := replayMigrationsDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(unhandled) != 1 {
		t.Fatalf("a RENAME TABLE delta must be reported as unhandled, got %v", unhandled)
	}
}
