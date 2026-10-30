package memoryinvestigation

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The in-memory twin of pginvestigation's thread store (CHAOS-11948). The
// thread rows are written by AdmitSubmission under mu, with the record they
// admit; every rule below is decided under mu in the order the PostgreSQL
// statements decide it.

var _ contextfabric.ThreadStore = (*Store)(nil)

func validThreadKey(key contextfabric.ThreadKey) error {
	if strings.TrimSpace(key.OrgID) == "" || key.Owner.Subject == "" || key.Owner.AuthenticationMethod == "" {
		return errors.New("memoryinvestigation: thread organization and owner are required")
	}
	if !contextfabric.ValidSubmissionID(key.ThreadID) {
		return errors.New("memoryinvestigation: thread id is malformed")
	}
	return nil
}

func threadKeyOf(key contextfabric.SubmissionKey, threadID string) contextfabric.ThreadKey {
	return contextfabric.ThreadKey{OrgID: key.OrgID, Owner: key.Owner, ThreadID: threadID}
}

// threadClosedLocked is why a record's thread refuses a repeat of the key and
// hides the record from a status read: deleted or expired. Callers hold mu.
func (s *Store) threadClosedLocked(record contextfabric.SubmissionRecord, now time.Time) contextfabric.ThreadClosedReason {
	if record.ThreadID == "" {
		return ""
	}
	thread, found := s.threads[threadKeyOf(record.Key, record.ThreadID)]
	switch {
	case !found:
		return ""
	case thread.ClosedAt != nil:
		return contextfabric.ThreadClosedDeleted
	case !now.Before(thread.ExpiresAt):
		return contextfabric.ThreadClosedExpired
	}
	return ""
}

// hiddenByClosedThreadLocked: resultID is named by at least one thread turn
// of orgID (its final or its reserved result) and every record that names it
// belongs to a closed thread. Such a result is not served on any read (design
// §6 T6); a result one live turn, or one record with no thread, names stays
// readable (T3). Callers hold mu.
func (s *Store) hiddenByClosedThreadLocked(orgID, resultID string, now time.Time) bool {
	named := false
	for key, record := range s.submissions {
		if key.OrgID != orgID || (record.ResultID != resultID && record.ReservedResultID != resultID) {
			continue
		}
		if record.ThreadID == "" || s.threadClosedLocked(record, now) == "" {
			return false
		}
		named = true
	}
	return named
}

// ListThreads implements contextfabric.ThreadStore.
func (s *Store) ListThreads(ctx context.Context, orgID string, owner contextfabric.SubmissionOwner, after *contextfabric.ThreadListCursor, limit int, now time.Time) ([]contextfabric.ThreadRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(orgID) == "" || owner.Subject == "" || owner.AuthenticationMethod == "" || limit < 1 {
		return nil, false, errors.New("memoryinvestigation: thread list organization, owner and a positive limit are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var open []contextfabric.ThreadRecord
	for key, thread := range s.threads {
		if key.OrgID != orgID || key.Owner != owner || thread.Closed(now) {
			continue
		}
		if after != nil && !threadListAfter(thread, *after) {
			continue
		}
		open = append(open, thread)
	}
	sort.Slice(open, func(i, j int) bool { return threadListBefore(open[i], open[j]) })
	more := len(open) > limit
	if more {
		open = open[:limit]
	}
	return open, more, nil
}

func threadListBefore(a, b contextfabric.ThreadRecord) bool {
	if !a.LastTurnAt.Equal(b.LastTurnAt) {
		return a.LastTurnAt.After(b.LastTurnAt)
	}
	return a.Key.ThreadID < b.Key.ThreadID
}

// threadListAfter: thread comes after the cursor in list order.
func threadListAfter(thread contextfabric.ThreadRecord, cursor contextfabric.ThreadListCursor) bool {
	if !thread.LastTurnAt.Equal(cursor.LastTurnAt) {
		return thread.LastTurnAt.Before(cursor.LastTurnAt)
	}
	return thread.Key.ThreadID > cursor.ThreadID
}

// GetThread implements contextfabric.ThreadStore.
func (s *Store) GetThread(ctx context.Context, key contextfabric.ThreadKey, before *contextfabric.ThreadTurnCursor, limit int, now time.Time) (contextfabric.ThreadPage, error) {
	if err := ctx.Err(); err != nil {
		return contextfabric.ThreadPage{}, err
	}
	if err := validThreadKey(key); err != nil || limit < 1 {
		return contextfabric.ThreadPage{}, contextfabric.ErrThreadNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, found := s.threads[key]
	if !found || thread.Closed(now) {
		return contextfabric.ThreadPage{}, contextfabric.ErrThreadNotFound
	}
	var turns []contextfabric.ThreadTurn
	for submissionKey, record := range s.submissions {
		if record.ThreadID != key.ThreadID || threadKeyOf(submissionKey, record.ThreadID) != key {
			continue
		}
		cursor := contextfabric.ThreadTurnCursor{AdmittedAt: record.AdmittedAt, SubmissionID: submissionKey.SubmissionID}
		if before != nil && !cursor.Before(*before) {
			continue
		}
		reconciled, changed := s.reconcileLocked(record, now)
		if changed {
			s.submissions[submissionKey] = reconciled
		}
		turns = append(turns, contextfabric.ThreadTurn{
			SubmissionID: submissionKey.SubmissionID, State: reconciled.State, ResultID: reconciled.ResultID,
			Question: reconciled.Question, AdmittedAt: reconciled.AdmittedAt, UpdatedAt: reconciled.UpdatedAt,
		})
	}
	sort.Slice(turns, func(i, j int) bool {
		return contextfabric.ThreadTurnCursor{AdmittedAt: turns[i].AdmittedAt, SubmissionID: turns[i].SubmissionID}.
			Before(contextfabric.ThreadTurnCursor{AdmittedAt: turns[j].AdmittedAt, SubmissionID: turns[j].SubmissionID})
	})
	earlier := len(turns) > limit
	if earlier {
		turns = turns[len(turns)-limit:]
	}
	if len(turns) == 0 {
		// A cursor before the first turn names no page.
		return contextfabric.ThreadPage{}, contextfabric.ErrThreadNotFound
	}
	return contextfabric.ThreadPage{Thread: thread, Turns: turns, EarlierTurns: earlier}, nil
}

// CloseThread implements contextfabric.ThreadStore.
func (s *Store) CloseThread(ctx context.Context, key contextfabric.ThreadKey, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validThreadKey(key); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, found := s.threads[key]
	if !found || thread.Closed(now) {
		return false, nil
	}
	closedAt := now.UTC()
	thread.ClosedAt = &closedAt
	s.threads[key] = thread
	for submissionKey, record := range s.submissions {
		if record.ThreadID == key.ThreadID && threadKeyOf(submissionKey, record.ThreadID) == key {
			record.Question = ""
			s.submissions[submissionKey] = record
		}
	}
	return true, nil
}
