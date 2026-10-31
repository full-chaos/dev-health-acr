package pginvestigation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The retention job's store side (CHAOS-11952): contextfabric.RetentionSweeper
// over the Ask Dev backing set. contextfabric/retention.go states the rules;
// this file holds them in SQL. Each unit is one transaction under the
// organization's admission lock (the lock CloseThread and AdmitSubmission
// take), so no turn is admitted half-way through a removal and no reader sees
// a result live after the thread or record that closed it is gone: the
// results go first, in the same transaction as their records.

var _ contextfabric.RetentionSweeper = (*Store)(nil)

// retentionStep calls the store's retention step hook, when one is set
// (tests only, export_test.go SetRetentionStepHook): after each statement of
// a unit's transaction, so a test can read through another connection
// mid-unit. Production leaves it nil.
func (s *Store) retentionStep(step string) {
	if s.retentionStepHook != nil {
		s.retentionStepHook(step)
	}
}

// erasedReservedPrefixSQL is contextfabric.ErasedReservedResultIDPrefix as a
// SQL literal.
var erasedReservedPrefixSQL = "'" + contextfabric.ErasedReservedResultIDPrefix + "'"

// threadRecordsSQL selects the records of the thread row ($1 org, $2 method,
// $3 subject, $4 thread id): the turns admitted with that thread id whose
// thread owner is the row's owner (threadTurnOwner*SQL).
const threadRecordsSQL = ` s.org_id = $1 AND s.thread_id = $4
   AND ` + threadTurnOwnerMethodSQL + ` = $2 AND ` + threadTurnOwnerSubjectSQL + ` = $3`

type retentionThreadUnit struct {
	orgID, method, subject, threadID string
}

type retentionRecordUnit struct {
	orgID, method, subject, submissionID string
}

// SweepRetention implements contextfabric.RetentionSweeper.
func (s *Store) SweepRetention(ctx context.Context, now time.Time, limit int) (contextfabric.RetentionSweep, error) {
	var sweep contextfabric.RetentionSweep
	if err := s.configured(); err != nil {
		return sweep, err
	}
	if !s.unthreadedRetention {
		return sweep, contextfabric.ErrRetentionReadRuleOff
	}
	if limit < 1 {
		limit = contextfabric.RetentionSweepBatch
	}
	now = now.UTC()
	threads, err := s.dueRetentionThreads(ctx, now, limit)
	if err != nil {
		return sweep, err
	}
	for _, unit := range threads {
		if err := s.sweepRetentionThread(ctx, unit, now, &sweep); err != nil {
			return sweep, err
		}
	}
	if remaining := limit - len(threads); remaining > 0 {
		records, err := s.dueRetentionRecords(ctx, now, remaining)
		if err != nil {
			return sweep, err
		}
		for _, unit := range records {
			if err := s.sweepRetentionRecord(ctx, unit, now, &sweep); err != nil {
				return sweep, err
			}
		}
	}
	return sweep, nil
}

// retentionProbeStatements are the kinds of statement the job runs, per
// table, on no row: a SELECT and a DELETE on every table the job removes from,
// and the UPDATE (also what SELECT ... FOR UPDATE needs) on the records. A
// role missing any privilege dbacl.RetentionGrants declares is refused on one
// of them.
var retentionProbeStatements = func() []string {
	var statements []string
	for _, table := range []string{
		"context_fabric_threads", "context_fabric_investigation_submissions", "context_fabric_investigation_results",
		"context_fabric_investigation_run_events", "context_fabric_clarification_selections", "context_fabric_structure_selections",
		"context_fabric_structure_supersession_claims",
	} {
		statements = append(statements, `SELECT 1 FROM acr.`+table+` WHERE false`, `DELETE FROM acr.`+table+` WHERE false`)
	}
	return append(statements, `UPDATE acr.context_fabric_investigation_submissions SET updated_at = updated_at WHERE false`)
}()

// ProbeRetention implements contextfabric.RetentionSweeper: the probe
// statements in one transaction it rolls back.
func (s *Store) ProbeRetention(ctx context.Context) error {
	if err := s.configured(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("retention probe: begin: %w", sanitizeError(err))
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range retentionProbeStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("retention probe: %w", sanitizeError(err))
		}
	}
	return nil
}

