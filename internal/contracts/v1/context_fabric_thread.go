package v1

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Ask Dev conversations (Ask Dev plan §2.6, item I-28). A thread is one
// principal's conversation: the submission records the principal admitted
// with one X-ACR-Thread-ID, in admission order, plus the thread's retention.
// The key is (organization, owner, thread id), so another principal's turn
// with the same thread id belongs to another thread. A thread names each
// turn's result by id and carries no answer content; the result is read
// through GET /api/v1/context-fabric/investigations/{result_id}, which decides
// the read again. See docs/design/ask-dev-conversation-contract.md.
const (
	// ContextFabricThreadSchema is the schema_version of the thread read,
	// GET /api/v1/context-fabric/threads/{thread_id}.
	ContextFabricThreadSchema = "context_fabric_thread.v1"
	// ContextFabricThreadListSchema is the schema_version of the thread list,
	// GET /api/v1/context-fabric/threads.
	ContextFabricThreadListSchema = "context_fabric_thread_list.v1"

	// ContextFabricThreadRetentionHeader is the optional create-investigation
	// header that sets the thread's retention: exactly "0" or "30". The
	// latest turn of a thread that named one decides; a thread whose turns
	// named none has ContextFabricThreadDefaultRetention. Like
	// X-ACR-Thread-ID it is not part of the submission digest.
	ContextFabricThreadRetentionHeader = "X-ACR-Thread-Retention-Days"

	// ContextFabricThreadMaxServedTurns is how many turns one thread read
	// serves: the latest ones, oldest first. turn_count states every turn;
	// earlier turns are read with the before query parameter, a page at a
	// time, so no turn of a retained thread is out of reach.
	ContextFabricThreadMaxServedTurns = 100
	// ContextFabricThreadListDefaultLimit and ContextFabricThreadListMaxLimit
	// bound the list's limit query parameter.
	ContextFabricThreadListDefaultLimit = 20
	ContextFabricThreadListMaxLimit     = 50
	// ContextFabricThreadCursorMaxLength bounds the opaque cursors: the
	// list's next_cursor and the thread's earlier_turns_cursor.
	ContextFabricThreadCursorMaxLength = 512
	// ContextFabricThreadMaxTurnCount is the most turns a thread holds. A
	// turn that would pass it is refused 409 thread_closed (the conversation
	// is full). It is far above any thread retention allows in practice, and
	// it gives turn_count a published maximum, so every validator of the
	// schema bounds it as Go's int does.
	ContextFabricThreadMaxTurnCount = 1_000_000
	// ContextFabricThreadQuestionMaxRunes is the bound of a turn's question:
	// the create-investigation request's own bound.
	ContextFabricThreadQuestionMaxRunes = 8000

	// ErrorCodeThreadClosed: 409 on create investigation. X-ACR-Thread-ID,
	// or X-ACR-Submission-ID, belongs to a thread of the caller that was
	// deleted or has expired, while the service holds the thread's closure
	// record (at least until its expires_at), or to a thread that holds
	// ContextFabricThreadMaxTurnCount turns. Nothing runs. Not retryable;
	// start a new thread.
	ErrorCodeThreadClosed = "thread_closed"
	// ContextFabricThreadClosedStatus and ContextFabricThreadClosedMessage
	// are the refusal's status and message.
	ContextFabricThreadClosedStatus  = 409
	ContextFabricThreadClosedMessage = "The conversation was deleted or has expired; start a new one"
)

// ContextFabricThreadRetentionDays is the closed retention of a thread. The
// thread's retention is the value of its latest turn (admitted_at, then
// submission_id) that named one.
type ContextFabricThreadRetentionDays int

const (
	// ContextFabricThreadRetentionDay: kept until 24 hours after the last
	// turn ("0-day" retention, chris 2026-10-29).
	ContextFabricThreadRetentionDay ContextFabricThreadRetentionDays = 0
	// ContextFabricThreadRetentionMonth: kept until 30 days after the last
	// turn.
	ContextFabricThreadRetentionMonth ContextFabricThreadRetentionDays = 30
)

// ContextFabricThreadDefaultRetention is the retention of a thread whose
// turns named none: the shortest.
const ContextFabricThreadDefaultRetention = ContextFabricThreadRetentionDay

// ContextFabricThreadRetentionVocabulary is the closed set, in the published
// enum's order.
func ContextFabricThreadRetentionVocabulary() [2]ContextFabricThreadRetentionDays {
	return [2]ContextFabricThreadRetentionDays{ContextFabricThreadRetentionDay, ContextFabricThreadRetentionMonth}
}

