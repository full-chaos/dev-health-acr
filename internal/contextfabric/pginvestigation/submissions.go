package pginvestigation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The submission store (CHAOS-11912, plan §2.7.5) over
// acr.context_fabric_investigation_submissions (migration 0055). The
// completion a Save makes is in Save's own statement (store.go); every method
// here takes the caller's clock, so the expiry admission wrote and the
// reconcile that reads it use one time source.

var (
	_ contextfabric.SubmissionStore         = (*Store)(nil)
	_ contextfabric.SubmissionUnsavedMarker = (*Store)(nil)
	_ contextfabric.UsageReader             = (*Store)(nil)
)

const submissionColumns = `org_id, owner_auth_method, owner_subject, submission_id, request_digest, reserved_result_id, state, result_id, thread_id, admitted_at, updated_at, expires_at, owner_user_id, owner_surface, question, thread_owner_auth_method, thread_owner_subject, lease_holder, lease_expires_at, run_request_id, cancel_requested_at`

// storedResultExists is the reconcile rule's condition: the record's reserved
// result is stored for its organization AND bound to the record's own owner
// (round-2 F1: a done record never names a result its owner does not own; a
// row saved under the reserved id by anyone else, or unbound, completes
// nothing). s is the submissions row alias.
const storedResultExists = `EXISTS (SELECT 1 FROM acr.context_fabric_investigation_results r WHERE r.result_id = s.reserved_result_id AND r.org_id = s.org_id AND r.submission_owner_auth_method = s.owner_auth_method AND r.submission_owner_subject = s.owner_subject)`

// reconcileSetSQL is the reconcile rule as one UPDATE's SET and WHERE, over
// the alias s and the clock $now: a running or abandoned record whose
// reserved result is stored becomes done with that id; a running record at
// or past its expiry, with no stored result, becomes abandoned. A stored
// result always wins over the clock.
func reconcileSetSQL(now string) string {
	return `
   SET state = CASE WHEN ` + storedResultExists + ` THEN 'done' ELSE 'abandoned' END,
       result_id = CASE WHEN ` + storedResultExists + ` THEN s.reserved_result_id ELSE NULL END,
       updated_at = ` + now + `
 WHERE ((s.state IN ('running', 'abandoned') AND ` + storedResultExists + `)
     OR (s.state = 'running' AND s.expires_at <= ` + now + ` AND NOT ` + storedResultExists + `))`
}

func validSubmissionKey(key contextfabric.SubmissionKey) error {
	if strings.TrimSpace(key.OrgID) == "" || key.Owner.Subject == "" || key.Owner.AuthenticationMethod == "" {
		return errors.New("pginvestigation: submission organization and owner are required")
	}
	if !contextfabric.ValidSubmissionID(key.SubmissionID) {
		return errors.New("pginvestigation: submission id is malformed")
	}
	return nil
}

type submissionScanner interface {
	Scan(dest ...any) error
}

func scanSubmission(row submissionScanner) (contextfabric.SubmissionRecord, error) {
	var record contextfabric.SubmissionRecord
	var state string
	var resultID, threadID, ownerUser, ownerSurface, question, threadOwnerMethod, threadOwnerSubject, leaseHolder, runRequestID sql.NullString
	var leaseExpiresAt, cancelRequestedAt sql.NullTime
	if err := row.Scan(&record.Key.OrgID, &record.Key.Owner.AuthenticationMethod, &record.Key.Owner.Subject, &record.Key.SubmissionID,
		&record.RequestDigest, &record.ReservedResultID, &state, &resultID, &threadID, &record.AdmittedAt, &record.UpdatedAt, &record.ExpiresAt,
		&ownerUser, &ownerSurface, &question, &threadOwnerMethod, &threadOwnerSubject,
		&leaseHolder, &leaseExpiresAt, &runRequestID, &cancelRequestedAt); err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	// CHAOS-11949: the run lease, when the record was admitted under one.
	record.LeaseHolder, record.RunRequestID = leaseHolder.String, runRequestID.String
	if leaseExpiresAt.Valid {
		record.LeaseExpiresAt = leaseExpiresAt.Time.UTC()
	}
	if cancelRequestedAt.Valid {
		record.CancelRequestedAt = cancelRequestedAt.Time.UTC()
	}
	if ownerUser.Valid && ownerSurface.Valid {
		record.Key.Owner.UserID, record.Key.Owner.Surface = ownerUser.String, contextfabric.SubmissionOwnerSurface(ownerSurface.String)
	}
	record.State = contextfabric.SubmissionState(state)
	record.ResultID = resultID.String
	record.ThreadID = threadID.String
	record.Question = question.String
	if threadOwnerMethod.Valid && threadOwnerSubject.Valid {
		record.ThreadOwner = contextfabric.SubmissionOwner{AuthenticationMethod: threadOwnerMethod.String, Subject: threadOwnerSubject.String}
	}
	record.AdmittedAt, record.UpdatedAt, record.ExpiresAt = record.AdmittedAt.UTC(), record.UpdatedAt.UTC(), record.ExpiresAt.UTC()
	return record, nil
}

