package pginvestigation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/jackc/pgx/v5/pgconn"
)

// The run lease and its event log (CHAOS-11949, plan §2.7.9) over the lease
// columns of acr.context_fabric_investigation_submissions and
// acr.context_fabric_investigation_run_events (migration 0064). The row lock
// of a statement decides every race between replicas: a renewal by the
// holder and a takeover by another replica are both conditional UPDATEs of
// one row, so exactly one of them applies.

var _ contextfabric.RunStore = (*Store)(nil)

// reapBatch bounds one takeover pass.
const reapBatch = 100

func validRunKey(run contextfabric.RunKey) error {
	if strings.TrimSpace(run.OrgID) == "" || run.ReservedResultID == "" {
		return errors.New("pginvestigation: run organization and reserved result id are required")
	}
	return nil
}

// CheckRunLease implements contextfabric.RunStore.
func (s *Store) CheckRunLease(ctx context.Context, run contextfabric.RunKey, holder string, renewUntil time.Time, now time.Time) (contextfabric.RunLeaseCheck, error) {
	if err := s.configured(); err != nil {
		return contextfabric.RunLeaseCheck{}, err
	}
	if err := validRunKey(run); err != nil {
		return contextfabric.RunLeaseCheck{}, err
	}
	var check contextfabric.RunLeaseCheck
	if !renewUntil.IsZero() && holder != "" {
		// Only the holder of a running record renews; a lease never moves
		// back.
		result, err := s.db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions
   SET lease_expires_at = GREATEST(lease_expires_at, $4)
 WHERE org_id = $1 AND reserved_result_id = $2 AND lease_holder = $3 AND state = 'running'`,
			run.OrgID, run.ReservedResultID, holder, renewUntil.UTC())
		if err != nil {
			return contextfabric.RunLeaseCheck{}, fmt.Errorf("renew investigation run lease: %w", sanitizeError(err))
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return contextfabric.RunLeaseCheck{}, fmt.Errorf("renew investigation run lease rows affected: %w", sanitizeError(err))
		}
		check.Renewed = affected == 1
	}
	var state string
	var leaseHolder sql.NullString
	var cancel sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT state, lease_holder, cancel_requested_at
  FROM acr.context_fabric_investigation_submissions
 WHERE org_id = $1 AND reserved_result_id = $2`, run.OrgID, run.ReservedResultID).Scan(&state, &leaseHolder, &cancel)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.RunLeaseCheck{}, nil
	case err != nil:
		return contextfabric.RunLeaseCheck{}, fmt.Errorf("check investigation run lease: %w", sanitizeError(err))
	}
	check.Found, check.State, check.Holder = true, contextfabric.SubmissionState(state), leaseHolder.String
	if cancel.Valid {
		check.CancelRequestedAt = cancel.Time.UTC()
	}
	return check, nil
}