// Valid reports whether r is a member of the closed set.
func (r ContextFabricThreadRetentionDays) Valid() bool {
	members := ContextFabricThreadRetentionVocabulary()
	return slices.Contains(members[:], r)
}

// Window is how long after the last turn a thread with retention r is kept.
// It is zero for a value outside the closed set.
func (r ContextFabricThreadRetentionDays) Window() time.Duration {
	switch r {
	case ContextFabricThreadRetentionDay:
		return 24 * time.Hour
	case ContextFabricThreadRetentionMonth:
		return 30 * 24 * time.Hour
	default:
		return 0
	}
}

// ErrContextFabricThreadRetentionInvalid is a retention header value outside
// the closed grammar.
var ErrContextFabricThreadRetentionInvalid = errors.New("thread retention must be exactly 0 or 30")

// ParseContextFabricThreadRetention is the one grammar of the retention
// header: exactly "0" or "30", nothing else (no sign, no padding, no unit).
func ParseContextFabricThreadRetention(value string) (ContextFabricThreadRetentionDays, error) {
	switch value {
	case "0":
		return ContextFabricThreadRetentionDay, nil
	case "30":
		return ContextFabricThreadRetentionMonth, nil
	default:
		return 0, ErrContextFabricThreadRetentionInvalid
	}
}

// ContextFabricThreadExpiresAt is the thread's expiry: lastTurnAt plus the
// retention window. From it on, every read of the thread and of its turns'
// results fails closed, and the retention job removes the backing set.
func ContextFabricThreadExpiresAt(lastTurnAt time.Time, retention ContextFabricThreadRetentionDays) time.Time {
	return lastTurnAt.Add(retention.Window())
}