type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// resolveSubmissionKey returns the stored key of the record key names: the
// same organization and submission id, and one owner -- the same legacy
// pair, or the same canonical user on the same surface (CHAOS-12091). A
// rotated credential's request finds its user's record this way; the exact
// legacy pair wins when both kinds of row exist. Every later statement then
// names the stored record by its own legacy pair.
func resolveSubmissionKey(ctx context.Context, q rowQueryer, key contextfabric.SubmissionKey) (contextfabric.SubmissionKey, bool, error) {
	stored := contextfabric.SubmissionKey{OrgID: key.OrgID, SubmissionID: key.SubmissionID}
	var user, surface sql.NullString
	err := q.QueryRowContext(ctx, `
SELECT owner_auth_method, owner_subject, owner_user_id, owner_surface
  FROM acr.context_fabric_investigation_submissions
 WHERE org_id = $1 AND submission_id = $2
   AND `+ownerMatchSQL("owner_auth_method", "owner_subject", "owner_user_id", "owner_surface", "$3", "$4", "$5", "$6")+`
 ORDER BY (owner_auth_method = $3 AND owner_subject = $4) DESC, admitted_at
 LIMIT 1`,
		key.OrgID, key.SubmissionID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.Owner.UserID, string(key.Owner.Surface)).
		Scan(&stored.Owner.AuthenticationMethod, &stored.Owner.Subject, &user, &surface)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return key, false, nil
	case err != nil:
		return contextfabric.SubmissionKey{}, false, fmt.Errorf("resolve investigation submission: %w", sanitizeError(err))
	}
	if user.Valid && surface.Valid {
		stored.Owner.UserID, stored.Owner.Surface = user.String, contextfabric.SubmissionOwnerSurface(surface.String)
	}
	return stored, true, nil
}

func (s *Store) configured() error {
	if s == nil || s.db == nil {
		return errors.New("pginvestigation: store is not configured")
	}
	return nil
}

// submissionAdmissionLockClass is the advisory lock class that serializes
// admissions of one organization (CHAOS-11914). It is not the projection
// lock (20260812) or the organization write fence (20261014).
const submissionAdmissionLockClass = 20261029