// dueRetentionThreads lists thread rows that are expired, or deleted with a
// turn not yet erased, oldest expiry first.
func (s *Store) dueRetentionThreads(ctx context.Context, now time.Time, limit int) ([]retentionThreadUnit, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT t.org_id, t.owner_auth_method, t.owner_subject, t.thread_id
  FROM acr.context_fabric_threads t
 WHERE t.expires_at <= $1
    OR (t.closed_at IS NOT NULL AND EXISTS (
          SELECT 1 FROM acr.context_fabric_investigation_submissions s
           WHERE s.org_id = t.org_id AND s.thread_id = t.thread_id
             AND `+threadTurnOwnerMethodSQL+` = t.owner_auth_method AND `+threadTurnOwnerSubjectSQL+` = t.owner_subject
             AND left(s.reserved_result_id, `+fmt.Sprint(len(contextfabric.ErasedReservedResultIDPrefix))+`) <> `+erasedReservedPrefixSQL+`))
 ORDER BY t.expires_at, t.org_id, t.thread_id
 LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("retention: list due threads: %w", sanitizeError(err))
	}
	defer rows.Close()
	var units []retentionThreadUnit
	for rows.Next() {
		var unit retentionThreadUnit
		if err := rows.Scan(&unit.orgID, &unit.method, &unit.subject, &unit.threadID); err != nil {
			return nil, fmt.Errorf("retention: list due threads: %w", sanitizeError(err))
		}
		units = append(units, unit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("retention: list due threads: %w", sanitizeError(err))
	}
	return units, nil
}

// dueRetentionRecords lists records with no thread past their retention-0
// window, oldest admission first.
func (s *Store) dueRetentionRecords(ctx context.Context, now time.Time, limit int) ([]retentionRecordUnit, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT org_id, owner_auth_method, owner_subject, submission_id
  FROM acr.context_fabric_investigation_submissions
 WHERE thread_id IS NULL AND admitted_at + `+unthreadedRecordWindowSQL+` <= $1
 ORDER BY admitted_at, org_id, submission_id
 LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("retention: list due records: %w", sanitizeError(err))
	}
	defer rows.Close()
	var units []retentionRecordUnit
	for rows.Next() {
		var unit retentionRecordUnit
		if err := rows.Scan(&unit.orgID, &unit.method, &unit.subject, &unit.submissionID); err != nil {
			return nil, fmt.Errorf("retention: list due records: %w", sanitizeError(err))
		}
		units = append(units, unit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("retention: list due records: %w", sanitizeError(err))
	}
	return units, nil
}

// retentionRecord is the part of a record a unit decides on.
type retentionRecord struct {
	record   contextfabric.SubmissionRecord
	reserved string
	resultID string
}

func scanRetentionRecords(rows *sql.Rows) ([]retentionRecord, error) {
	defer rows.Close()
	var records []retentionRecord
	for rows.Next() {
		var r retentionRecord
		var state string
		var resultID sql.NullString
		if err := rows.Scan(&state, &r.record.ExpiresAt, &r.reserved, &resultID); err != nil {
			return nil, err
		}
		r.record.State = contextfabric.SubmissionState(state)
		r.record.ExpiresAt = r.record.ExpiresAt.UTC()
		r.resultID = resultID.String
		records = append(records, r)
	}
	return records, rows.Err()
}

// beginRetentionUnit opens a unit's transaction under the organization's
// admission lock.
func (s *Store) beginRetentionUnit(ctx context.Context, orgID string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("retention: begin: %w", sanitizeError(err))
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, submissionAdmissionLockClass, orgID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("retention: lock: %w", sanitizeError(err))
	}
	return tx, nil
}

