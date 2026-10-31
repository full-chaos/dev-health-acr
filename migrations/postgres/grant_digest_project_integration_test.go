package postgres

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// CHAOS-12227: 0066 widens the stored-result grant_digest CHECK to the
// project-scope form 'p:<32 hex>' (CHAOS-12107 design v3 V2).

const grantDigestProjectMigration = "0066_context_fabric_result_grant_digest_project.sql"

// chainBefore is every embedded migration before the named one.
func chainBefore(t *testing.T, name string) *Runner {
	t.Helper()
	pre := fstest.MapFS{}
	entries, err := fs.ReadDir(Files, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= name {
			continue
		}
		body, err := fs.ReadFile(Files, entry.Name())
		require.NoError(t, err)
		pre[entry.Name()] = &fstest.MapFile{Data: body}
	}
	runner, err := NewRunner(pre)
	require.NoError(t, err)
	return runner
}

func insertGrantDigest(ctx context.Context, db *sql.DB, resultID string, digest any) error {
	_, err := db.ExecContext(ctx, `INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at, grant_digest) VALUES ($1, 'org-0066', '{}'::jsonb, now(), $2)`, resultID, digest)
	return err
}

func grantDigestCheck(t *testing.T, ctx context.Context, db *sql.DB) (definition string, validated bool) {
	t.Helper()
	require.NoError(t, db.QueryRowContext(ctx, `SELECT pg_get_constraintdef(oid), convalidated FROM pg_constraint WHERE conname = 'ck_acr_cf_investigation_results_grant_digest_shape'`).Scan(&definition, &validated))
	return definition, validated
}

// The first statement of 0066 bounds its lock wait, scoped to the file's own
// transaction.
func TestMigration0066BoundsItsLockWaitFirst(t *testing.T) {
	t.Parallel()
	body, err := fs.ReadFile(Files, grantDigestProjectMigration)
	require.NoError(t, err)
	var statements []string
	for _, line := range strings.Split(string(body), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			statements = append(statements, trimmed)
		}
	}
	require.NotEmpty(t, statements)
	require.Equal(t, "SET LOCAL lock_timeout = '5s';", statements[0], "the lock bound must come before the ALTER that takes the lock")
	require.Equal(t, 1, strings.Count(string(body), "ALTER TABLE"), "one ALTER, so the lock is taken once")
}

// Upgrade: a database at 0065 refuses 'p:'; after 0066 it takes 'p:', keeps
// every row saved before, still takes every earlier form, and still refuses a
// malformed digest. The constraint stays NOT VALID (the file header's choice).
func TestRunner_upgradeTo66AcceptsTheProjectGrantDigest(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	require.NoError(t, chainBefore(t, grantDigestProjectMigration).Up(ctx, db))

	hex32 := strings.Repeat("0123456789abcdef", 2)
	before := map[string]any{"result-0066-null": nil, "result-0066-open": "open", "result-0066-g": "g:" + hex32}
	for id, digest := range before {
		require.NoError(t, insertGrantDigest(ctx, db, id, digest), "the pre-0066 column takes %v", digest)
	}
	require.Error(t, insertGrantDigest(ctx, db, "result-0066-p-early", "p:"+hex32), "before 0066 the column refuses p:")

	runner, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.Up(ctx, db))

	var kept int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_investigation_results WHERE org_id = 'org-0066'`).Scan(&kept))
	require.Equal(t, len(before), kept, "every row saved before 0066 is kept")
	require.NoError(t, insertGrantDigest(ctx, db, "result-0066-p", "p:"+hex32))
	for i, good := range []any{nil, "open", "g:" + hex32} {
		require.NoError(t, insertGrantDigest(ctx, db, "result-0066-after-"+string(rune('a'+i)), good), "an earlier form %v is still taken", good)
	}
	for i, bad := range []string{"p:" + hex32[:31], "p:" + hex32 + hex32, "P:" + hex32, "p:" + strings.ToUpper(hex32), "p:", "xp:" + hex32} {
		require.Error(t, insertGrantDigest(ctx, db, "result-0066-bad-"+string(rune('a'+i)), bad), "%q is refused", bad)
	}
	definition, validated := grantDigestCheck(t, ctx, db)
	require.Contains(t, definition, "^p:[0-9a-f]{32}$")
	require.Contains(t, definition, "NOT VALID")
	require.False(t, validated, "0066 leaves the CHECK NOT VALID: the superset needs no scan under the ACCESS EXCLUSIVE lock")
}

// A long reader holds the results table: 0066 fails at its lock bound
// (SQLSTATE 55P03) instead of queueing every later query behind it, records
// nothing, and leaves the old CHECK; once the reader is gone it applies.
func TestRunner_migration66FailsAtItsLockBoundBehindAHeldLock(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	require.NoError(t, chainBefore(t, grantDigestProjectMigration).Up(ctx, db))
	oldDefinition, _ := grantDigestCheck(t, ctx, db)

	reader, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	holder, err := reader.BeginTx(ctx, nil)
	require.NoError(t, err)
	// Cleanups run last-in first-out: on a failed assertion the holder rolls
	// back before its connection closes, so the test fails instead of hanging.
	t.Cleanup(func() { _ = holder.Rollback() })
	_, err = holder.ExecContext(ctx, `LOCK TABLE acr.context_fabric_investigation_results IN ACCESS SHARE MODE`)
	require.NoError(t, err)

	runner, err := Embedded()
	require.NoError(t, err)
	// A deadline above the bound: without the bound the wait would end here,
	// with a context error, not 55P03.
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	started := time.Now()
	err = runner.Up(bounded, db)
	waited := time.Since(started)
	require.Error(t, err, "0066 must not wait for the reader without bound")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "want a PostgreSQL error, got %v", err)
	require.Equal(t, "55P03", pgErr.Code, "want lock_not_available from lock_timeout, got %v", err)
	require.GreaterOrEqual(t, waited, 4*time.Second, "the bound is 5s")
	require.Less(t, waited, 30*time.Second)

	var recorded int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.schema_migrations WHERE version = 66`).Scan(&recorded))
	require.Zero(t, recorded, "a failed 0066 records nothing")
	stillOld, _ := grantDigestCheck(t, ctx, db)
	require.Equal(t, oldDefinition, stillOld, "a failed 0066 leaves the old CHECK")
	require.NotContains(t, stillOld, "p:")

	require.NoError(t, holder.Rollback())
	require.NoError(t, runner.Up(ctx, db))
	applied, _ := grantDigestCheck(t, ctx, db)
	require.Regexp(t, regexp.MustCompile(`\^p:\[0-9a-f\]\{32\}\$`), applied)
}