// RequestRunCancel implements contextfabric.RunStore. A closed thread's turn
// is answered from the read before anything is written: closed is unknown
// (design §6 T6).
func (s *Store) RequestRunCancel(ctx context.Context, key contextfabric.SubmissionKey, now time.Time) (contextfabric.SubmissionRecord, error) {
	record, err := s.GetSubmission(ctx, key, now)
	if err != nil || record.ThreadClosed != "" || record.State != contextfabric.SubmissionRunning || !record.Leased() || !record.CancelRequestedAt.IsZero() {
		return record, err
	}
	stored := record.Key
	if _, err := s.db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions
   SET cancel_requested_at = $5
 WHERE org_id = $1 AND owner_auth_method = $2 AND owner_subject = $3 AND submission_id = $4
   AND state = 'running' AND lease_holder IS NOT NULL AND cancel_requested_at IS NULL`,
		stored.OrgID, stored.Owner.AuthenticationMethod, stored.Owner.Subject, stored.SubmissionID, now.UTC()); err != nil {
		return contextfabric.SubmissionRecord{}, fmt.Errorf("request investigation run cancel: %w", sanitizeError(err))
	}
	return s.GetSubmission(ctx, key, now)
}

// AppendRunEvent implements contextfabric.RunStore.
func (s *Store) AppendRunEvent(ctx context.Context, run contextfabric.RunKey, event contextfabric.RunEvent, maxEvents int, now time.Time) (bool, error) {
	if err := s.configured(); err != nil {
		return false, err
	}
	if err := validRunKey(run); err != nil {
		return false, err
	}
	if event.Seq < 1 {
		return false, contextfabric.ErrRunEventSeq
	}
	if event.Terminal != nil {
		kind, resultID, envelope, err := terminalColumns(*event.Terminal)
		if err != nil {
			return false, err
		}
		result, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_run_events
    (org_id, reserved_result_id, seq, event_type, last_stage_started, terminal_kind, terminal_result_id, terminal_error, written_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (org_id, reserved_result_id) WHERE terminal_kind IS NOT NULL DO NOTHING`,
			run.OrgID, run.ReservedResultID, event.Seq, string(event.Type), string(event.LastStageStarted), kind, resultID, envelope, now.UTC())
		if err != nil {
			if uniqueViolation(err) {
				return false, contextfabric.ErrRunEventSeq
			}
			return false, fmt.Errorf("append investigation run terminal: %w", sanitizeError(err))
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("append investigation run terminal rows affected: %w", sanitizeError(err))
		}
		return affected == 1, nil
	}
	if maxEvents < 1 {
		maxEvents = contextfabric.RunEventLogMaxEvents
	}
	// A run that already has its terminal takes no further event: a stale
	// holder cannot write after the replica that took its lease ended it.
	var inserted int64
	err := s.db.QueryRowContext(ctx, `
WITH inserted AS (
    INSERT INTO acr.context_fabric_investigation_run_events
        (org_id, reserved_result_id, seq, event_type, last_stage_started, data, written_at)
    SELECT $1, $2, $3, $4, $5, $6, $7
     WHERE NOT EXISTS (SELECT 1 FROM acr.context_fabric_investigation_run_events
                        WHERE org_id = $1 AND reserved_result_id = $2 AND terminal_kind IS NOT NULL)
    RETURNING seq
), pruned AS (
    DELETE FROM acr.context_fabric_investigation_run_events
     WHERE org_id = $1 AND reserved_result_id = $2 AND terminal_kind IS NULL AND seq <= $3 - $8
       AND EXISTS (SELECT 1 FROM inserted)
)
SELECT count(*) FROM inserted`,
		run.OrgID, run.ReservedResultID, event.Seq, string(event.Type), string(event.LastStageStarted), string(event.Data), now.UTC(), int64(maxEvents)).Scan(&inserted)
	switch {
	case err != nil && uniqueViolation(err):
		return false, contextfabric.ErrRunEventSeq
	case err != nil:
		return false, fmt.Errorf("append investigation run event: %w", sanitizeError(err))
	case inserted == 0:
		return false, contextfabric.ErrRunEventSeq
	}
	return true, nil
}

// terminalColumns encodes a terminal marker.
func terminalColumns(terminal contextfabric.RunTerminal) (kind string, resultID, envelope sql.NullString, err error) {
	switch terminal.Kind {
	case contextfabric.RunTerminalResult:
		if terminal.ResultID == "" {
			return "", resultID, envelope, errors.New("pginvestigation: a result terminal names its result id")
		}
		return string(terminal.Kind), sql.NullString{String: terminal.ResultID, Valid: true}, envelope, nil
	case contextfabric.RunTerminalUnsaved:
		return string(terminal.Kind), resultID, envelope, nil
	case contextfabric.RunTerminalError:
		if terminal.Error == nil {
			return "", resultID, envelope, errors.New("pginvestigation: an error terminal carries its envelope")
		}
		encoded, err := json.Marshal(terminal.Error)
		if err != nil {
			return "", resultID, envelope, fmt.Errorf("pginvestigation: encode run terminal envelope: %w", err)
		}
		return string(terminal.Kind), resultID, sql.NullString{String: string(encoded), Valid: true}, nil
	}
	return "", resultID, envelope, errors.New("pginvestigation: run terminal kind is outside the closed set")
}

const runEventColumns = `seq, event_type, last_stage_started, data, terminal_kind, terminal_result_id, terminal_error, written_at`

func scanRunEvent(row submissionScanner) (contextfabric.RunEvent, error) {
	var event contextfabric.RunEvent
	var eventType, lastStage string
	var data, kind, resultID, envelope sql.NullString
	if err := row.Scan(&event.Seq, &eventType, &lastStage, &data, &kind, &resultID, &envelope, &event.WrittenAt); err != nil {
		return contextfabric.RunEvent{}, err
	}
	event.Type = contractsv1.ContextFabricInvestigationEventType(eventType)
	event.LastStageStarted = contractsv1.ContextFabricInvestigationStage(lastStage)
	event.WrittenAt = event.WrittenAt.UTC()
	if data.Valid {
		event.Data = []byte(data.String)
	}
	if kind.Valid {
		terminal := contextfabric.RunTerminal{Kind: contextfabric.RunTerminalKind(kind.String), ResultID: resultID.String}
		if envelope.Valid {
			var decoded contractsv1.ErrorEnvelope
			if err := json.Unmarshal([]byte(envelope.String), &decoded); err != nil {
				return contextfabric.RunEvent{}, fmt.Errorf("pginvestigation: decode run terminal envelope: %w", err)
			}
			terminal.Error = &decoded
		}
		event.Terminal = &terminal
	}
	return event, nil
}