// AdmitSubmission implements contextfabric.SubmissionStore. In ONE
// transaction, serialized per organization by an advisory lock on every
// replica: the INSERT comes first and the primary key decides a repeated
// key (a repeat writes nothing and is never refused by a limit); a new
// record is counted against the shared concurrency limits, a platform run
// increments the month's aggregate under the allowance, and any refusal
// rolls all of it back.
func (s *Store) AdmitSubmission(ctx context.Context, admission contextfabric.SubmissionAdmission, now time.Time) (contextfabric.SubmissionRecord, bool, error) {
	if err := s.configured(); err != nil {
		return contextfabric.SubmissionRecord{}, false, err
	}
	if err := validSubmissionKey(admission.Key); err != nil {
		return contextfabric.SubmissionRecord{}, false, err
	}
	if admission.RequestDigest == "" || admission.ReservedResultID == "" || !admission.ExpiresAt.After(now) {
		return contextfabric.SubmissionRecord{}, false, errors.New("pginvestigation: submission digest, reserved result id and a future expiry are required")
	}
	if admission.ThreadID != "" && !contextfabric.ValidSubmissionID(admission.ThreadID) {
		return contextfabric.SubmissionRecord{}, false, errors.New("pginvestigation: thread id is malformed")
	}
	if admission.ThreadRetention != nil && !admission.ThreadRetention.Valid() {
		return contextfabric.SubmissionRecord{}, false, errors.New("pginvestigation: thread retention is outside the closed set")
	}
	// CHAOS-11949: the run lease a new record is admitted under.
	var leaseHolder, runRequestID sql.NullString
	var leaseExpiresAt sql.NullTime
	if admission.Lease != nil {
		if !admission.Lease.Valid() || !admission.Lease.ExpiresAt.After(now) {
			return contextfabric.SubmissionRecord{}, false, errors.New("pginvestigation: run lease holder, request id and a future lease expiry are required")
		}
		leaseHolder = sql.NullString{String: admission.Lease.Holder, Valid: true}
		runRequestID = sql.NullString{String: admission.Lease.RequestID, Valid: true}
		leaseExpiresAt = sql.NullTime{Time: admission.Lease.ExpiresAt.UTC(), Valid: true}
	}
	threadID := sql.NullString{String: admission.ThreadID, Valid: admission.ThreadID != ""}
	// The question is stored with a thread turn only (CHAOS-11948).
	question := sql.NullString{String: admission.Question, Valid: admission.ThreadID != "" && admission.Question != ""}
	key := admission.Key
	limits := admission.Limits.Effective()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: begin: %w", sanitizeError(err))
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, submissionAdmissionLockClass, key.OrgID); err != nil {
		return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: lock: %w", sanitizeError(err))
	}
	// CHAOS-12091: a record of the same owner under another legacy pair (the
	// user's earlier, rotated credential) is this key's record. Under the
	// lock no replica admits the key in between.
	if _, exists, err := resolveSubmissionKey(ctx, tx, key); err != nil {
		return contextfabric.SubmissionRecord{}, false, err
	} else if exists {
		if err := tx.Commit(); err != nil {
			return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: commit: %w", sanitizeError(err))
		}
		existing, err := s.GetSubmission(ctx, key, now)
		if err != nil {
			return contextfabric.SubmissionRecord{}, false, err
		}
		return existing, false, nil
	}
	// CHAOS-11948: a new turn of a closed thread is refused before anything is
	// written and before any limit is counted. The turn joins the row the
	// caller reaches (a rotated credential of the same user on the same
	// surface reaches the user's row, CHAOS-12091); a new thread is keyed by
	// the caller's own owner. The record links the row by its legacy pair.
	var thread *contextfabric.ThreadRecord
	threadKey := contextfabric.ThreadKey{OrgID: key.OrgID, Owner: key.Owner, ThreadID: admission.ThreadID}
	var threadOwnerMethod, threadOwnerSubject sql.NullString
	if admission.ThreadID != "" {
		current, found, err := resolveThread(ctx, tx, key.OrgID, key.Owner, admission.ThreadID, true)
		if err != nil {
			return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: read thread: %w", err)
		}
		if found {
			if reason := current.ClosedReason(now); reason != "" {
				return contextfabric.SubmissionRecord{}, false, &contextfabric.ThreadClosedError{Reason: reason}
			}
			thread, threadKey = &current, current.Key
		}
		threadOwnerMethod = sql.NullString{String: threadKey.Owner.AuthenticationMethod, Valid: true}
		threadOwnerSubject = sql.NullString{String: threadKey.Owner.Subject, Valid: true}
	}
	row := tx.QueryRowContext(ctx, `
INSERT INTO acr.context_fabric_investigation_submissions
    (org_id, owner_auth_method, owner_subject, submission_id, request_digest, reserved_result_id, state, thread_id, admitted_at, updated_at, expires_at, owner_user_id, owner_surface, question, thread_owner_auth_method, thread_owner_subject, lease_holder, lease_expires_at, run_request_id)
VALUES ($1, $2, $3, $4, $5, $6, 'running', $9, $7, $7, $8, NULLIF($10, ''), NULLIF($11, ''), $12, $13, $14, $15, $16, $17)
ON CONFLICT (org_id, owner_auth_method, owner_subject, submission_id) DO NOTHING
RETURNING `+submissionColumns,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.SubmissionID,
		admission.RequestDigest, admission.ReservedResultID, now.UTC(), admission.ExpiresAt.UTC(), threadID,
		key.Owner.UserID, string(key.Owner.Surface), question, threadOwnerMethod, threadOwnerSubject,
		leaseHolder, leaseExpiresAt, runRequestID)
	record, err := scanSubmission(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A repeat of the key: nothing was written, no limit applies.
		if err := tx.Commit(); err != nil {
			return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: commit: %w", sanitizeError(err))
		}
		existing, err := s.GetSubmission(ctx, key, now)
		if err != nil {
			return contextfabric.SubmissionRecord{}, false, err
		}
		return existing, false, nil
	case err != nil:
		return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: %w", sanitizeError(err))
	}
	// The live runs, without the one just inserted: running, inside its
	// budget, its reserved result not stored.
	var principalRunning, orgRunning int64
	if err := tx.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE `+ownerMatchSQL("s.owner_auth_method", "s.owner_subject", "s.owner_user_id", "s.owner_surface", "$2", "$3", "$6", "$7")+`), count(*)
  FROM acr.context_fabric_investigation_submissions s
 WHERE s.org_id = $1 AND s.state = 'running' AND s.expires_at > $5 AND s.reserved_result_id <> $4
   AND NOT `+storedResultExists,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, admission.ReservedResultID, now.UTC(),
		key.Owner.UserID, string(key.Owner.Surface)).Scan(&principalRunning, &orgRunning); err != nil {
		return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: count running: %w", sanitizeError(err))
	}
	switch {
	case principalRunning >= int64(limits.MaxRunningPerPrincipal):
		return contextfabric.SubmissionRecord{}, false, &contextfabric.SubmissionRefusedError{Refusal: contextfabric.SubmissionRefusedPrincipalConcurrency, Count: principalRunning, Limit: int64(limits.MaxRunningPerPrincipal), RetryAfter: contextfabric.SubmissionConcurrencyRetryAfter}
	case orgRunning >= int64(limits.MaxRunningPerOrg):
		return contextfabric.SubmissionRecord{}, false, &contextfabric.SubmissionRefusedError{Refusal: contextfabric.SubmissionRefusedOrgConcurrency, Count: orgRunning, Limit: int64(limits.MaxRunningPerOrg), RetryAfter: contextfabric.SubmissionConcurrencyRetryAfter}
	}
	if admission.PlatformRun {
		month := contextfabric.UsageMonth(now)
		var runs int64
		err := tx.QueryRowContext(ctx, `
