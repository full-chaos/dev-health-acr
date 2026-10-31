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

// threadRowKey is a thread row's identity: organization, the legacy pair of
// the owner the row was created under, thread id.
type threadRowKey struct {
	orgID, method, subject, threadID string
}

func rowKeyOf(key contextfabric.ThreadKey) threadRowKey {
	return threadRowKey{orgID: key.OrgID, method: key.Owner.AuthenticationMethod, subject: key.Owner.Subject, threadID: key.ThreadID}
}

// findThreadLocked is the row owner's threadID reaches in orgID: a row whose
// owner is the same owner (contextfabric.SameSubmissionOwner); the row with
// owner's own legacy pair first, then the earliest created, then the legacy
// pair's order (the PostgreSQL store's ORDER BY). Callers hold mu.
func (s *Store) findThreadLocked(orgID string, owner contextfabric.SubmissionOwner, threadID string) (contextfabric.ThreadRecord, bool) {
	var best contextfabric.ThreadRecord
	found := false
	for key, thread := range s.threads {
		if key.orgID != orgID || key.threadID != threadID || !contextfabric.SameSubmissionOwner(thread.Key.Owner, owner) {
			continue
		}
		if !found || threadRowPreferred(thread, best, owner) {
			best, found = thread, true
		}
	}
	return best, found
}

// threadRowPreferred: row a is preferred over row b for owner.
func threadRowPreferred(a, b contextfabric.ThreadRecord, owner contextfabric.SubmissionOwner) bool {
	ownA := a.Key.Owner.AuthenticationMethod == owner.AuthenticationMethod && a.Key.Owner.Subject == owner.Subject
	ownB := b.Key.Owner.AuthenticationMethod == owner.AuthenticationMethod && b.Key.Owner.Subject == owner.Subject
	if ownA != ownB {
		return ownA
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	if a.Key.Owner.AuthenticationMethod != b.Key.Owner.AuthenticationMethod {
		return a.Key.Owner.AuthenticationMethod < b.Key.Owner.AuthenticationMethod
	}
	return a.Key.Owner.Subject < b.Key.Owner.Subject
}

// recordThreadRow is the row a thread turn joined: its ThreadOwner's legacy
// pair, or its own owner's for a record stored without one.
func recordThreadRow(key contextfabric.SubmissionKey, record contextfabric.SubmissionRecord) threadRowKey {
	owner := record.ThreadOwner
	if owner.Subject == "" {
		owner = key.Owner
	}
	return threadRowKey{orgID: key.OrgID, method: owner.AuthenticationMethod, subject: owner.Subject, threadID: record.ThreadID}
}

// threadClosedLocked is why a record's thread refuses a repeat of the key and
// hides the record from a status read: deleted or expired. With the store's
// unthreaded retention on (CHAOS-11952), a record with no thread is a
// one-turn thread with retention 0: expired from
// contextfabric.UnthreadedRecordExpiresAt; off, it stays live. Callers hold
// mu.
func (s *Store) threadClosedLocked(record contextfabric.SubmissionRecord, now time.Time) contextfabric.ThreadClosedReason {
	if record.ThreadID == "" {
		if s.unthreadedRetention && !now.Before(contextfabric.UnthreadedRecordExpiresAt(record.AdmittedAt)) {
			return contextfabric.ThreadClosedExpired
		}
		return ""
	}
	thread, found := s.threads[recordThreadRow(record.Key, record)]
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

// hiddenByClosedThreadLocked: resultID is named by at least one submission
// record of orgID (its final or its reserved result) and every record that
// names it is closed: a turn of a closed thread, or (with the store's
// unthreaded retention on, CHAOS-11952) a record with no thread past its
// retention-0 expiry. Off, a record with no thread keeps its result
// readable, as before. Such a result is not served on
// any read (design §6 T6); a result one live record names stays readable
// (T3), and a result no record names (MCP, Workbench) is out of scope.
// Callers hold mu.
func (s *Store) hiddenByClosedThreadLocked(orgID, resultID string, now time.Time) bool {
	named := false
	for key, record := range s.submissions {
		if key.OrgID != orgID || (record.ResultID != resultID && record.ReservedResultID != resultID) {
			continue
		}
		if s.threadClosedLocked(record, now) == "" {
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
	// One row per thread id: the row the owner reaches (findThreadLocked).
	reached := map[string]contextfabric.ThreadRecord{}
	for key, thread := range s.threads {
		if key.orgID != orgID || !contextfabric.SameSubmissionOwner(thread.Key.Owner, owner) {
			continue
		}
		if best, seen := reached[key.threadID]; !seen || threadRowPreferred(thread, best, owner) {
			reached[key.threadID] = thread
		}
	}
	var open []contextfabric.ThreadRecord
	for _, thread := range reached {
		if thread.Closed(now) {
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
	thread, found := s.findThreadLocked(key.OrgID, key.Owner, key.ThreadID)
	if !found || thread.Closed(now) {
		return contextfabric.ThreadPage{}, contextfabric.ErrThreadNotFound
	}
	row := rowKeyOf(thread.Key)
	var turns []contextfabric.ThreadTurn
	for submissionKey, record := range s.submissions {
		if record.ThreadID != key.ThreadID || recordThreadRow(submissionKey, record) != row {
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
	thread, found := s.findThreadLocked(key.OrgID, key.Owner, key.ThreadID)
	if !found || thread.Closed(now) {
		return false, nil
	}
	closedAt := now.UTC()
	thread.ClosedAt = &closedAt
	row := rowKeyOf(thread.Key)
	s.threads[row] = thread
	for submissionKey, record := range s.submissions {
		if record.ThreadID == key.ThreadID && recordThreadRow(submissionKey, record) == row {
			record.Question = ""
			s.submissions[submissionKey] = record
		}
	}
	return true, nil
}