// sweepRetentionThread removes an expired thread's whole backing set, or a
// deleted thread's content (keeping its closure record), in one transaction.
func (s *Store) sweepRetentionThread(ctx context.Context, unit retentionThreadUnit, now time.Time, sweep *contextfabric.RetentionSweep) error {
	tx, err := s.beginRetentionUnit(ctx, unit.orgID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds
	var expiresAt time.Time
	var closedAt sql.NullTime
	// No row lock: the organization's admission lock, held for the whole
	// unit, already serializes it with every admission and delete, the only
	// writers of a thread row (and the retention role holds no UPDATE on
	// the threads, dbacl.RetentionGrants).
	err = tx.QueryRowContext(ctx, `
SELECT expires_at, closed_at FROM acr.context_fabric_threads
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4`, unit.orgID, unit.method, unit.subject, unit.threadID).Scan(&expiresAt, &closedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Another pass removed it first.
		return nil
	case err != nil:
		return fmt.Errorf("retention: read thread: %w", sanitizeError(err))
	}
	expired := !now.Before(expiresAt)
	if !expired && !closedAt.Valid {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
SELECT s.state, s.expires_at, s.reserved_result_id, s.result_id FROM acr.context_fabric_investigation_submissions s
 WHERE`+threadRecordsSQL+`
   FOR UPDATE`, unit.orgID, unit.method, unit.subject, unit.threadID)
	if err != nil {
		return fmt.Errorf("retention: read thread records: %w", sanitizeError(err))
	}
	records, err := scanRetentionRecords(rows)
	if err != nil {
		return fmt.Errorf("retention: read thread records: %w", sanitizeError(err))
	}
	for _, r := range records {
		if contextfabric.RetentionFenced(r.record, now) {
			sweep.Fenced++
			return nil
		}
	}
	removed, err := s.removeRetainedResults(ctx, tx, unit.orgID, records, now)
	if err != nil {
		return err
	}
	if expired {
		// The records (their event logs go by the foreign key's cascade),
		// then the thread row.
		if _, err := tx.ExecContext(ctx, `
DELETE FROM acr.context_fabric_investigation_submissions s
 WHERE`+threadRecordsSQL, unit.orgID, unit.method, unit.subject, unit.threadID); err != nil {
			return fmt.Errorf("retention: remove thread records: %w", sanitizeError(err))
		}
		s.retentionStep("records")
		if _, err := tx.ExecContext(ctx, `
DELETE FROM acr.context_fabric_threads
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND thread_id = $4`,
			unit.orgID, unit.method, unit.subject, unit.threadID); err != nil {
			return fmt.Errorf("retention: remove thread: %w", sanitizeError(err))
		}
		s.retentionStep("thread")
	} else {
		// Deleted, not expired (lead ruling Q1): the closure record stays
		// content-free until expires_at (design §6 T1). The event logs go
		// first (their foreign key names the reserved id the erase
		// replaces); each record keeps its id and digest, and loses its
		// result ids and its question.
		if _, err := tx.ExecContext(ctx, `
DELETE FROM acr.context_fabric_investigation_run_events e
 USING acr.context_fabric_investigation_submissions s
 WHERE e.org_id = s.org_id AND e.reserved_result_id = s.reserved_result_id AND`+threadRecordsSQL,
			unit.orgID, unit.method, unit.subject, unit.threadID); err != nil {
			return fmt.Errorf("retention: remove thread event logs: %w", sanitizeError(err))
		}
		s.retentionStep("events")
		if _, err := tx.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions s
   SET state = CASE WHEN s.state = 'done' THEN 'done_unsaved' ELSE s.state END,
       result_id = NULL, question = NULL,
       reserved_result_id = `+erasedReservedPrefixSQL+` || encode(sha256(convert_to(s.reserved_result_id, 'UTF8')), 'hex'),
       updated_at = $5
 WHERE`+threadRecordsSQL+`
   AND left(s.reserved_result_id, `+fmt.Sprint(len(contextfabric.ErasedReservedResultIDPrefix))+`) <> `+erasedReservedPrefixSQL,
			unit.orgID, unit.method, unit.subject, unit.threadID, now); err != nil {
			return fmt.Errorf("retention: erase thread records: %w", sanitizeError(err))
		}
		s.retentionStep("erase")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("retention: commit thread: %w", sanitizeError(err))
	}
	sweep.ResultsRemoved += removed
	if expired {
		sweep.ThreadsRemoved++
	} else {
		sweep.ThreadsErased++
	}
	return nil
}

