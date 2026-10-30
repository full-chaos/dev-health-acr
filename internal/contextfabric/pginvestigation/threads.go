package pginvestigation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The thread store (CHAOS-11948) over acr.context_fabric_threads (migration
// 0058) and the thread turns of acr.context_fabric_investigation_submissions.
// AdmitSubmission writes a thread row in the admission's own transaction
// (submissions.go); the reads and the delete are here.

var _ contextfabric.ThreadStore = (*Store)(nil)

const threadColumns = `org_id, owner_auth_method, owner_subject, thread_id, created_at, last_turn_at, turn_count, retention_days, retention_set_by_admitted_at, retention_set_by_submission_id, expires_at, closed_at`

// queryRower is a *sql.DB or a *sql.Tx.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanThread(row submissionScanner) (contextfabric.ThreadRecord, error) {
	var thread contextfabric.ThreadRecord
	var retention int
	var setByAt sql.NullTime
	var setByID sql.NullString
	var closedAt sql.NullTime
	if err := row.Scan(&thread.Key.OrgID, &thread.Key.Owner.AuthenticationMethod, &thread.Key.Owner.Subject, &thread.Key.ThreadID,
		&thread.CreatedAt, &thread.LastTurnAt, &thread.TurnCount, &retention, &setByAt, &setByID, &thread.ExpiresAt, &closedAt); err != nil {
		return contextfabric.ThreadRecord{}, err
	}
	thread.RetentionDays = contractsv1.ContextFabricThreadRetentionDays(retention)
	thread.CreatedAt, thread.LastTurnAt, thread.ExpiresAt = thread.CreatedAt.UTC(), thread.LastTurnAt.UTC(), thread.ExpiresAt.UTC()
	if setByAt.Valid {
		thread.RetentionSetBy = &contextfabric.ThreadTurnCursor{AdmittedAt: setByAt.Time.UTC(), SubmissionID: setByID.String}
	}
	if closedAt.Valid {
		at := closedAt.Time.UTC()
		thread.ClosedAt = &at
	}
	return thread, nil
}

func validThreadKey(key contextfabric.ThreadKey) error {
	if strings.TrimSpace(key.OrgID) == "" || key.Owner.Subject == "" || key.Owner.AuthenticationMethod == "" {
		return errors.New("pginvestigation: thread organization and owner are required")
	}
	if !contextfabric.ValidSubmissionID(key.ThreadID) {
		return errors.New("pginvestigation: thread id is malformed")
	}
	return nil
}

