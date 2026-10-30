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
func TestRunner_upgradeTo60BuildsThreadRowsForEarlierThreadTurns(t *testing.T) {
	runUpgradeTo60BuildsThreadRows(t, newTestDatabase(t, context.Background()))
}

func runUpgradeTo60BuildsThreadRows(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	pre := fstest.MapFS{}
	entries, err := fs.ReadDir(Files, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || name >= "0060_" {
			continue
		}
		body, readErr := fs.ReadFile(Files, name)
		require.NoError(t, readErr)
		pre[name] = &fstest.MapFile{Data: body}
	}
	require.Len(t, pre, 59, "every migration before 0060")
	released, err := NewRunner(pre)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))

	first := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	insertAs := func(method, subject, user, submissionID, reserved string, threadID any, admitted time.Time) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO acr.context_fabric_investigation_submissions
    (org_id, owner_auth_method, owner_subject, submission_id, request_digest, reserved_result_id, state, thread_id, admitted_at, updated_at, expires_at, owner_user_id, owner_surface)
VALUES ('org-backfill', $6, $1, $2, 'sha256:'||repeat('a', 64), $3, 'abandoned', $4, $5::timestamptz, $5::timestamptz, $5::timestamptz + interval '10 minutes',
        NULLIF($7::text, ''), CASE WHEN $7::text = '' THEN NULL ELSE 'browser' END)`,
			subject, submissionID, reserved, threadID, admitted, method, user)
		require.NoError(t, err)
	}
	// A web assertion's records carry their subject as the canonical owner (0059).
	insert := func(subject, submissionID, reserved string, threadID any, admitted time.Time) {
		t.Helper()
		insertAs("web_assertion", subject, subject, submissionID, reserved, threadID, admitted)
	}
	insert("user-a", "submission-backfill-1", "result_backfill_1", "thread-backfill-0001", first)
	insert("user-a", "submission-backfill-2", "result_backfill_2", "thread-backfill-0001", first.Add(time.Hour))
	// Another principal's same thread id is another thread.
	insert("user-b", "submission-backfill-3", "result_backfill_3", "thread-backfill-0001", first.Add(2*time.Hour))
	// A submission with no thread has no thread row.
	insert("user-a", "submission-backfill-4", "result_backfill_4", nil, first.Add(3*time.Hour))
	// Two credentials of one user (a rotation) on one thread: two legacy rows;
	// only the earlier takes the canonical owner.
	insertAs("credential", "cred-backfill-1", "user-c", "submission-backfill-5", "result_backfill_5", "thread-backfill-0002", first.Add(4*time.Hour))
	insertAs("credential", "cred-backfill-2", "user-c", "submission-backfill-6", "result_backfill_6", "thread-backfill-0002", first.Add(5*time.Hour))

	latest, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, latest.Up(ctx, db))

	type row struct {
		subject                string
		created, last, expires time.Time
		count, retention       int
		setByAt, closed        sql.NullTime
		setBySubmission        sql.NullString
		user, surface          sql.NullString
	}
	rows, err := db.QueryContext(ctx, `SELECT owner_subject, created_at, last_turn_at, expires_at, turn_count, retention_days,
       retention_set_by_admitted_at, closed_at, retention_set_by_submission_id, owner_user_id, owner_surface
  FROM acr.context_fabric_threads WHERE org_id = 'org-backfill' ORDER BY owner_subject`)
	require.NoError(t, err)
	defer rows.Close()
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.subject, &r.created, &r.last, &r.expires, &r.count, &r.retention, &r.setByAt, &r.closed, &r.setBySubmission, &r.user, &r.surface))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 4, "one thread row per (legacy owner, thread id) with turns")
	c1, c2, a, b := got[0], got[1], got[2], got[3]
	require.Equal(t, "cred-backfill-1", c1.subject)
	require.True(t, c1.user.Valid && c1.user.String == "user-c" && c1.surface.String == "browser", "the earlier rotated row takes the canonical owner: %+v", c1)
	require.Equal(t, "cred-backfill-2", c2.subject)
	require.False(t, c2.user.Valid || c2.surface.Valid, "the later rotated row keeps its legacy owner only: %+v", c2)
	require.True(t, a.user.Valid && a.user.String == "user-a" && a.surface.String == "browser", "user-a canonical owner %+v", a)
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
	require.Equal(t, 4, total)
}
