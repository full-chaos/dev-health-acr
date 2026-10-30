package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Ask Dev conversations (threads; CHAOS-11948, plan §2.6, contract
// docs/design/ask-dev-conversation-contract.md). A thread is the submission
// records one owner admitted with one X-ACR-Thread-ID, keyed exactly like a
// submission's owner: (organization, owner, thread id). Another principal's
// turn with the same thread id is another thread. The thread row holds what
// the turns do not: created_at, last_turn_at, the turn count, the retention
// the latest naming turn set, the expiry, and the closure of a delete.
//
// A thread is closed when it was deleted (closed_at set) or its expiry has
// passed. A closed thread is not served on any read, a turn naming it is
// refused (409 thread_closed), and a result only its closed turns name is
// not served either (the result stores' Get and answer reuse hide it). The
// rows stay, content-free, until the retention job removes them after the
// expiry (design §6 T1): delete erases the turns' questions at once and
// keeps the submission records, so a run still in flight saves bound to its
// owner (T2).

// ThreadKey is the unique key of a thread.
type ThreadKey struct {
	OrgID    string
	Owner    SubmissionOwner
	ThreadID string
}

// ThreadKeyFor builds the key of threadID for principal, with the same owner
// a submission of the principal records.
func ThreadKeyFor(principal storage.Principal, threadID string) (ThreadKey, error) {
	owner, err := SubmissionOwnerOf(principal)
	if err != nil {
		return ThreadKey{}, err
	}
	return ThreadKey{OrgID: principal.OrgID, Owner: owner, ThreadID: threadID}, nil
}

// ThreadRecord is one thread's row.
type ThreadRecord struct {
	Key           ThreadKey
	CreatedAt     time.Time
	LastTurnAt    time.Time
	TurnCount     int
	RetentionDays contractsv1.ContextFabricThreadRetentionDays
	// ExpiresAt is ContextFabricThreadExpiresAt(LastTurnAt, RetentionDays).
	ExpiresAt time.Time
	// ClosedAt is set when the thread was deleted.
	ClosedAt *time.Time
	// RetentionSetBy is the turn whose X-ACR-Thread-Retention-Days set
	// RetentionDays, nil while no turn named one (the default applies).
	RetentionSetBy *ThreadTurnCursor
}

// Closed reports whether the thread is closed at now: deleted, or past its
// expiry.
func (r ThreadRecord) Closed(now time.Time) bool {
	return r.ClosedAt != nil || !now.Before(r.ExpiresAt)
}

// ClosedReason is why a turn naming the thread is refused at now, or "":
// deleted, expired, or full (ContextFabricThreadMaxTurnCount turns).
func (r ThreadRecord) ClosedReason(now time.Time) ThreadClosedReason {
	switch {
	case r.ClosedAt != nil:
		return ThreadClosedDeleted
	case !now.Before(r.ExpiresAt):
		return ThreadClosedExpired
	case r.TurnCount >= contractsv1.ContextFabricThreadMaxTurnCount:
		return ThreadClosedFull
	}
	return ""
}

// Before reports whether a precedes b in turn order.
func (a ThreadTurnCursor) Before(b ThreadTurnCursor) bool {
	if !a.AdmittedAt.Equal(b.AdmittedAt) {
		return a.AdmittedAt.Before(b.AdmittedAt)
	}
	return a.SubmissionID < b.SubmissionID
}

// AdvanceThread is the thread row after one more turn is admitted: thread is
// the current row, nil for the thread's first turn. The latest turn in turn
// order that names a retention decides it (a turn admitted out of clock
// order does not override a later one); a thread whose turns named none
// keeps the default. Callers decide ClosedReason first.
func AdvanceThread(thread *ThreadRecord, key ThreadKey, turn ThreadTurnCursor, retention *contractsv1.ContextFabricThreadRetentionDays) ThreadRecord {
	at := turn.AdmittedAt.UTC()
	turn.AdmittedAt = at
	var next ThreadRecord
	if thread == nil {
		next = ThreadRecord{Key: key, CreatedAt: at, LastTurnAt: at, RetentionDays: contractsv1.ContextFabricThreadDefaultRetention}
	} else {
		next = *thread
		if at.Before(next.CreatedAt) {
			next.CreatedAt = at
		}
		if at.After(next.LastTurnAt) {
			next.LastTurnAt = at
		}
	}
	next.TurnCount++
	if retention != nil && (next.RetentionSetBy == nil || !turn.Before(*next.RetentionSetBy)) {
		setBy := turn
		next.RetentionDays, next.RetentionSetBy = *retention, &setBy
	}
	next.ExpiresAt = contractsv1.ContextFabricThreadExpiresAt(next.LastTurnAt, next.RetentionDays)
	return next
}

// ThreadTurn is one turn of a thread: a submission record and the question
// it was admitted with.
type ThreadTurn struct {
	SubmissionID string
	State        SubmissionState
	// ResultID is the final result id, set only in state done.
	ResultID   string
	Question   string
	AdmittedAt time.Time
	UpdatedAt  time.Time
}

// ThreadTurnCursor is the position of a turn in turn order (admitted_at,
// then submission id).
type ThreadTurnCursor struct {
	AdmittedAt   time.Time
	SubmissionID string
}

// ThreadListCursor is the position of a thread in list order (last_turn_at
// newest first, then thread id ascending).
type ThreadListCursor struct {
	LastTurnAt time.Time
	ThreadID   string
}

// ThreadPage is one read of a thread: its row and one page of its turns,
// oldest first.
type ThreadPage struct {
	Thread ThreadRecord
	Turns  []ThreadTurn
	// EarlierTurns is true when turns earlier than the page exist.
	EarlierTurns bool
}

// ThreadClosedReason says why a turn naming a thread was refused.
type ThreadClosedReason string

const (
	ThreadClosedDeleted ThreadClosedReason = "deleted"
	ThreadClosedExpired ThreadClosedReason = "expired"
	// ThreadClosedFull: the thread holds ContextFabricThreadMaxTurnCount
	// turns (design §6 T7).
	ThreadClosedFull ThreadClosedReason = "full"
)

// ThreadClosedReasonVocabulary is the closed set of reasons.
func ThreadClosedReasonVocabulary() [3]ThreadClosedReason {
	return [3]ThreadClosedReason{ThreadClosedDeleted, ThreadClosedExpired, ThreadClosedFull}
}

// ThreadClosedError is an admission (or a repeat of a key) refused because
// its thread is closed. Nothing ran and nothing was written.
type ThreadClosedError struct {
	Reason ThreadClosedReason
}

func (e *ThreadClosedError) Error() string {
	return fmt.Sprintf("context fabric: the thread is closed (%s)", e.Reason)
}

// ErrThreadNotFound is a thread read or turn read that found no open thread
// for the key: unknown, another principal's, deleted or expired, which no
// caller can tell apart.
var ErrThreadNotFound = errors.New("context fabric: thread not found")

// ThreadStore reads and closes threads. Turns are admitted through
// SubmissionStore.AdmitSubmission (a SubmissionAdmission with a ThreadID),
// which writes the thread row in the admission's own transaction.
type ThreadStore interface {
	// ListThreads returns up to limit open threads of owner in list order,
	// after the cursor when one is given, and whether more follow.
	ListThreads(ctx context.Context, orgID string, owner SubmissionOwner, after *ThreadListCursor, limit int, now time.Time) ([]ThreadRecord, bool, error)
	// GetThread returns the open thread and up to limit of its turns in turn
	// order: the latest ones, or the ones before the cursor. Turn states are
	// reconciled like a submission status read. ErrThreadNotFound when no
	// open thread exists for the key.
	GetThread(ctx context.Context, key ThreadKey, before *ThreadTurnCursor, limit int, now time.Time) (ThreadPage, error)
	// CloseThread deletes the thread: it records the closure and erases
	// the questions of its turns, in one transaction. closed is false when
	// no open thread existed for the key (nothing is written).
	CloseThread(ctx context.Context, key ThreadKey, now time.Time) (closed bool, err error)
}