INSERT INTO acr.context_fabric_usage_monthly AS u (org_id, month, platform_runs, updated_at)
VALUES ($1, $2, 1, $3)
ON CONFLICT (org_id, month) DO UPDATE SET platform_runs = u.platform_runs + 1, updated_at = $3
 WHERE u.platform_runs < $4
RETURNING u.platform_runs`, key.OrgID, month, now.UTC(), limits.MonthlyPlatformRunAllowance).Scan(&runs)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			var current int64
			if err := tx.QueryRowContext(ctx, `SELECT platform_runs FROM acr.context_fabric_usage_monthly WHERE org_id = $1 AND month = $2`, key.OrgID, month).Scan(&current); err != nil {
				return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: read usage: %w", sanitizeError(err))
			}
			return contextfabric.SubmissionRecord{}, false, &contextfabric.SubmissionRefusedError{Refusal: contextfabric.SubmissionRefusedAllowanceExhausted, Count: current, Limit: limits.MonthlyPlatformRunAllowance, RetryAfter: contextfabric.AllowanceRetryAfter(now)}
		case err != nil:
			return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: count usage: %w", sanitizeError(err))
		}
	}
	if admission.ThreadID != "" {
		next := contextfabric.AdvanceThread(thread, threadKey,
			contextfabric.ThreadTurnCursor{AdmittedAt: record.AdmittedAt, SubmissionID: key.SubmissionID}, admission.ThreadRetention)
		if err := writeThread(ctx, tx, next); err != nil {
			return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: write thread: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: commit: %w", sanitizeError(err))
	}
	return record, true, nil
}

// ReadUsageMonthly implements contextfabric.UsageReader.
func (s *Store) ReadUsageMonthly(ctx context.Context, orgID string, month time.Time) (contextfabric.UsageMonthly, bool, error) {
	if err := s.configured(); err != nil {
		return contextfabric.UsageMonthly{}, false, err
	}
	usage := contextfabric.UsageMonthly{OrgID: orgID, Month: contextfabric.UsageMonth(month)}
	var cost sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT platform_runs, cost_micro_usd FROM acr.context_fabric_usage_monthly WHERE org_id = $1 AND month = $2`, orgID, usage.Month).Scan(&usage.PlatformRuns, &cost)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.UsageMonthly{}, false, nil
	case err != nil:
		return contextfabric.UsageMonthly{}, false, fmt.Errorf("read usage aggregate: %w", sanitizeError(err))
	}
	if cost.Valid {
		usage.CostMicroUSD = &cost.Int64
	}
	return usage, true, nil
}

