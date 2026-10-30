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
