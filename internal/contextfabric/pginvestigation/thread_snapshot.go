package pginvestigation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// readThreadSnapshot reads a thread row and up to n of its turns (newest
// first) in one repeatable-read, read-only transaction, so a turn admitted
// or a delete committed during the read is in both or in neither: the turn
// count always agrees with the turns (CHAOS-11948). A missing or closed
// thread is contextfabric.ErrThreadNotFound.
//
// This file only reads. The organization write fence needs READ COMMITTED
// for a write to a fenced table, so no write may join this transaction;
// orgpurge's TestNoWriterOfAFencedTableRunsOutsideReadCommitted holds the
// file to that.
func (s *Store) readThreadSnapshot(ctx context.Context, key contextfabric.ThreadKey, before *contextfabric.ThreadTurnCursor, n int, now time.Time) (contextfabric.ThreadRecord, []contextfabric.ThreadTurn, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return contextfabric.ThreadRecord{}, nil, fmt.Errorf("read thread: begin: %w", sanitizeError(err))
	}
	defer func() { _ = tx.Rollback() }()
	thread, err := scanThread(tx.QueryRowContext(ctx, `
SELECT `+threadColumns+` FROM acr.context_fabric_threads
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.ThreadID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.ThreadRecord{}, nil, contextfabric.ErrThreadNotFound
	case err != nil:
		return contextfabric.ThreadRecord{}, nil, fmt.Errorf("read thread: %w", sanitizeError(err))
	case thread.Closed(now):
		return contextfabric.ThreadRecord{}, nil, contextfabric.ErrThreadNotFound
	}
	if s.afterThreadSummaryRead != nil {
		s.afterThreadSummaryRead()
	}
	args := []any{key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.ThreadID, n}
	keyset := ""
	if before != nil {
		keyset = ` AND (admitted_at < $6 OR (admitted_at = $6 AND submission_id < $7))`
		args = append(args, before.AdmittedAt.UTC(), before.SubmissionID)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT `+submissionColumns+` FROM acr.context_fabric_investigation_submissions
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4`+keyset+`
 ORDER BY admitted_at DESC, submission_id DESC
 LIMIT $5`, args...)
	if err != nil {
		return contextfabric.ThreadRecord{}, nil, fmt.Errorf("read thread turns: %w", sanitizeError(err))
	}
	defer rows.Close()
	var turns []contextfabric.ThreadTurn
	for rows.Next() {
		record, err := scanSubmission(rows)
		if err != nil {
			return contextfabric.ThreadRecord{}, nil, fmt.Errorf("read thread turns: %w", sanitizeError(err))
		}
		turns = append(turns, contextfabric.ThreadTurn{
			SubmissionID: record.Key.SubmissionID, State: record.State, ResultID: record.ResultID,
			Question: record.Question, AdmittedAt: record.AdmittedAt, UpdatedAt: record.UpdatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return contextfabric.ThreadRecord{}, nil, fmt.Errorf("read thread turns: %w", sanitizeError(err))
	}
	if err := rows.Close(); err != nil {
		return contextfabric.ThreadRecord{}, nil, fmt.Errorf("read thread turns: %w", sanitizeError(err))
	}
	if err := tx.Commit(); err != nil {
		return contextfabric.ThreadRecord{}, nil, fmt.Errorf("read thread: commit: %w", sanitizeError(err))
	}
	return thread, turns, nil
}