// GetSubmission implements contextfabric.SubmissionStore: the reconcile
// update for this key, then the read.
func (s *Store) GetSubmission(ctx context.Context, key contextfabric.SubmissionKey, now time.Time) (contextfabric.SubmissionRecord, error) {
	if err := s.configured(); err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	if err := validSubmissionKey(key); err != nil {
		return contextfabric.SubmissionRecord{}, contextfabric.ErrSubmissionNotFound
	}
	key, found, err := resolveSubmissionKey(ctx, s.db, key)
	if err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	if !found {
		return contextfabric.SubmissionRecord{}, contextfabric.ErrSubmissionNotFound
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions s`+reconcileSetSQL("$5")+`
   AND s.org_id = $1 AND s.owner_auth_method = $2 AND s.owner_subject = $3 AND s.submission_id = $4`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.SubmissionID, now.UTC()); err != nil {
		return contextfabric.SubmissionRecord{}, fmt.Errorf("reconcile investigation submission: %w", sanitizeError(err))
	}
	record, err := scanSubmission(s.db.QueryRowContext(ctx, `
SELECT `+submissionColumns+` FROM acr.context_fabric_investigation_submissions
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND submission_id = $4`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.SubmissionID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.SubmissionRecord{}, contextfabric.ErrSubmissionNotFound
	case err != nil:
		return contextfabric.SubmissionRecord{}, fmt.Errorf("read investigation submission: %w", sanitizeError(err))
	}
	if record.ThreadClosed, err = s.recordThreadClosed(ctx, record, now); err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	return record, nil
}

// FinishSubmission implements contextfabric.SubmissionStore: one conditional
// update on a running record, then the read.
func (s *Store) FinishSubmission(ctx context.Context, key contextfabric.SubmissionKey, servedResultID string, now time.Time) (contextfabric.SubmissionRecord, contextfabric.SubmissionCompletion, error) {
	if err := s.configured(); err != nil {
		return contextfabric.SubmissionRecord{}, "", err
	}
	if err := validSubmissionKey(key); err != nil {
		return contextfabric.SubmissionRecord{}, "", err
	}
	key, found, resolveErr := resolveSubmissionKey(ctx, s.db, key)
	if resolveErr != nil {
		return contextfabric.SubmissionRecord{}, "", resolveErr
	}
	if !found {
		return contextfabric.SubmissionRecord{}, "", contextfabric.ErrSubmissionNotFound
	}
	var stored sql.NullBool
	err := s.db.QueryRowContext(ctx, `
WITH served AS (
    SELECT EXISTS (SELECT 1 FROM acr.context_fabric_investigation_results
                    WHERE result_id = $5 AND org_id = $1 AND $5 <> ''
                      AND submission_owner_auth_method = $2 AND submission_owner_subject = $3) AS stored
)
UPDATE acr.context_fabric_investigation_submissions s
   SET state = CASE WHEN (SELECT stored FROM served) THEN 'done' ELSE 'done_unsaved' END,
       result_id = CASE WHEN (SELECT stored FROM served) THEN $5 ELSE NULL END,
       updated_at = $6
 WHERE s.org_id = $1 AND s.owner_auth_method = $2 AND s.owner_subject = $3 AND s.submission_id = $4
   AND s.state = 'running'
RETURNING (SELECT stored FROM served)`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.SubmissionID, servedResultID, now.UTC()).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return contextfabric.SubmissionRecord{}, "", fmt.Errorf("finish investigation submission: %w", sanitizeError(err))
	}
	record, readErr := s.readSubmission(ctx, key)
	if readErr != nil {
		return contextfabric.SubmissionRecord{}, "", readErr
	}
	switch {
	case err == nil && stored.Bool:
		return record, contextfabric.SubmissionCompletionServedStored, nil
	case err == nil:
		return record, contextfabric.SubmissionCompletionServedUnsaved, nil
	case record.State == contextfabric.SubmissionAbandoned:
		return record, contextfabric.SubmissionCompletionNotRunning, nil
	default:
		return record, contextfabric.SubmissionCompletionAlreadyFinal, nil
	}
}

// AbandonSubmission implements contextfabric.SubmissionStore.
func (s *Store) AbandonSubmission(ctx context.Context, key contextfabric.SubmissionKey, now time.Time) (contextfabric.SubmissionRecord, error) {
	if err := s.configured(); err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	if err := validSubmissionKey(key); err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	key, found, err := resolveSubmissionKey(ctx, s.db, key)
	if err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	if !found {
		return contextfabric.SubmissionRecord{}, contextfabric.ErrSubmissionNotFound
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions
   SET state = 'abandoned', updated_at = $5
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND submission_id = $4
   AND state = 'running'`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.SubmissionID, now.UTC()); err != nil {
		return contextfabric.SubmissionRecord{}, fmt.Errorf("abandon investigation submission: %w", sanitizeError(err))
	}
	return s.readSubmission(ctx, key)
}

