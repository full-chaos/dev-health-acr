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
)

const submissionColumns = `org_id, owner_auth_method, owner_subject, submission_id, request_digest, reserved_result_id, state, result_id, thread_id, admitted_at, updated_at, expires_at`

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
	var resultID, threadID sql.NullString
	if err := row.Scan(&record.Key.OrgID, &record.Key.Owner.AuthenticationMethod, &record.Key.Owner.Subject, &record.Key.SubmissionID,
		&record.RequestDigest, &record.ReservedResultID, &state, &resultID, &threadID, &record.AdmittedAt, &record.UpdatedAt, &record.ExpiresAt); err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	record.State = contextfabric.SubmissionState(state)
	record.ResultID = resultID.String
	record.ThreadID = threadID.String
	record.AdmittedAt, record.UpdatedAt, record.ExpiresAt = record.AdmittedAt.UTC(), record.UpdatedAt.UTC(), record.ExpiresAt.UTC()
	return record, nil
}

func (s *Store) configured() error {
	if s == nil || s.db == nil {
		return errors.New("pginvestigation: store is not configured")
	}
	return nil
}

// AdmitSubmission implements contextfabric.SubmissionStore. The INSERT is
// first and the primary key decides: of any number of concurrent admissions
// of one key, on any number of replicas, exactly one inserts; every other one
// reads the record that one wrote.
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
	threadID := sql.NullString{String: admission.ThreadID, Valid: admission.ThreadID != ""}
	key := admission.Key
	row := s.db.QueryRowContext(ctx, `
INSERT INTO acr.context_fabric_investigation_submissions
    (org_id, owner_auth_method, owner_subject, submission_id, request_digest, reserved_result_id, state, thread_id, admitted_at, updated_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, 'running', $9, $7, $7, $8)
ON CONFLICT (org_id, owner_auth_method, owner_subject, submission_id) DO NOTHING
RETURNING `+submissionColumns,
		key.OrgID, key.Owner.AuthenticationMethod, key.Owner.Subject, key.SubmissionID,
		admission.RequestDigest, admission.ReservedResultID, now.UTC(), admission.ExpiresAt.UTC(), threadID)
	record, err := scanSubmission(row)
	switch {
	case err == nil:
		return record, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return contextfabric.SubmissionRecord{}, false, fmt.Errorf("admit investigation submission: %w", sanitizeError(err))
	}
	existing, err := s.GetSubmission(ctx, key, now)
	if err != nil {
		return contextfabric.SubmissionRecord{}, false, err
	}
	return existing, false, nil
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
 WHERE org_id = $1 AND reserved_result_id = $2 AND owner_auth_method = $3 AND owner_subject = $4
   AND state = 'running'`,
		strings.TrimSpace(principal.OrgID), resultID, owner.AuthenticationMethod, owner.Subject, now.UTC())
	if err != nil {
		return false, fmt.Errorf("mark investigation submission unsaved: %w", sanitizeError(err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark investigation submission unsaved rows affected: %w", sanitizeError(err))
	}
	return affected == 1, nil
}