// readThreadForUpdate reads and row-locks a thread inside the admission
// transaction (the organization's admission lock already serializes every
// admission; the row lock also orders a concurrent delete).
func readThreadForUpdate(ctx context.Context, tx *sql.Tx, key contextfabric.ThreadKey) (contextfabric.ThreadRecord, bool, error) {
	thread, err := scanThread(tx.QueryRowContext(ctx, `
SELECT `+threadColumns+` FROM acr.context_fabric_threads
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4
   FOR UPDATE`, key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.ThreadID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.ThreadRecord{}, false, nil
	case err != nil:
		return contextfabric.ThreadRecord{}, false, sanitizeError(err)
	}
	return thread, true, nil
}

// writeThread inserts or replaces the thread row AdvanceThread computed.
func writeThread(ctx context.Context, tx *sql.Tx, thread contextfabric.ThreadRecord) error {
	var setByAt sql.NullTime
	var setByID sql.NullString
	if thread.RetentionSetBy != nil {
		setByAt = sql.NullTime{Time: thread.RetentionSetBy.AdmittedAt.UTC(), Valid: true}
		setByID = sql.NullString{String: thread.RetentionSetBy.SubmissionID, Valid: true}
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO acr.context_fabric_threads (`+threadColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULL)
ON CONFLICT (org_id, owner_auth_method, owner_subject, thread_id) DO UPDATE
   SET created_at = EXCLUDED.created_at, last_turn_at = EXCLUDED.last_turn_at, turn_count = EXCLUDED.turn_count,
       retention_days = EXCLUDED.retention_days, retention_set_by_admitted_at = EXCLUDED.retention_set_by_admitted_at,
       retention_set_by_submission_id = EXCLUDED.retention_set_by_submission_id, expires_at = EXCLUDED.expires_at`,
		thread.Key.OrgID, thread.Key.Owner.AuthenticationMethod, thread.Key.Owner.Subject, thread.Key.ThreadID,
		thread.CreatedAt.UTC(), thread.LastTurnAt.UTC(), thread.TurnCount, int(thread.RetentionDays), setByAt, setByID, thread.ExpiresAt.UTC())
	return sanitizeError(err)
}

// recordThreadClosed is why a record's thread refuses a repeat of the key and
// hides the record from a status read: deleted or expired.
func (s *Store) recordThreadClosed(ctx context.Context, record contextfabric.SubmissionRecord, now time.Time) (contextfabric.ThreadClosedReason, error) {
	if record.ThreadID == "" {
		return "", nil
	}
	var closedAt sql.NullTime
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx, `
SELECT closed_at, expires_at FROM acr.context_fabric_threads
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4`,
		record.Key.OrgID, record.Key.Owner.AuthenticationMethod, record.Key.Owner.Subject, record.ThreadID).Scan(&closedAt, &expiresAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read submission thread: %w", sanitizeError(err))
	case closedAt.Valid:
		return contextfabric.ThreadClosedDeleted, nil
	case !now.Before(expiresAt):
		return contextfabric.ThreadClosedExpired, nil
	}
	return "", nil
}

// hiddenByClosedThreadSQL is the closed-thread rule for a result row r
// (design §6 T6): r is named by at least one thread turn of its organization
// (as the turn's final or reserved result) and by no record outside a closed
// thread. $now is the store clock. A result one live turn, or one record with
// no thread, names stays readable (T3).
func hiddenByClosedThreadSQL(now string) string {
	return `(EXISTS (SELECT 1 FROM acr.context_fabric_investigation_submissions hs
                 WHERE hs.org_id = r.org_id AND (hs.result_id = r.result_id OR hs.reserved_result_id = r.result_id)
                   AND hs.thread_id IS NOT NULL)
     AND NOT EXISTS (SELECT 1 FROM acr.context_fabric_investigation_submissions ls
                      LEFT JOIN acr.context_fabric_threads lt
                        ON lt.org_id = ls.org_id AND lt.owner_auth_method = ls.owner_auth_method
                       AND lt.owner_subject = ls.owner_subject AND lt.thread_id = ls.thread_id
                     WHERE ls.org_id = r.org_id AND (ls.result_id = r.result_id OR ls.reserved_result_id = r.result_id)
                       AND (ls.thread_id IS NULL OR lt.thread_id IS NULL
                            OR (lt.closed_at IS NULL AND lt.expires_at > ` + now + `))))`
}

// ListThreads implements contextfabric.ThreadStore.
func (s *Store) ListThreads(ctx context.Context, orgID string, owner contextfabric.SubmissionOwner, after *contextfabric.ThreadListCursor, limit int, now time.Time) ([]contextfabric.ThreadRecord, bool, error) {
	if err := s.configured(); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(orgID) == "" || owner.Subject == "" || owner.AuthenticationMethod == "" || limit < 1 {
		return nil, false, errors.New("pginvestigation: thread list organization, owner and a positive limit are required")
	}
	args := []any{orgID, owner.AuthenticationMethod, owner.Subject, now.UTC(), limit + 1}
	keyset := ""
	if after != nil {
		keyset = ` AND (last_turn_at < $6 OR (last_turn_at = $6 AND thread_id > $7))`
		args = append(args, after.LastTurnAt.UTC(), after.ThreadID)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+threadColumns+` FROM acr.context_fabric_threads
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3
   AND closed_at IS NULL AND expires_at > $4`+keyset+`
 ORDER BY last_turn_at DESC, thread_id ASC
 LIMIT $5`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list threads: %w", sanitizeError(err))
	}
	defer rows.Close()
	var threads []contextfabric.ThreadRecord
	for rows.Next() {
		thread, err := scanThread(rows)
		if err != nil {
			return nil, false, fmt.Errorf("list threads: %w", sanitizeError(err))
		}
		threads = append(threads, thread)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list threads: %w", sanitizeError(err))
	}
	more := len(threads) > limit
	if more {
		threads = threads[:limit]
	}
	return threads, more, nil
}

// GetThread implements contextfabric.ThreadStore: the reconcile update for
// the thread's unfinished turns, then the reads.
func (s *Store) GetThread(ctx context.Context, key contextfabric.ThreadKey, before *contextfabric.ThreadTurnCursor, limit int, now time.Time) (contextfabric.ThreadPage, error) {
	if err := s.configured(); err != nil {
		return contextfabric.ThreadPage{}, err
	}
	if err := validThreadKey(key); err != nil || limit < 1 {
		return contextfabric.ThreadPage{}, contextfabric.ErrThreadNotFound
	}
	thread, err := scanThread(s.db.QueryRowContext(ctx, `
SELECT `+threadColumns+` FROM acr.context_fabric_threads
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.ThreadID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.ThreadPage{}, contextfabric.ErrThreadNotFound
	case err != nil:
		return contextfabric.ThreadPage{}, fmt.Errorf("read thread: %w", sanitizeError(err))
	case thread.Closed(now):
		return contextfabric.ThreadPage{}, contextfabric.ErrThreadNotFound
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions s`+reconcileSetSQL("$5")+`
   AND s.org_id = $1 AND s.owner_auth_method = $2 AND s.owner_subject = $3 AND s.thread_id = $4`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.ThreadID, now.UTC()); err != nil {
		return contextfabric.ThreadPage{}, fmt.Errorf("reconcile thread turns: %w", sanitizeError(err))
	}
	args := []any{key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.ThreadID, limit + 1}
	keyset := ""
	if before != nil {
		keyset = ` AND (admitted_at < $6 OR (admitted_at = $6 AND submission_id < $7))`
		args = append(args, before.AdmittedAt.UTC(), before.SubmissionID)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+submissionColumns+` FROM acr.context_fabric_investigation_submissions
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4`+keyset+`
 ORDER BY admitted_at DESC, submission_id DESC
 LIMIT $5`, args...)
	if err != nil {
		return contextfabric.ThreadPage{}, fmt.Errorf("read thread turns: %w", sanitizeError(err))
	}
	defer rows.Close()
	var turns []contextfabric.ThreadTurn
	for rows.Next() {
		record, err := scanSubmission(rows)
		if err != nil {
			return contextfabric.ThreadPage{}, fmt.Errorf("read thread turns: %w", sanitizeError(err))
		}
		turns = append(turns, contextfabric.ThreadTurn{
			SubmissionID: record.Key.SubmissionID, State: record.State, ResultID: record.ResultID,
			Question: record.Question, AdmittedAt: record.AdmittedAt, UpdatedAt: record.UpdatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return contextfabric.ThreadPage{}, fmt.Errorf("read thread turns: %w", sanitizeError(err))
	}
	earlier := len(turns) > limit
	if earlier {
		turns = turns[:limit]
	}
	if len(turns) == 0 {
		// A cursor before the first turn names no page.
		return contextfabric.ThreadPage{}, contextfabric.ErrThreadNotFound
	}
	// Newest first from the read; the page is oldest first.
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	return contextfabric.ThreadPage{Thread: thread, Turns: turns, EarlierTurns: earlier}, nil
}

// CloseThread implements contextfabric.ThreadStore: under the organization's
// admission lock (so no turn is admitted half-way through a delete), the
// closure and the erasure of the turns' questions, in one transaction.
func (s *Store) CloseThread(ctx context.Context, key contextfabric.ThreadKey, now time.Time) (bool, error) {
	if err := s.configured(); err != nil {
		return false, err
	}
	if err := validThreadKey(key); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("close thread: begin: %w", sanitizeError(err))
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, submissionAdmissionLockClass, key.OrgID); err != nil {
		return false, fmt.Errorf("close thread: lock: %w", sanitizeError(err))
	}
	result, err := tx.ExecContext(ctx, `
UPDATE acr.context_fabric_threads SET closed_at = $5
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4
   AND closed_at IS NULL AND expires_at > $5`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.ThreadID, now.UTC())
	if err != nil {
		return false, fmt.Errorf("close thread: %w", sanitizeError(err))
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return false, fmt.Errorf("close thread: %w", sanitizeError(err))
		}
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions SET question = NULL
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4 AND question IS NOT NULL`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.ThreadID); err != nil {
		return false, fmt.Errorf("close thread: erase questions: %w", sanitizeError(err))
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("close thread: commit: %w", sanitizeError(err))
	}
	return true, nil
}
