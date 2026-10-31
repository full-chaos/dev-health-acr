package contextfabric

import (
	"context"
	"errors"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The run lease and its event log (CHAOS-11949, plan §2.7.9, INV-7). With
// ACR_CONTEXT_FABRIC_RUN_LEASE_ENABLED on, a streamed run admitted under a
// client-named submission is owned by ACR, not by its connection:
//
//   - Owner: the replica that admitted it, under a lease on the submission
//     record. Admission writes the lease (RunLeaseGrant) in its own
//     transaction; the holder renews it every RunLeaseRenewInterval.
//   - Detach: a disconnect no longer cancels the run. The run keeps writing
//     its events to the log; the client attaches again with Last-Event-ID,
//     from either replica.
//   - Cancel: an explicit owner-checked request records t_req
//     (RequestRunCancel); the holder sees it (at once on its own replica,
//     within RunCancelPollInterval on another), cancels the run's context and
//     logs t_eff. The run's terminal is error{code: canceled}.
//   - Pod loss: a running record whose lease ended is taken over by another
//     replica (ReapLostRuns): it becomes abandoned and the log gets the
//     run_lost terminal, exactly once.
//   - Event log: every non-terminal event of the stream verbatim, bounded to
//     the latest RunEventLogMaxEvents of a run, plus one terminal marker by
//     reference (a result id, "unsaved", or an error.v1 envelope), never the
//     result content.

// Run lease timing.
const (
	// RunLeaseTTL is how long a lease holds without a renewal.
	RunLeaseTTL = 15 * time.Second
	// RunLeaseRenewInterval is how often the holder renews its lease.
	RunLeaseRenewInterval = 5 * time.Second
	// RunCancelPollInterval is how often the holder reads the record for a
	// cancel requested on another replica. With the same-replica fast path
	// it keeps t_eff - t_req inside RunCancelEffectiveBound.
	RunCancelPollInterval = time.Second
	// RunCancelEffectiveBound is the stated bound on t_eff - t_req (plan
	// §2.7.9).
	RunCancelEffectiveBound = 5 * time.Second
)

// RunEventLogMaxEvents is the per-run cap of the event log: the latest this
// many non-terminal events are kept; the terminal marker is always kept.
const RunEventLogMaxEvents = contractsv1.ContextFabricInvestigationRunEventLogMaxEvents

// RunLeaseGrant is the lease a new record is admitted under.
type RunLeaseGrant struct {
	// Holder is the admitting replica's instance id.
	Holder string
	// ExpiresAt is the end of the first lease term.
	ExpiresAt time.Time
	// RequestID is the run's request id, the request_id of every event of
	// its stream.
	RequestID string
}

// Valid reports whether the grant can be stored.
func (g RunLeaseGrant) Valid() bool {
	return len(g.Holder) >= 1 && len(g.Holder) <= 200 && len(g.RequestID) >= 1 && len(g.RequestID) <= 128 && !g.ExpiresAt.IsZero()
}

// RunKey names one run: its organization and the result id its submission
// reserved (unique per organization).
type RunKey struct {
	OrgID            string
	ReservedResultID string
}

// RunKeyOf is record's run.
func RunKeyOf(record SubmissionRecord) RunKey {
	return RunKey{OrgID: record.Key.OrgID, ReservedResultID: record.ReservedResultID}
}

// RunTerminalKind is the closed kind of a terminal marker.
type RunTerminalKind string

const (
	// RunTerminalResult: the run ended with a stored result; ResultID names
	// the final result (the reused one on answer reuse).
	RunTerminalResult RunTerminalKind = "result"
	// RunTerminalUnsaved: the run served an answer that was not stored.
	RunTerminalUnsaved RunTerminalKind = "unsaved"
	// RunTerminalError: the run ended with an error.v1 envelope (its own
	// failure, canceled, or run_lost).
	RunTerminalError RunTerminalKind = "error"
)

// RunTerminal is the terminal marker of a run.
type RunTerminal struct {
	Kind     RunTerminalKind
	ResultID string
	Error    *contractsv1.ErrorEnvelope
}

// RunEvent is one row of the event log.
type RunEvent struct {
	Seq  int64
	Type contractsv1.ContextFabricInvestigationEventType
	// LastStageStarted is the last stage started as of this event, "" when
	// none started.
	LastStageStarted contractsv1.ContextFabricInvestigationStage
	// Data is a non-terminal event's data payload, byte for byte as the
	// stream wrote it.
	Data []byte
	// Terminal is set on the terminal row only.
	Terminal *RunTerminal
	// WrittenAt is when the row was written.
	WrittenAt time.Time
}

// RunEventPage is one read of the log after a cursor, with the state the
// attach needs to decide the cursor and to end the stream.
type RunEventPage struct {
	// Events are the rows with seq above the cursor, in seq order.
	Events []RunEvent
	// FirstSeq and LastSeq bound the rows the log still holds, terminal
	// included; both 0 when the log is empty.
	FirstSeq, LastSeq int64
	// TerminalSeq is the terminal row's seq, 0 when there is none yet.
	TerminalSeq int64
	// LastStageStarted is that of the latest row ("" when empty).
	LastStageStarted contractsv1.ContextFabricInvestigationStage
	// Record is the submission record as stored (not reconciled).
	Record SubmissionRecord
}

// RunLeaseCheck is what the holder learns when it renews or polls.
type RunLeaseCheck struct {
	// Found is false when the run has no record (removed).
	Found bool
	// State and Holder are the record's, after the renewal.
	State  SubmissionState
	Holder string
	// Renewed: this call moved the lease to renewUntil.
	Renewed bool
	// CancelRequestedAt is t_req, zero when no cancel was requested.
	CancelRequestedAt time.Time
}

// Held reports whether the record is running under holder's lease.
func (c RunLeaseCheck) Held(holder string) bool {
	return c.Found && c.State == SubmissionRunning && c.Holder != "" && c.Holder == holder
}

// LostRun is a run whose lease ended and that a replica took over.
type LostRun struct {
	Run              RunKey
	RequestID        string
	TerminalSeq      int64
	PreviousHolder   string
	LeaseExpiredAt   time.Time
	TerminalAppended bool
}

// ErrRunEventSeq is an append whose seq is not the next one of the log.
var ErrRunEventSeq = errors.New("context fabric: run event seq is not the next one")

// RunStore persists the run lease and the event log. It is the submission
// store's own (the lease is a column of the record, the log references it).
// Every method takes the caller's clock.
type RunStore interface {
	// CheckRunLease reads the lease of a run. With renewUntil non-zero it
	// first renews the lease to it, only while the record is running and
	// holder is its lease_holder (Renewed reports it).
	CheckRunLease(ctx context.Context, run RunKey, holder string, renewUntil time.Time, now time.Time) (RunLeaseCheck, error)
	// RequestRunCancel records t_req = now on the key's record when it is
	// running under a lease and no cancel was requested yet, and returns
	// the record (reconciled, ThreadClosed set). A repeat keeps the first
	// t_req. ErrSubmissionNotFound for an unknown key.
	RequestRunCancel(ctx context.Context, key SubmissionKey, now time.Time) (SubmissionRecord, error)
	// AppendRunEvent writes one row. A non-terminal row must take the next
	// seq (ErrRunEventSeq otherwise) and prunes the run's non-terminal rows
	// older than the latest maxEvents. A terminal row is written only when
	// the run has none: appended is false when it already had one.
	AppendRunEvent(ctx context.Context, run RunKey, event RunEvent, maxEvents int, now time.Time) (appended bool, err error)
	// ReadRunEvents reads up to limit rows with seq above afterSeq, and the
	// page state.
	ReadRunEvents(ctx context.Context, run RunKey, afterSeq int64, limit int) (RunEventPage, error)
	// RunTerminalOf reads the terminal row of a run, nil when none.
	RunTerminalOf(ctx context.Context, run RunKey) (*RunEvent, error)
	// ReapLostRuns takes over running records whose lease ended at or before
	// now and whose reserved result is not stored: each becomes abandoned
	// with lease_holder = taker, and its log gets the run_lost terminal
	// (lostTerminal(requestID)) when it has none, in one transaction per
	// record. With only set, only that run is considered. Two replicas
	// reaping at once take each record once.
	ReapLostRuns(ctx context.Context, taker string, only *RunKey, lostTerminal func(requestID string) contractsv1.ErrorEnvelope, now time.Time) ([]LostRun, error)
}
