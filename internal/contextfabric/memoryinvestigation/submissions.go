package memoryinvestigation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The in-memory twin of pginvestigation's submission store (CHAOS-11912).
// The mutex is the transaction: every rule below is decided under it, in the
// same order the PostgreSQL statements decide it.

var (
	_ contextfabric.SubmissionStore         = (*Store)(nil)
	_ contextfabric.SubmissionUnsavedMarker = (*Store)(nil)
)

func validSubmissionKey(key contextfabric.SubmissionKey) error {
	if strings.TrimSpace(key.OrgID) == "" || key.Owner.Subject == "" || key.Owner.AuthenticationMethod == "" {
		return errors.New("memoryinvestigation: submission organization and owner are required")
	}
	if !contextfabric.ValidSubmissionID(key.SubmissionID) {
		return errors.New("memoryinvestigation: submission id is malformed")
	}
	return nil
}

// completeSubmissionLocked is Save's completion: the record whose reserved
// result id is resultID and whose owner is principal binds the saved row to
// that owner, whatever its state, and a running or abandoned one becomes
// done. It returns the owner the saved row records, or nil. Callers hold mu.
func (s *Store) completeSubmissionLocked(principal storage.Principal, orgID, resultID string) *contextfabric.SubmissionOwner {
	key, found := s.reserved[reservedResultKey{orgID: orgID, resultID: resultID}]
	if !found {
		return nil
	}
	record := s.submissions[key]
	if !record.Key.Owner.Is(principal) {
		return nil
	}
	if record.State == contextfabric.SubmissionRunning || record.State == contextfabric.SubmissionAbandoned {
		record.State = contextfabric.SubmissionDone
		record.ResultID = resultID
		record.UpdatedAt = s.now().UTC()
		s.submissions[key] = record
	}
	owner := record.Key.Owner
	return &owner
}

// ownedByLocked: resultID is stored for key's organization AND bound to
// key's owner. A done record names only such a result (round-2 F1). Callers
// hold mu.
func (s *Store) ownedByLocked(resultID string, key contextfabric.SubmissionKey) bool {
	stored, found := s.results[resultID]
	return found && stored.orgID == key.OrgID && stored.owner != nil && *stored.owner == key.Owner
}

// reconcileLocked applies the store's reconcile rule to one record. Callers
// hold mu.
func (s *Store) reconcileLocked(record contextfabric.SubmissionRecord, now time.Time) (contextfabric.SubmissionRecord, bool) {
	if record.State != contextfabric.SubmissionRunning && record.State != contextfabric.SubmissionAbandoned {
		return record, false
	}
	if s.ownedByLocked(record.ReservedResultID, record.Key) {
		record.State, record.ResultID, record.UpdatedAt = contextfabric.SubmissionDone, record.ReservedResultID, now.UTC()
		return record, true
	}
	if record.State == contextfabric.SubmissionRunning && !now.Before(record.ExpiresAt) {
		record.State, record.UpdatedAt = contextfabric.SubmissionAbandoned, now.UTC()
		return record, true
	}
	return record, false
}

// AdmitSubmission implements contextfabric.SubmissionStore.
func (s *Store) AdmitSubmission(ctx context.Context, admission contextfabric.SubmissionAdmission, now time.Time) (contextfabric.SubmissionRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return contextfabric.SubmissionRecord{}, false, err
	}
	if err := validSubmissionKey(admission.Key); err != nil {
		return contextfabric.SubmissionRecord{}, false, err
	}
	if admission.RequestDigest == "" || admission.ReservedResultID == "" || !admission.ExpiresAt.After(now) {
		return contextfabric.SubmissionRecord{}, false, errors.New("memoryinvestigation: submission digest, reserved result id and a future expiry are required")
	}
	if admission.ThreadID != "" && !contextfabric.ValidSubmissionID(admission.ThreadID) {
		return contextfabric.SubmissionRecord{}, false, errors.New("memoryinvestigation: thread id is malformed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, found := s.submissions[admission.Key]; found {
		reconciled, changed := s.reconcileLocked(existing, now)
		if changed {
			s.submissions[admission.Key] = reconciled
		}
		return reconciled, false, nil
	}
	index := reservedResultKey{orgID: admission.Key.OrgID, resultID: admission.ReservedResultID}
	if _, taken := s.reserved[index]; taken {
		return contextfabric.SubmissionRecord{}, false, fmt.Errorf("memoryinvestigation: reserved result id is already reserved")
	}
	record := contextfabric.SubmissionRecord{
		Key: admission.Key, RequestDigest: admission.RequestDigest, ReservedResultID: admission.ReservedResultID, ThreadID: admission.ThreadID,
		State: contextfabric.SubmissionRunning, AdmittedAt: now.UTC(), UpdatedAt: now.UTC(), ExpiresAt: admission.ExpiresAt.UTC(),
	}
	s.submissions[admission.Key] = record
	s.reserved[index] = admission.Key
	return record, true, nil
}

