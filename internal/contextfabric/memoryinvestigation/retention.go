package memoryinvestigation

import (
	"context"
	"sort"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The retention job's in-memory twin (CHAOS-11952): the rules
// contextfabric/retention.go states and pginvestigation/retention.go holds in
// SQL, under the store's one lock, so a unit is removed atomically as the
// PostgreSQL store removes it in one transaction.

var _ contextfabric.RetentionSweeper = (*Store)(nil)

// SweepRetention implements contextfabric.RetentionSweeper.
func (s *Store) SweepRetention(ctx context.Context, now time.Time, limit int) (contextfabric.RetentionSweep, error) {
	var sweep contextfabric.RetentionSweep
	if err := ctx.Err(); err != nil {
		return sweep, err
	}
	if !s.unthreadedRetention {
		return sweep, contextfabric.ErrRetentionReadRuleOff
	}
	if limit < 1 {
		limit = contextfabric.RetentionSweepBatch
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	threads := s.dueRetentionThreadsLocked(now)
	if len(threads) > limit {
		threads = threads[:limit]
	}
	for _, row := range threads {
		s.sweepRetentionThreadLocked(row, now, &sweep)
	}
	if remaining := limit - len(threads); remaining > 0 {
		records := s.dueRetentionRecordsLocked(now)
		if len(records) > remaining {
			records = records[:remaining]
		}
		for _, key := range records {
			s.sweepRetentionRecordLocked(key, now, &sweep)
		}
	}
	return sweep, nil
}

// ProbeRetention implements contextfabric.RetentionSweeper: the memory store
// holds no privileges, so there is nothing to prove.
func (s *Store) ProbeRetention(ctx context.Context) error {
	return ctx.Err()
}

// threadRecordsLocked lists the records whose thread row is row. Callers
// hold mu.
func (s *Store) threadRecordsLocked(row threadRowKey) []contextfabric.SubmissionKey {
	var keys []contextfabric.SubmissionKey
	for key, record := range s.submissions {
		if record.ThreadID != "" && recordThreadRow(key, record) == row {
			keys = append(keys, key)
		}
	}
	return keys
}

// dueRetentionThreadsLocked lists the thread rows that are expired, or
// deleted with a turn not yet erased, oldest expiry first. Callers hold mu.
func (s *Store) dueRetentionThreadsLocked(now time.Time) []threadRowKey {
	var due []threadRowKey
	for row, thread := range s.threads {
		if !now.Before(thread.ExpiresAt) {
			due = append(due, row)
			continue
		}
		if thread.ClosedAt == nil {
			continue
		}
		for _, key := range s.threadRecordsLocked(row) {
			if !contextfabric.IsErasedReservedResultID(s.submissions[key].ReservedResultID) {
				due = append(due, row)
				break
			}
		}
	}
	sort.Slice(due, func(i, j int) bool {
		a, b := s.threads[due[i]], s.threads[due[j]]
		if !a.ExpiresAt.Equal(b.ExpiresAt) {
			return a.ExpiresAt.Before(b.ExpiresAt)
		}
		if due[i].orgID != due[j].orgID {
			return due[i].orgID < due[j].orgID
		}
		return due[i].threadID < due[j].threadID
	})
	return due
}

// dueRetentionRecordsLocked lists records with no thread past their
// retention-0 window, oldest admission first. Callers hold mu.
func (s *Store) dueRetentionRecordsLocked(now time.Time) []contextfabric.SubmissionKey {
	var due []contextfabric.SubmissionKey
	for key, record := range s.submissions {
		if record.ThreadID == "" && !now.Before(contextfabric.UnthreadedRecordExpiresAt(record.AdmittedAt)) {
			due = append(due, key)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		a, b := s.submissions[due[i]], s.submissions[due[j]]
		if !a.AdmittedAt.Equal(b.AdmittedAt) {
			return a.AdmittedAt.Before(b.AdmittedAt)
		}
		if due[i].OrgID != due[j].OrgID {
			return due[i].OrgID < due[j].OrgID
		}
		return due[i].SubmissionID < due[j].SubmissionID
	})
	return due
}

// sweepRetentionThreadLocked removes an expired thread's whole backing set,
// or a deleted thread's content (keeping its closure record). Callers hold
// mu.
func (s *Store) sweepRetentionThreadLocked(row threadRowKey, now time.Time, sweep *contextfabric.RetentionSweep) {
	thread, found := s.threads[row]
	if !found {
		return
	}
	expired := !now.Before(thread.ExpiresAt)
	if !expired && thread.ClosedAt == nil {
		return
	}
	keys := s.threadRecordsLocked(row)
	for _, key := range keys {
		if contextfabric.RetentionFenced(s.submissions[key], now) {
			sweep.Fenced++
			return
		}
	}
	sweep.ResultsRemoved += s.removeRetainedResultsLocked(row.orgID, keys, now)
	if expired {
		for _, key := range keys {
			s.removeRecordLocked(key)
		}
		delete(s.threads, row)
		sweep.ThreadsRemoved++
		return
	}
	for _, key := range keys {
		record := s.submissions[key]
		if contextfabric.IsErasedReservedResultID(record.ReservedResultID) {
			continue
		}
		delete(s.runEvents, contextfabric.RunKeyOf(record))
		delete(s.reserved, reservedResultKey{orgID: key.OrgID, resultID: record.ReservedResultID})
		if record.State == contextfabric.SubmissionDone {
			record.State = contextfabric.SubmissionDoneUnsaved
		}
		record.ResultID, record.Question = "", ""
		record.ReservedResultID = contextfabric.ErasedReservedResultID(record.ReservedResultID)
		record.UpdatedAt = now
		s.submissions[key] = record
		s.reserved[reservedResultKey{orgID: key.OrgID, resultID: record.ReservedResultID}] = key
	}
	sweep.ThreadsErased++
}

// sweepRetentionRecordLocked removes a record with no thread past its
// retention-0 window, with its result. Callers hold mu.
func (s *Store) sweepRetentionRecordLocked(key contextfabric.SubmissionKey, now time.Time, sweep *contextfabric.RetentionSweep) {
	record, found := s.submissions[key]
	if !found || record.ThreadID != "" {
		return
	}
	if contextfabric.RetentionFenced(record, now) {
		sweep.Fenced++
		return
	}
	sweep.ResultsRemoved += s.removeRetainedResultsLocked(key.OrgID, []contextfabric.SubmissionKey{key}, now)
	s.removeRecordLocked(key)
	sweep.RecordsRemoved++
}

// removeRecordLocked deletes one record, its reservation and its event log.
// Callers hold mu.
func (s *Store) removeRecordLocked(key contextfabric.SubmissionKey) {
	record := s.submissions[key]
	delete(s.submissions, key)
	delete(s.reserved, reservedResultKey{orgID: key.OrgID, resultID: record.ReservedResultID})
	delete(s.runEvents, contextfabric.RunKeyOf(record))
}

// removeRetainedResultsLocked removes the results the unit's records name,
// except a result a live record still names (design §6 T3), with the
// supersession claims made on them and their feedback. It returns how many
// result rows went. Callers hold mu.
func (s *Store) removeRetainedResultsLocked(orgID string, keys []contextfabric.SubmissionKey, now time.Time) int {
	removed := 0
	seen := map[string]bool{}
	for _, key := range keys {
		record := s.submissions[key]
		for _, id := range []string{record.ResultID, record.ReservedResultID} {
			if id == "" || seen[id] || contextfabric.IsErasedReservedResultID(id) {
				continue
			}
			seen[id] = true
			if s.namedByLiveRecordLocked(orgID, id, now) {
				continue
			}
			stored, found := s.results[id]
			if !found || stored.orgID != orgID {
				continue
			}
			delete(s.results, id)
			delete(s.feedback, id)
			for claim := range s.claims {
				if claim.orgID == orgID && claim.priorResultID == id {
					delete(s.claims, claim)
				}
			}
			removed++
		}
	}
	return removed
}

// namedByLiveRecordLocked: a record of orgID that is not closed at now names
// resultID. Callers hold mu.
func (s *Store) namedByLiveRecordLocked(orgID, resultID string, now time.Time) bool {
	for key, record := range s.submissions {
		if key.OrgID == orgID && (record.ResultID == resultID || record.ReservedResultID == resultID) && s.threadClosedLocked(record, now) == "" {
			return true
		}
	}
	return false
}