// ContextFabricThreadTurn is one turn: one submission record of the thread.
type ContextFabricThreadTurn struct {
	SubmissionID string                       `json:"submission_id"`
	State        ContextFabricSubmissionState `json:"state"`
	// Question is the question the turn was admitted with, as the caller
	// sent it: the caller's own words, so a reload can show a running,
	// unsaved or abandoned turn too. Absent on a turn admitted before the
	// service recorded questions.
	Question string `json:"question,omitempty"`
	// ResultID is present exactly when State is done and the result read by
	// id would serve the result to the reader now: the stored-result gate
	// (surface "thread") admits it and the read's serving rules serve it.
	ResultID string `json:"result_id,omitempty"`
	// ResultWithheld is true exactly when State is done and the result read
	// by id would not serve the result to the reader now. The result id is
	// then not served.
	ResultWithheld bool      `json:"result_withheld,omitempty"`
	AdmittedAt     time.Time `json:"admitted_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Validate holds the turn to its schema.
func (t ContextFabricThreadTurn) Validate() error {
	states := ContextFabricSubmissionStateVocabulary()
	done := t.State == ContextFabricSubmissionDone
	switch {
	case !ValidContextFabricClientCorrelationID(t.SubmissionID):
		return fmt.Errorf("thread turn submission_id violates v1 bounds")
	case !slices.Contains(states[:], t.State):
		return fmt.Errorf("thread turn state is not a member of the closed vocabulary")
	// The question is the admitted request's, already held to that
	// request's own rules; here it is held to the published bound exactly
	// (1..8000 runes, no trimming), so the validator and the schema agree on
	// every string.
	case t.Question != "" && !stringLengthBetween(t.Question, 1, ContextFabricThreadQuestionMaxRunes):
		return fmt.Errorf("thread turn question violates v1 bounds")
	case t.ResultID != "" && !stringLengthBetween(t.ResultID, 8, 256):
		return fmt.Errorf("thread turn result_id violates v1 bounds")
	case t.ResultID != "" && t.ResultWithheld:
		return fmt.Errorf("thread turn names a result and withholds it")
	case done != (t.ResultID != "" || t.ResultWithheld):
		return fmt.Errorf("thread turn has a result_id or result_withheld exactly when its state is done")
	case t.AdmittedAt.IsZero():
		return fmt.Errorf("thread turn admitted_at is required")
	case t.UpdatedAt.IsZero():
		return fmt.Errorf("thread turn updated_at is required")
	case t.UpdatedAt.Before(t.AdmittedAt):
		return fmt.Errorf("thread turn updated_at is before admitted_at")
	}
	return nil
}

// ContextFabricThreadSummary is a thread as the list names it.
type ContextFabricThreadSummary struct {
	ThreadID      string                           `json:"thread_id"`
	RetentionDays ContextFabricThreadRetentionDays `json:"retention_days"`
	// TurnCount is every turn of the thread, at least one.
	TurnCount int `json:"turn_count"`
	// CreatedAt is the first turn's admitted_at; LastTurnAt the latest's.
	CreatedAt  time.Time `json:"created_at"`
	LastTurnAt time.Time `json:"last_turn_at"`
	// ExpiresAt is ContextFabricThreadExpiresAt(LastTurnAt, RetentionDays).
	ExpiresAt time.Time `json:"expires_at"`
}

// Validate holds the summary to its schema.
func (s ContextFabricThreadSummary) Validate() error {
	switch {
	case !ValidContextFabricClientCorrelationID(s.ThreadID):
		return fmt.Errorf("thread thread_id violates v1 bounds")
	case !s.RetentionDays.Valid():
		return fmt.Errorf("thread retention_days is not a member of the closed vocabulary")
	case s.TurnCount < 1 || s.TurnCount > ContextFabricThreadMaxTurnCount:
		return fmt.Errorf("thread turn_count must be 1..%d", ContextFabricThreadMaxTurnCount)
	case s.CreatedAt.IsZero() || s.LastTurnAt.IsZero() || s.ExpiresAt.IsZero():
		return fmt.Errorf("thread created_at, last_turn_at and expires_at are required")
	case s.LastTurnAt.Before(s.CreatedAt):
		return fmt.Errorf("thread last_turn_at is before created_at")
	case s.TurnCount == 1 && !s.LastTurnAt.Equal(s.CreatedAt):
		return fmt.Errorf("a thread of one turn has last_turn_at equal to created_at")
	case !s.ExpiresAt.Equal(ContextFabricThreadExpiresAt(s.LastTurnAt, s.RetentionDays)):
		return fmt.Errorf("thread expires_at must be last_turn_at plus the retention window")
	}
	return nil
}

// ContextFabricThread is the body of GET /api/v1/context-fabric/threads/{thread_id}:
// the caller's own thread.
type ContextFabricThread struct {
	SchemaVersion string                           `json:"schema_version"`
	ThreadID      string                           `json:"thread_id"`
	RetentionDays ContextFabricThreadRetentionDays `json:"retention_days"`
	// TurnCount, CreatedAt, LastTurnAt and ExpiresAt are as on
	// ContextFabricThreadSummary.
	TurnCount  int       `json:"turn_count"`
	CreatedAt  time.Time `json:"created_at"`
	LastTurnAt time.Time `json:"last_turn_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// Turns are one page of at most ContextFabricThreadMaxServedTurns turns,
	// oldest first (admitted_at, then submission_id): the latest turns, or
	// the turns before the before query parameter's cursor.
	Turns []ContextFabricThreadTurn `json:"turns"`
	// EarlierTurnsCursor is present when turns earlier than this page exist;
	// pass it back as the before query parameter. Opaque.
	EarlierTurnsCursor string `json:"earlier_turns_cursor,omitempty"`
}

// Validate holds the thread to its schema.
func (t ContextFabricThread) Validate() error {
	if t.SchemaVersion != ContextFabricThreadSchema {
		return fmt.Errorf("thread schema_version must be %q", ContextFabricThreadSchema)
	}
	if err := t.Summary().Validate(); err != nil {
		return err
	}
	if len(t.Turns) < 1 || len(t.Turns) > min(t.TurnCount, ContextFabricThreadMaxServedTurns) {
		return fmt.Errorf("thread serves %d turns, want 1..min(turn_count, %d)", len(t.Turns), ContextFabricThreadMaxServedTurns)
	}
	if t.EarlierTurnsCursor != "" && !ValidContextFabricThreadCursor(t.EarlierTurnsCursor) {
		return fmt.Errorf("thread earlier_turns_cursor violates v1 bounds")
	}
	if t.EarlierTurnsCursor != "" && len(t.Turns) >= t.TurnCount {
		return fmt.Errorf("thread names earlier turns while it serves every turn")
	}
	seen := make(map[string]struct{}, len(t.Turns))
	for i, turn := range t.Turns {
		if err := turn.Validate(); err != nil {
			return fmt.Errorf("thread turn %d: %w", i, err)
		}
		if _, dup := seen[turn.SubmissionID]; dup {
			return fmt.Errorf("thread turn %d repeats a submission_id", i)
		}
		seen[turn.SubmissionID] = struct{}{}
		if i > 0 && !contextFabricThreadTurnBefore(t.Turns[i-1], turn) {
			return fmt.Errorf("thread turn %d is out of order (admitted_at, then submission_id)", i)
		}
		if turn.AdmittedAt.Before(t.CreatedAt) || turn.AdmittedAt.After(t.LastTurnAt) {
			return fmt.Errorf("thread turn %d admitted_at is outside created_at..last_turn_at", i)
		}
	}
	if t.EarlierTurnsCursor == "" && !t.Turns[0].AdmittedAt.Equal(t.CreatedAt) {
		return fmt.Errorf("thread created_at must be the first turn's admitted_at on the page that reaches it (no earlier_turns_cursor)")
	}
	return nil
}