func (s *Store) readSubmission(ctx context.Context, key contextfabric.SubmissionKey) (contextfabric.SubmissionRecord, error) {
	record, err := scanSubmission(s.db.QueryRowContext(ctx, `
SELECT `+submissionColumns+` FROM acr.context_fabric_investigation_submissions
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND submission_id = $4`,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.SubmissionID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.SubmissionRecord{}, contextfabric.ErrSubmissionNotFound
	case err != nil:
		return contextfabric.SubmissionRecord{}, fmt.Errorf("read investigation submission: %w", sanitizeError(err))
	}
	return record, nil
}

// SweepSubmissions implements contextfabric.SubmissionStore: the reconcile
// rule over every record it applies to, in one statement.
func (s *Store) SweepSubmissions(ctx context.Context, now time.Time) (contextfabric.SubmissionSweep, error) {
	if err := s.configured(); err != nil {
		return contextfabric.SubmissionSweep{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions s`+reconcileSetSQL("$1")+`
RETURNING s.state`, now.UTC())
	if err != nil {
		return contextfabric.SubmissionSweep{}, fmt.Errorf("sweep investigation submissions: %w", sanitizeError(err))
	}
	defer rows.Close()
	var sweep contextfabric.SubmissionSweep
	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			return contextfabric.SubmissionSweep{}, fmt.Errorf("sweep investigation submissions: %w", sanitizeError(err))
		}
		if contextfabric.SubmissionState(state) == contextfabric.SubmissionDone {
			sweep.Completed++
		} else {
			sweep.Abandoned++
		}
	}
	if err := rows.Err(); err != nil {
		return contextfabric.SubmissionSweep{}, fmt.Errorf("sweep investigation submissions: %w", sanitizeError(err))
	}
	return sweep, nil
}

// MarkSubmissionUnsaved implements contextfabric.SubmissionUnsavedMarker.
func (s *Store) MarkSubmissionUnsaved(ctx context.Context, principal storage.Principal, resultID string, now time.Time) (bool, error) {
	if err := s.configured(); err != nil {
		return false, err
	}
	owner, err := contextfabric.SubmissionOwnerOf(principal)
	if err != nil {
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions
   SET state = 'done_unsaved', updated_at = $5
 WHERE org_id = $1 AND reserved_result_id = $2
   AND `+ownerMatchSQL("owner_auth_method", "owner_subject", "owner_user_id", "owner_surface", "$3", "$4", "$6", "$7")+`
   AND state = 'running'`,
		strings.TrimSpace(principal.OrgID), resultID, owner.AuthenticationMethod, owner.Subject, now.UTC(), owner.UserID, string(owner.Surface))
	if err != nil {
		return false, fmt.Errorf("mark investigation submission unsaved: %w", sanitizeError(err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark investigation submission unsaved rows affected: %w", sanitizeError(err))
	}
	return affected == 1, nil
}