// GetSubmission implements contextfabric.SubmissionStore.
func (s *Store) GetSubmission(ctx context.Context, key contextfabric.SubmissionKey, now time.Time) (contextfabric.SubmissionRecord, error) {
	if err := ctx.Err(); err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	if err := validSubmissionKey(key); err != nil {
		return contextfabric.SubmissionRecord{}, contextfabric.ErrSubmissionNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.submissions[key]
	if !found {
		return contextfabric.SubmissionRecord{}, contextfabric.ErrSubmissionNotFound
	}
	reconciled, changed := s.reconcileLocked(record, now)
	if changed {
		s.submissions[key] = reconciled
	}
	return reconciled, nil
}

// FinishSubmission implements contextfabric.SubmissionStore.
func (s *Store) FinishSubmission(ctx context.Context, key contextfabric.SubmissionKey, servedResultID string, now time.Time) (contextfabric.SubmissionRecord, contextfabric.SubmissionCompletion, error) {
	if err := validSubmissionKey(key); err != nil {
		return contextfabric.SubmissionRecord{}, "", err
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.SubmissionRecord{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.submissions[key]
	if !found {
		return contextfabric.SubmissionRecord{}, "", contextfabric.ErrSubmissionNotFound
	}
	if record.State != contextfabric.SubmissionRunning {
		if record.State == contextfabric.SubmissionAbandoned {
			return record, contextfabric.SubmissionCompletionNotRunning, nil
		}
		return record, contextfabric.SubmissionCompletionAlreadyFinal, nil
	}
	completion := contextfabric.SubmissionCompletionServedUnsaved
	record.State = contextfabric.SubmissionDoneUnsaved
	if servedResultID != "" && s.ownedByLocked(servedResultID, key) {
		completion = contextfabric.SubmissionCompletionServedStored
		record.State, record.ResultID = contextfabric.SubmissionDone, servedResultID
	}
	record.UpdatedAt = now.UTC()
	s.submissions[key] = record
	return record, completion, nil
}

// AbandonSubmission implements contextfabric.SubmissionStore.
func (s *Store) AbandonSubmission(ctx context.Context, key contextfabric.SubmissionKey, now time.Time) (contextfabric.SubmissionRecord, error) {
	if err := validSubmissionKey(key); err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.SubmissionRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.submissions[key]
	if !found {
		return contextfabric.SubmissionRecord{}, contextfabric.ErrSubmissionNotFound
	}
	if record.State == contextfabric.SubmissionRunning {
		record.State, record.UpdatedAt = contextfabric.SubmissionAbandoned, now.UTC()
		s.submissions[key] = record
	}
	return record, nil
}

// SweepSubmissions implements contextfabric.SubmissionStore.
func (s *Store) SweepSubmissions(ctx context.Context, now time.Time) (contextfabric.SubmissionSweep, error) {
	if err := ctx.Err(); err != nil {
		return contextfabric.SubmissionSweep{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var sweep contextfabric.SubmissionSweep
	for key, record := range s.submissions {
		reconciled, changed := s.reconcileLocked(record, now)
		if !changed {
			continue
		}
		s.submissions[key] = reconciled
		if reconciled.State == contextfabric.SubmissionDone {
			sweep.Completed++
		} else {
			sweep.Abandoned++
		}
	}
	return sweep, nil
}

// MarkSubmissionUnsaved implements contextfabric.SubmissionUnsavedMarker.
func (s *Store) MarkSubmissionUnsaved(ctx context.Context, principal storage.Principal, resultID string, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, found := s.reserved[reservedResultKey{orgID: orgID, resultID: resultID}]
	if !found {
		return false, nil
	}
	record := s.submissions[key]
	if record.State != contextfabric.SubmissionRunning || !record.Key.Owner.Is(principal) {
		return false, nil
	}
	record.State, record.UpdatedAt = contextfabric.SubmissionDoneUnsaved, now.UTC()
	s.submissions[key] = record
	return true, nil
}