// Summary is the thread as the list names it.
func (t ContextFabricThread) Summary() ContextFabricThreadSummary {
	return ContextFabricThreadSummary{
		ThreadID: t.ThreadID, RetentionDays: t.RetentionDays, TurnCount: t.TurnCount,
		CreatedAt: t.CreatedAt, LastTurnAt: t.LastTurnAt, ExpiresAt: t.ExpiresAt,
	}
}

// contextFabricThreadTurnBefore is the turn order: admitted_at, then
// submission_id; strict, since a submission id appears once.
func contextFabricThreadTurnBefore(a, b ContextFabricThreadTurn) bool {
	if !a.AdmittedAt.Equal(b.AdmittedAt) {
		return a.AdmittedAt.Before(b.AdmittedAt)
	}
	return a.SubmissionID < b.SubmissionID
}

// ContextFabricThreadList is the body of GET /api/v1/context-fabric/threads:
// one page of the caller's own threads, newest last_turn_at first, then
// thread_id.
type ContextFabricThreadList struct {
	SchemaVersion string                       `json:"schema_version"`
	Threads       []ContextFabricThreadSummary `json:"threads"`
	// NextCursor is present when more threads follow this page; pass it back
	// as the cursor query parameter. Opaque.
	NextCursor string `json:"next_cursor,omitempty"`
}

// ValidContextFabricThreadCursor reports whether value has the cursor
// grammar of next_cursor and earlier_turns_cursor: 1 to 512 URL-safe base64
// characters, no padding.
func ValidContextFabricThreadCursor(value string) bool {
	if len(value) < 1 || len(value) > ContextFabricThreadCursorMaxLength {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// Validate holds the list to its schema.
func (l ContextFabricThreadList) Validate() error {
	switch {
	case l.SchemaVersion != ContextFabricThreadListSchema:
		return fmt.Errorf("thread list schema_version must be %q", ContextFabricThreadListSchema)
	case l.Threads == nil:
		return fmt.Errorf("thread list threads is required (empty, not absent)")
	case len(l.Threads) > ContextFabricThreadListMaxLimit:
		return fmt.Errorf("thread list holds more than %d threads", ContextFabricThreadListMaxLimit)
	case l.NextCursor != "" && !ValidContextFabricThreadCursor(l.NextCursor):
		return fmt.Errorf("thread list next_cursor violates v1 bounds")
	case l.NextCursor != "" && len(l.Threads) == 0:
		return fmt.Errorf("thread list names a next page after an empty one")
	}
	seen := make(map[string]struct{}, len(l.Threads))
	for i, summary := range l.Threads {
		if err := summary.Validate(); err != nil {
			return fmt.Errorf("thread list entry %d: %w", i, err)
		}
		if _, dup := seen[summary.ThreadID]; dup {
			return fmt.Errorf("thread list entry %d repeats a thread_id", i)
		}
		seen[summary.ThreadID] = struct{}{}
		if i > 0 && !contextFabricThreadListBefore(l.Threads[i-1], summary) {
			return fmt.Errorf("thread list entry %d is out of order (last_turn_at newest first, then thread_id)", i)
		}
	}
	return nil
}

// contextFabricThreadListBefore is the list order: last_turn_at newest
// first, then thread_id ascending.
func contextFabricThreadListBefore(a, b ContextFabricThreadSummary) bool {
	if !a.LastTurnAt.Equal(b.LastTurnAt) {
		return a.LastTurnAt.After(b.LastTurnAt)
	}
	return a.ThreadID < b.ThreadID
}
