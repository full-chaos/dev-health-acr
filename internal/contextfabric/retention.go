package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Ask Dev retention (CHAOS-11952, plan §2.6 and D-22; contract
// docs/design/ask-dev-conversation-contract.md §2 and §6). The backing set of
// an Ask Dev result is its thread row, its submission records (with the
// questions), the result rows (semantic state and reuse columns are columns
// of the row), the receipt selections and structure supersession claims made
// on those results, the run event log and the answer feedback. Retention
// removes it:
//
//   - an expired thread (expires_at = last turn + 24 h for retention 0, or
//     + 30 days for 30): the whole set, thread row included;
//   - a deleted thread not yet expired (lead ruling Q1: the delete is the
//     user's act, retention is a ceiling, not a floor): on the first pass
//     after closed_at its content goes (results, feedback, selections,
//     claims, event log, the records' result ids), and the content-free
//     closure record (thread row, submission ids and digests) stays until
//     expires_at (design §6 T1), then goes like an expired thread;
//   - a record with no thread (lead ruling Q2): a one-turn thread with
//     retention 0, removed with its result from its admission + 24 h.
//
// Kept: a result a live record still names (T3); every record whose run can
// still save (T2: running, or abandoned until RetentionSaveWindow past its run
// deadline), and the unit it belongs to until then; results no record names
// (MCP, Workbench); the content-free monthly usage aggregate (§2.9, AC-6);
// model execution receipts, which carry no question, answer, principal or
// result id (lead ruling Q3 test). An organization purge removes the whole
// set (orgpurge).
//
// Each unit (one thread, or one record with no thread) is removed in one
// transaction, so no read ever sees a result live after the thread or
// record that closed it is gone (lead ruling Q4).

// RetentionSaveWindow is how long past its run deadline an abandoned record
// is still held for a late save (design §6 T2). A run's work ends at its
// deadline; a save already in flight then is bounded by its statement, far
// inside this window. Retention keeps the record, and the unit it belongs
// to, until the window has passed.
const RetentionSaveWindow = time.Hour

// RetentionSweepBatch is the default number of units one pass handles.
const RetentionSweepBatch = 500

// RetentionSweep counts one retention pass.
type RetentionSweep struct {
	// ThreadsRemoved is the expired threads whose whole backing set went.
	ThreadsRemoved int
	// ThreadsErased is the deleted, unexpired threads whose content went
	// (their closure record stays until expiry).
	ThreadsErased int
	// RecordsRemoved is the records with no thread that went.
	RecordsRemoved int
	// ResultsRemoved is the result rows that went, across every unit.
	ResultsRemoved int
	// Fenced is the units left for a later pass because a record of theirs
	// can still save (T2).
	Fenced int
}

// Changed reports whether the pass removed or erased anything.
func (s RetentionSweep) Changed() bool {
	return s.ThreadsRemoved > 0 || s.ThreadsErased > 0 || s.RecordsRemoved > 0 || s.ResultsRemoved > 0
}

// RetentionSweeper is the retention job's store side. SweepRetention handles
// up to limit units due at now, each in its own transaction, and is safe to
// run from every replica at once: a unit another pass already removed is a
// no-op.
type RetentionSweeper interface {
	SweepRetention(ctx context.Context, now time.Time, limit int) (RetentionSweep, error)
}

// RetentionFenced reports whether a record can still save at now, so
// retention must keep it (design §6 T2): a running or abandoned record until
// RetentionSaveWindow past its run deadline (ExpiresAt). A record still
// running past its deadline is abandoned by the reconcile rule (its run ended
// with its pod), so the window runs from the deadline for both.
func RetentionFenced(record SubmissionRecord, now time.Time) bool {
	switch record.State {
	case SubmissionRunning, SubmissionAbandoned:
		return now.Before(record.ExpiresAt.Add(RetentionSaveWindow))
	}
	return false
}

// ErasedReservedResultIDPrefix starts the reserved result id an erased record
// keeps (design §6 T1: the closure record holds no result id). The rest is
// the hex SHA-256 of the erased id, unique per organization as the id was. It
// is not a result id: no result is ever saved under it, and no reserved save
// can name it (a reserved id is a minted "result_" id).
const ErasedReservedResultIDPrefix = "erased_"

// ErasedReservedResultID is the id an erased record keeps in place of
// reserved (the store's SQL computes the same value).
func ErasedReservedResultID(reserved string) string {
	sum := sha256.Sum256([]byte(reserved))
	return ErasedReservedResultIDPrefix + hex.EncodeToString(sum[:])
}

// IsErasedReservedResultID reports whether id is an erased record's id.
func IsErasedReservedResultID(id string) bool {
	return strings.HasPrefix(id, ErasedReservedResultIDPrefix)
}
