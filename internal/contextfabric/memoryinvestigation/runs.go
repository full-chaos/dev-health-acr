package memoryinvestigation

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The in-memory twin of pginvestigation's run store (CHAOS-11949). The mutex
// is the transaction and the row lock: a renewal and a takeover are decided
// under it, so exactly one of them applies.

var _ contextfabric.RunStore = (*Store)(nil)

// recordOfRunLocked finds the record whose reserved result id is run's.
// Callers hold mu.
func (s *Store) recordOfRunLocked(run contextfabric.RunKey) (contextfabric.SubmissionKey, contextfabric.SubmissionRecord, bool) {
	key, found := s.reserved[reservedResultKey{orgID: run.OrgID, resultID: run.ReservedResultID}]
	if !found {
		return contextfabric.SubmissionKey{}, contextfabric.SubmissionRecord{}, false
	}
	record, found := s.submissions[key]
	return key, record, found
}

// CheckRunLease implements contextfabric.RunStore.
func (s *Store) CheckRunLease(ctx context.Context, run contextfabric.RunKey, holder string, renewUntil time.Time, now time.Time) (contextfabric.RunLeaseCheck, error) {
	if err := ctx.Err(); err != nil {
		return contextfabric.RunLeaseCheck{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, record, found := s.recordOfRunLocked(run)
	if !found {
		return contextfabric.RunLeaseCheck{}, nil
	}
	check := contextfabric.RunLeaseCheck{Found: true, State: record.State, Holder: record.LeaseHolder, CancelRequestedAt: record.CancelRequestedAt}
	if check.Held(holder) && !renewUntil.IsZero() {
		if renewUntil.After(record.LeaseExpiresAt) {
			record.LeaseExpiresAt = renewUntil.UTC()
			s.submissions[key] = record
		}
		check.Renewed = true
	}
	return check, nil
}

// RequestRunCancel implements contextfabric.RunStore.
func (s *Store) RequestRunCancel(ctx context.Context, key contextfabric.SubmissionKey, now time.Time) (contextfabric.SubmissionRecord, error) {
	record, err := s.GetSubmission(ctx, key, now)
	if err != nil || record.ThreadClosed != "" || record.State != contextfabric.SubmissionRunning || !record.Leased() || !record.CancelRequestedAt.IsZero() {
		return record, err
	}
	s.mu.Lock()
	if stored, current, found := s.findSubmissionLocked(key); found &&
		current.State == contextfabric.SubmissionRunning && current.Leased() && current.CancelRequestedAt.IsZero() {
		current.CancelRequestedAt = now.UTC()
		s.submissions[stored] = current
	}
	s.mu.Unlock()
	return s.GetSubmission(ctx, key, now)
}

func hasTerminal(rows []contextfabric.RunEvent) bool {
	for _, row := range rows {
		if row.Terminal != nil {
			return true
		}
	}
	return false
}

// AppendRunEvent implements contextfabric.RunStore.
func (s *Store) AppendRunEvent(ctx context.Context, run contextfabric.RunKey, event contextfabric.RunEvent, maxEvents int, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if event.Seq < 1 {
		return false, contextfabric.ErrRunEventSeq
	}
	if event.Terminal != nil {
		switch event.Terminal.Kind {
		case contextfabric.RunTerminalResult:
			if event.Terminal.ResultID == "" {
				return false, errors.New("memoryinvestigation: a result terminal names its result id")
			}
		case contextfabric.RunTerminalUnsaved:
		case contextfabric.RunTerminalError:
			if event.Terminal.Error == nil {
				return false, errors.New("memoryinvestigation: an error terminal carries its envelope")
			}
		default:
			return false, errors.New("memoryinvestigation: run terminal kind is outside the closed set")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, found := s.recordOfRunLocked(run); !found {
		return false, errors.New("memoryinvestigation: the run has no submission record")
	}
	rows := s.runEvents[run]
	for _, row := range rows {
		if row.Seq == event.Seq {
			if event.Terminal != nil && row.Terminal != nil {
				return false, nil
			}
			return false, contextfabric.ErrRunEventSeq
		}
	}
	if hasTerminal(rows) {
		if event.Terminal != nil {
			return false, nil
		}
		return false, contextfabric.ErrRunEventSeq
	}
	if maxEvents < 1 {
		maxEvents = contextfabric.RunEventLogMaxEvents
	}
	event.WrittenAt = now.UTC()
	event.Data = append([]byte(nil), event.Data...)
	rows = append(rows, event)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })
	if event.Terminal == nil {
		kept := rows[:0]
		for _, row := range rows {
			if row.Terminal != nil || row.Seq > event.Seq-int64(maxEvents) {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	s.runEvents[run] = rows
	return true, nil
}

// ReadRunEvents implements contextfabric.RunStore.
func (s *Store) ReadRunEvents(ctx context.Context, run contextfabric.RunKey, afterSeq int64, limit int) (contextfabric.RunEventPage, error) {
	if err := ctx.Err(); err != nil {
		return contextfabric.RunEventPage{}, err
	}
	if limit < 1 {
		limit = contextfabric.RunEventLogMaxEvents
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, record, found := s.recordOfRunLocked(run)
	if !found {
		return contextfabric.RunEventPage{}, contextfabric.ErrSubmissionNotFound
	}
	page := contextfabric.RunEventPage{Record: record}
	rows := s.runEvents[run]
	if len(rows) > 0 {
		page.FirstSeq, page.LastSeq = rows[0].Seq, rows[len(rows)-1].Seq
		page.LastStageStarted = rows[len(rows)-1].LastStageStarted
	}
	for _, row := range rows {
		if row.Terminal != nil {
			page.TerminalSeq = row.Seq
		}
		if row.Seq > afterSeq && len(page.Events) < limit {
			copied := row
			copied.Data = append([]byte(nil), row.Data...)
			page.Events = append(page.Events, copied)
		}
	}
	return page, nil
}

// RunTerminalOf implements contextfabric.RunStore.
func (s *Store) RunTerminalOf(ctx context.Context, run contextfabric.RunKey) (*contextfabric.RunEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.runEvents[run] {
		if row.Terminal != nil {
			copied := row
			return &copied, nil
		}
	}
	return nil, nil
}

// ReapLostRuns implements contextfabric.RunStore.
func (s *Store) ReapLostRuns(ctx context.Context, taker string, only *contextfabric.RunKey, lostTerminal func(requestID string) contractsv1.ErrorEnvelope, now time.Time) ([]contextfabric.LostRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if taker == "" || lostTerminal == nil {
		return nil, errors.New("memoryinvestigation: a takeover needs its taker and the lost terminal")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var lost []contextfabric.LostRun
	for key, record := range s.submissions {
		run := contextfabric.RunKeyOf(record)
		if only != nil && *only != run {
			continue
		}
		if record.State != contextfabric.SubmissionRunning || !record.Leased() || now.Before(record.LeaseExpiresAt) || s.ownedByLocked(record.ReservedResultID, key) {
			continue
		}
		previous, expired := record.LeaseHolder, record.LeaseExpiresAt
		record.State, record.LeaseHolder, record.UpdatedAt = contextfabric.SubmissionAbandoned, taker, now.UTC()
		s.submissions[key] = record
		rows := s.runEvents[run]
		next, lastStage := int64(1), contractsv1.ContextFabricInvestigationStage("")
		if len(rows) > 0 {
			next, lastStage = rows[len(rows)-1].Seq+1, rows[len(rows)-1].LastStageStarted
		}
		appended := false
		if !hasTerminal(rows) {
			envelope := lostTerminal(record.RunRequestID)
			s.runEvents[run] = append(rows, contextfabric.RunEvent{
				Seq: next, Type: contractsv1.ContextFabricInvestigationEventError, LastStageStarted: lastStage,
				Terminal: &contextfabric.RunTerminal{Kind: contextfabric.RunTerminalError, Error: &envelope}, WrittenAt: now.UTC(),
			})
			appended = true
		}
		lost = append(lost, contextfabric.LostRun{Run: run, RequestID: record.RunRequestID, TerminalSeq: next, PreviousHolder: previous, LeaseExpiredAt: expired, TerminalAppended: appended})
	}
	return lost, nil
}
