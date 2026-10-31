package memoryinvestigation

import (
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// SeedThreadTurnCount sets a thread row's turn count for the parity suite's
// full-thread case (paritytest.ThreadTurnCountSeeder). Test builds only.
func (s *Store) SeedThreadTurnCount(key contextfabric.ThreadKey, count int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, found := s.findThreadLocked(key.OrgID, key.Owner, key.ThreadID)
	if !found {
		return errors.New("memoryinvestigation: no thread row to seed")
	}
	thread.TurnCount = count
	s.threads[rowKeyOf(thread.Key)] = thread
	return nil
}

// RemoveSubmissionRecord deletes one submission record and its log, as the
// retention job does (paritytest.SubmissionRecordRemover). Test builds only.
func (s *Store) RemoveSubmissionRecord(key contextfabric.SubmissionKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, record, ok := s.findSubmissionLocked(key)
	if !ok {
		return errors.New("memoryinvestigation: no submission record to remove")
	}
	delete(s.submissions, found)
	delete(s.reserved, reservedResultKey{orgID: found.OrgID, resultID: record.ReservedResultID})
	delete(s.runEvents, contextfabric.RunKeyOf(record))
	return nil
}