// sweepRetentionRecord removes a record with no thread past its retention-0
// window, with its result, in one transaction.
func (s *Store) sweepRetentionRecord(ctx context.Context, unit retentionRecordUnit, now time.Time, sweep *contextfabric.RetentionSweep) error {
	tx, err := s.beginRetentionUnit(ctx, unit.orgID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds
	rows, err := tx.QueryContext(ctx, `
SELECT state, expires_at, reserved_result_id, result_id FROM acr.context_fabric_investigation_submissions
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND submission_id = $4
   AND thread_id IS NULL AND admitted_at + `+unthreadedRecordWindowSQL+` <= $5
   FOR UPDATE`, unit.orgID, unit.method, unit.subject, unit.submissionID, now)
	if err != nil {
		return fmt.Errorf("retention: read record: %w", sanitizeError(err))
	}
	records, err := scanRetentionRecords(rows)
	if err != nil {
		return fmt.Errorf("retention: read record: %w", sanitizeError(err))
	}
	if len(records) == 0 {
		return nil
	}
	if contextfabric.RetentionFenced(records[0].record, now) {
		sweep.Fenced++
		return nil
	}
	removed, err := s.removeRetainedResults(ctx, tx, unit.orgID, records, now)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM acr.context_fabric_investigation_submissions
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND submission_id = $4`,
		unit.orgID, unit.method, unit.subject, unit.submissionID); err != nil {
		return fmt.Errorf("retention: remove record: %w", sanitizeError(err))
	}
	s.retentionStep("record")
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("retention: commit record: %w", sanitizeError(err))
	}
	sweep.ResultsRemoved += removed
	sweep.RecordsRemoved++
	return nil
}

// removeRetainedResults removes the results the unit's records name, except
// a result a live record still names (design §6 T3), with what is keyed by
// them: the receipt selections and the supersession claims made on them, and
// (by the foreign key's cascade) their feedback. It runs before the records
// go, in the unit's transaction. It returns how many result rows went.
func (s *Store) removeRetainedResults(ctx context.Context, tx *sql.Tx, orgID string, records []retentionRecord, now time.Time) (int, error) {
	var named []string
	seen := map[string]bool{}
	for _, r := range records {
		for _, id := range []string{r.resultID, r.reserved} {
			if id == "" || seen[id] || contextfabric.IsErasedReservedResultID(id) {
				continue
			}
			seen[id] = true
			named = append(named, id)
		}
	}
	if len(named) == 0 {
		return 0, nil
	}
	// The unit's own records are closed (its thread expired or was
	// deleted, or the record is past its window), so liveRecordSQL is false
	// for them: a result is kept only when a record of another live unit
	// names it.
	rows, err := tx.QueryContext(ctx, `
SELECT id FROM unnest($2::text[]) AS named(id)
 WHERE NOT EXISTS (SELECT 1 FROM acr.context_fabric_investigation_submissions ls
                    `+recordThreadJoinSQL("ls", "lt")+`
                   WHERE ls.org_id = $1 AND (ls.result_id = named.id OR ls.reserved_result_id = named.id)
                     AND `+s.liveRecordSQL("ls", "lt", "$3")+`)`, orgID, named, now)
	if err != nil {
		return 0, fmt.Errorf("retention: decide results: %w", sanitizeError(err))
	}
	var doomed []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("retention: decide results: %w", sanitizeError(err))
		}
		doomed = append(doomed, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("retention: decide results: %w", sanitizeError(err))
	}
	rows.Close()
	if len(doomed) == 0 {
		return 0, nil
	}
	for _, table := range []string{"context_fabric_clarification_selections", "context_fabric_structure_selections", "context_fabric_structure_supersession_claims"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM acr.`+table+` WHERE org_id = $1 AND prior_result_id = ANY($2::text[])`, orgID, doomed); err != nil {
			return 0, fmt.Errorf("retention: remove %s: %w", table, sanitizeError(err))
		}
		s.retentionStep(table)
	}
	result, err := tx.ExecContext(ctx, `
DELETE FROM acr.context_fabric_investigation_results WHERE org_id = $1 AND result_id = ANY($2::text[])`, orgID, doomed)
	if err != nil {
		return 0, fmt.Errorf("retention: remove results: %w", sanitizeError(err))
	}
	s.retentionStep("results")
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retention: remove results: %w", sanitizeError(err))
	}
	return int(n), nil
}