// ReadRunEvents implements contextfabric.RunStore.
func (s *Store) ReadRunEvents(ctx context.Context, run contextfabric.RunKey, afterSeq int64, limit int) (contextfabric.RunEventPage, error) {
	if err := s.configured(); err != nil {
		return contextfabric.RunEventPage{}, err
	}
	if err := validRunKey(run); err != nil {
		return contextfabric.RunEventPage{}, err
	}
	if limit < 1 {
		limit = contextfabric.RunEventLogMaxEvents
	}
	var page contextfabric.RunEventPage
	var first, last, terminal sql.NullInt64
	var lastStage sql.NullString
	if err := s.db.QueryRowContext(ctx, `
SELECT min(seq), max(seq), max(seq) FILTER (WHERE terminal_kind IS NOT NULL),
       (SELECT last_stage_started FROM acr.context_fabric_investigation_run_events
         WHERE org_id = $1 AND reserved_result_id = $2 ORDER BY seq DESC LIMIT 1)
  FROM acr.context_fabric_investigation_run_events
 WHERE org_id = $1 AND reserved_result_id = $2`, run.OrgID, run.ReservedResultID).Scan(&first, &last, &terminal, &lastStage); err != nil {
		return contextfabric.RunEventPage{}, fmt.Errorf("read investigation run log state: %w", sanitizeError(err))
	}
	page.FirstSeq, page.LastSeq, page.TerminalSeq = first.Int64, last.Int64, terminal.Int64
	page.LastStageStarted = contractsv1.ContextFabricInvestigationStage(lastStage.String)
	rows, err := s.db.QueryContext(ctx, `
SELECT `+runEventColumns+` FROM acr.context_fabric_investigation_run_events
 WHERE org_id = $1 AND reserved_result_id = $2 AND seq > $3
 ORDER BY seq
 LIMIT $4`, run.OrgID, run.ReservedResultID, afterSeq, limit)
	if err != nil {
		return contextfabric.RunEventPage{}, fmt.Errorf("read investigation run events: %w", sanitizeError(err))
	}
	defer rows.Close()
	for rows.Next() {
		event, err := scanRunEvent(rows)
		if err != nil {
			return contextfabric.RunEventPage{}, fmt.Errorf("read investigation run events: %w", sanitizeError(err))
		}
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return contextfabric.RunEventPage{}, fmt.Errorf("read investigation run events: %w", sanitizeError(err))
	}
	record, err := scanSubmission(s.db.QueryRowContext(ctx, `
SELECT `+submissionColumns+` FROM acr.context_fabric_investigation_submissions
 WHERE org_id = $1 AND reserved_result_id = $2`, run.OrgID, run.ReservedResultID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.RunEventPage{}, contextfabric.ErrSubmissionNotFound
	case err != nil:
		return contextfabric.RunEventPage{}, fmt.Errorf("read investigation run record: %w", sanitizeError(err))
	}
	page.Record = record
	return page, nil
}

// RunTerminalOf implements contextfabric.RunStore.
func (s *Store) RunTerminalOf(ctx context.Context, run contextfabric.RunKey) (*contextfabric.RunEvent, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	if err := validRunKey(run); err != nil {
		return nil, err
	}
	event, err := scanRunEvent(s.db.QueryRowContext(ctx, `
SELECT `+runEventColumns+` FROM acr.context_fabric_investigation_run_events
 WHERE org_id = $1 AND reserved_result_id = $2 AND terminal_kind IS NOT NULL`, run.OrgID, run.ReservedResultID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read investigation run terminal: %w", sanitizeError(err))
	}
	return &event, nil
}

// ReapLostRuns implements contextfabric.RunStore: one transaction per lost
// record. The row is locked first and its conditions decided again under the
// lock, so a renewal that committed in between, or another replica's
// takeover, wins and this one changes nothing.
func (s *Store) ReapLostRuns(ctx context.Context, taker string, only *contextfabric.RunKey, lostTerminal func(requestID string) contractsv1.ErrorEnvelope, now time.Time) ([]contextfabric.LostRun, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	if taker == "" || lostTerminal == nil {
		return nil, errors.New("pginvestigation: a takeover needs its taker and the lost terminal")
	}
	query := `
SELECT s.org_id, s.reserved_result_id
  FROM acr.context_fabric_investigation_submissions s
 WHERE s.state = 'running' AND s.lease_expires_at IS NOT NULL AND s.lease_expires_at <= $1
   AND NOT ` + storedResultExists
	args := []any{now.UTC()}
	if only != nil {
		query += ` AND s.org_id = $2 AND s.reserved_result_id = $3`
		args = append(args, only.OrgID, only.ReservedResultID)
	}
	query += fmt.Sprintf(` ORDER BY s.lease_expires_at LIMIT %d`, reapBatch)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("find lost investigation runs: %w", sanitizeError(err))
	}
	var candidates []contextfabric.RunKey
	for rows.Next() {
		var run contextfabric.RunKey
		if err := rows.Scan(&run.OrgID, &run.ReservedResultID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("find lost investigation runs: %w", sanitizeError(err))
		}
		candidates = append(candidates, run)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("find lost investigation runs: %w", sanitizeError(err))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find lost investigation runs: %w", sanitizeError(err))
	}
	var lost []contextfabric.LostRun
	for _, run := range candidates {
		taken, ok, err := s.reapLostRun(ctx, run, taker, lostTerminal, now)
		if err != nil {
			return lost, err
		}
		if ok {
			lost = append(lost, taken)
		}
	}
	return lost, nil
}

func (s *Store) reapLostRun(ctx context.Context, run contextfabric.RunKey, taker string, lostTerminal func(requestID string) contractsv1.ErrorEnvelope, now time.Time) (contextfabric.LostRun, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextfabric.LostRun{}, false, fmt.Errorf("take over investigation run: begin: %w", sanitizeError(err))
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds
	var previous, requestID string
	var expired time.Time
	err = tx.QueryRowContext(ctx, `
UPDATE acr.context_fabric_investigation_submissions s
   SET state = 'abandoned', lease_holder = $3, updated_at = $4
  FROM (SELECT lease_holder, lease_expires_at FROM acr.context_fabric_investigation_submissions
         WHERE org_id = $1 AND reserved_result_id = $2 FOR UPDATE) AS before
 WHERE s.org_id = $1 AND s.reserved_result_id = $2
   AND s.state = 'running' AND s.lease_expires_at IS NOT NULL AND s.lease_expires_at <= $4
   AND NOT `+storedResultExists+`
RETURNING before.lease_holder, before.lease_expires_at, s.run_request_id`,
		run.OrgID, run.ReservedResultID, taker, now.UTC()).Scan(&previous, &expired, &requestID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.LostRun{}, false, nil
	case err != nil:
		return contextfabric.LostRun{}, false, fmt.Errorf("take over investigation run: %w", sanitizeError(err))
	}
	var next int64
	var lastStage sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT coalesce(max(seq), 0) + 1,
       (SELECT last_stage_started FROM acr.context_fabric_investigation_run_events
         WHERE org_id = $1 AND reserved_result_id = $2 ORDER BY seq DESC LIMIT 1)
  FROM acr.context_fabric_investigation_run_events
 WHERE org_id = $1 AND reserved_result_id = $2`, run.OrgID, run.ReservedResultID).Scan(&next, &lastStage); err != nil {
		return contextfabric.LostRun{}, false, fmt.Errorf("take over investigation run: read log: %w", sanitizeError(err))
	}
	envelope := lostTerminal(requestID)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return contextfabric.LostRun{}, false, fmt.Errorf("take over investigation run: encode terminal: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_run_events
    (org_id, reserved_result_id, seq, event_type, last_stage_started, terminal_kind, terminal_error, written_at)
VALUES ($1, $2, $3, 'error', $4, 'error', $5, $6)
ON CONFLICT (org_id, reserved_result_id) WHERE terminal_kind IS NOT NULL DO NOTHING`,
		run.OrgID, run.ReservedResultID, next, lastStage.String, string(encoded), now.UTC())
	if err != nil {
		return contextfabric.LostRun{}, false, fmt.Errorf("take over investigation run: write terminal: %w", sanitizeError(err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return contextfabric.LostRun{}, false, fmt.Errorf("take over investigation run: write terminal rows affected: %w", sanitizeError(err))
	}
	if err := tx.Commit(); err != nil {
		return contextfabric.LostRun{}, false, fmt.Errorf("take over investigation run: commit: %w", sanitizeError(err))
	}
	return contextfabric.LostRun{Run: run, RequestID: requestID, TerminalSeq: next, PreviousHolder: previous, LeaseExpiredAt: expired.UTC(), TerminalAppended: affected == 1}, true, nil
}

// uniqueViolation reports a unique-key refusal (SQLSTATE 23505): an event
// row whose seq the log already holds.
func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
