package postgres

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
)

// Thread turns admitted before the thread table existed (thread_id on the
// submission records since 0056) get their thread row on upgrade, so their
// conversation is served and a later turn counts on from them.
func TestRunner_upgradeTo58BuildsThreadRowsForEarlierThreadTurns(t *testing.T) {
	runUpgradeTo58BuildsThreadRows(t, newTestDatabase(t, context.Background()))
}

func runUpgradeTo58BuildsThreadRows(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	pre := fstest.MapFS{}
	entries, err := fs.ReadDir(Files, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || name >= "0058_" {
			continue
		}
		body, readErr := fs.ReadFile(Files, name)
		require.NoError(t, readErr)
		pre[name] = &fstest.MapFile{Data: body}
	}
	require.Len(t, pre, 57, "every migration before 0058")
	released, err := NewRunner(pre)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))

	first := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	insert := func(subject, submissionID, reserved string, threadID any, admitted time.Time) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO acr.context_fabric_investigation_submissions
    (org_id, owner_auth_method, owner_subject, submission_id, request_digest, reserved_result_id, state, thread_id, admitted_at, updated_at, expires_at)
VALUES ('org-backfill', 'web_assertion', $1, $2, 'sha256:'||repeat('a', 64), $3, 'abandoned', $4, $5::timestamptz, $5::timestamptz, $5::timestamptz + interval '10 minutes')`,
			subject, submissionID, reserved, threadID, admitted)
		require.NoError(t, err)
	}
	insert("user-a", "submission-backfill-1", "result_backfill_1", "thread-backfill-0001", first)
	insert("user-a", "submission-backfill-2", "result_backfill_2", "thread-backfill-0001", first.Add(time.Hour))
	// Another principal's same thread id is another thread.
	insert("user-b", "submission-backfill-3", "result_backfill_3", "thread-backfill-0001", first.Add(2*time.Hour))
	// A submission with no thread has no thread row.
	insert("user-a", "submission-backfill-4", "result_backfill_4", nil, first.Add(3*time.Hour))

	latest, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, latest.Up(ctx, db))

	type row struct {
		subject                string
		created, last, expires time.Time
		count, retention       int
		setByAt, closed        sql.NullTime
		setBySubmission        sql.NullString
	}
	rows, err := db.QueryContext(ctx, `SELECT owner_subject, created_at, last_turn_at, expires_at, turn_count, retention_days,
       retention_set_by_admitted_at, closed_at, retention_set_by_submission_id
  FROM acr.context_fabric_threads WHERE org_id = 'org-backfill' ORDER BY owner_subject`)
	require.NoError(t, err)
	defer rows.Close()
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.subject, &r.created, &r.last, &r.expires, &r.count, &r.retention, &r.setByAt, &r.closed, &r.setBySubmission))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 2, "one thread row per (owner, thread id) with turns")
	a, b := got[0], got[1]
	require.Equal(t, "user-a", a.subject)
	require.True(t, a.created.Equal(first) && a.last.Equal(first.Add(time.Hour)), "user-a times %v %v", a.created, a.last)
	require.Equal(t, 2, a.count)
	require.Equal(t, 0, a.retention, "no earlier turn named a retention: the default")
	require.True(t, a.expires.Equal(first.Add(time.Hour).Add(24*time.Hour)), "user-a expiry %v", a.expires)
	require.False(t, a.setByAt.Valid || a.setBySubmission.Valid || a.closed.Valid)
	require.Equal(t, "user-b", b.subject)
	require.Equal(t, 1, b.count)
	require.True(t, b.created.Equal(first.Add(2*time.Hour)) && b.last.Equal(b.created))

	// The upgrade is idempotent: a second run changes nothing.
	require.NoError(t, latest.Up(ctx, db))
	var total int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_threads WHERE org_id = 'org-backfill'`).Scan(&total))
	require.Equal(t, 2, total)
}
