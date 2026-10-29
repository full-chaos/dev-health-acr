package contextfabric

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Submission identity (CHAOS-11912, plan §2.7.5, INV-2): one user submit is
// one execution. The client names each submit with a submission id; the key
// (organization, owner, submission id) is unique, and the insert that admits
// it decides every race. The record is bound to the request's semantic
// digest, so a key can only be reused to retry the identical request, and no
// state lets the same key execute twice.
//
// The owner is the principal that admitted the submission. A result that
// completes a submission carries that owner, and StoredResultGate.Authorize
// serves it to that owner only (INV-1), on every surface.

// SubmissionState is the closed state of a submission record.
type SubmissionState string

const (
	// SubmissionRunning: the execution is live (or died with its pod and
	// has not yet been reconciled).
	SubmissionRunning SubmissionState = "running"
	// SubmissionDone: the final result is saved; ResultID names it. On
	// answer reuse it is the reused result's id.
	SubmissionDone SubmissionState = "done"
	// SubmissionDoneUnsaved: a valid answer was served but not stored, so
	// there is no result id to replay.
	SubmissionDoneUnsaved SubmissionState = "done_unsaved"
	// SubmissionAbandoned: the execution ended without a result, or ran past
	// its budget plus SubmissionAbandonGrace.
	SubmissionAbandoned SubmissionState = "abandoned"
)

// SubmissionStateVocabulary is the closed set of states.
func SubmissionStateVocabulary() [4]SubmissionState {
	return [4]SubmissionState{SubmissionRunning, SubmissionDone, SubmissionDoneUnsaved, SubmissionAbandoned}
}

// SubmissionAbandonGrace is how long past its run budget a running record is
// still considered live. After it, a read or the sweeper abandons it.
const SubmissionAbandonGrace = 30 * time.Second

// ErrSubmissionNotFound is a GetSubmission that found no record for the key.
var ErrSubmissionNotFound = errors.New("context fabric: submission not found")

// ErrSubmissionOwnerUnknown: the principal carries no subject, so no owner
// can be recorded. Validated authentication always sets one; this refuses a
// principal built without it rather than binding a result to an empty owner.
var ErrSubmissionOwnerUnknown = errors.New("context fabric: submission owner has no subject")

// SubmissionOwner is the identity a submission and its result are bound to:
// the authentication boundary and its subject (storage.Principal.AuditActor).
// A web user is its assertion subject; a credential is its credential (or
// workload binding) id.
type SubmissionOwner struct {
	AuthenticationMethod string
	Subject              string
}

// SubmissionOwnerOf is principal's owner identity.
func SubmissionOwnerOf(principal storage.Principal) (SubmissionOwner, error) {
	method, subject := principal.AuditActor()
	if subject == "" {
		return SubmissionOwner{}, ErrSubmissionOwnerUnknown
	}
	return SubmissionOwner{AuthenticationMethod: method, Subject: subject}, nil
}

// Is reports whether principal is this owner.
func (o SubmissionOwner) Is(principal storage.Principal) bool {
	method, subject := principal.AuditActor()
	return subject != "" && o.AuthenticationMethod == method && o.Subject == subject
}

// SubmissionKey is the unique key of a submission record.
type SubmissionKey struct {
	OrgID        string
	Owner        SubmissionOwner
	SubmissionID string
}

// SubmissionKeyFor builds the key of submissionID for principal.
func SubmissionKeyFor(principal storage.Principal, submissionID string) (SubmissionKey, error) {
	owner, err := SubmissionOwnerOf(principal)
	if err != nil {
		return SubmissionKey{}, err
	}
	return SubmissionKey{OrgID: principal.OrgID, Owner: owner, SubmissionID: submissionID}, nil
}

// ValidSubmissionID reports whether id is a well-formed submission id: the
// client correlation id grammar the event stream contract publishes (the
// `accepted` event echoes the id). The database holds the same pattern as a
// CHECK.
func ValidSubmissionID(id string) bool {
	return contractsv1.ValidContextFabricClientCorrelationID(id)
}

// SubmissionRecord is one stored submission.
type SubmissionRecord struct {
	Key              SubmissionKey
	RequestDigest    string
	ReservedResultID string
	State            SubmissionState
	// ResultID is the final result id, set only in state done. It equals
	// ReservedResultID unless the run served a reused answer.
	ResultID string
	// ThreadID is the X-ACR-Thread-ID the submission was admitted with, ""
	// when it named none (CHAOS-11913). It is not part of the digest: a
	// retry of the key keeps the first admission's thread.
	ThreadID   string
	AdmittedAt time.Time
	UpdatedAt  time.Time
	// ExpiresAt is the admission time plus the run budget plus
	// SubmissionAbandonGrace: a record still running at it is abandoned.
	ExpiresAt time.Time
}

// SubmissionAdmission is what admission inserts.
type SubmissionAdmission struct {
	Key              SubmissionKey
	RequestDigest    string
	ReservedResultID string
	// ThreadID is optional; when set it follows ValidSubmissionID's grammar.
	ThreadID  string
	ExpiresAt time.Time
	// Limits are the shared limits a NEW record is admitted under
	// (CHAOS-11914); a repeat of an existing key is never refused by them.
	Limits SubmissionLimits
	// PlatformRun is true when the run uses the deployment's own model: it
	// then counts to the monthly allowance as well as to concurrency.
	PlatformRun bool
}

// SubmissionCompletion names how a served run's record reached its final
// state. It is the decision basis the admission line records.
type SubmissionCompletion string

const (
	// SubmissionCompletionAlreadyFinal: the record was already final when
	// the serve finished -- the save transaction completed it (done) or the
	// engine wrote done_unsaved where it skipped the save.
	SubmissionCompletionAlreadyFinal SubmissionCompletion = "already_final"
	// SubmissionCompletionServedStored: the served result id is stored but
	// did not complete the record in its save -- an answer reuse.
	SubmissionCompletionServedStored SubmissionCompletion = "served_stored"
	// SubmissionCompletionServedUnsaved: the served result id is not
	// stored; the record becomes done_unsaved.
	SubmissionCompletionServedUnsaved SubmissionCompletion = "served_unsaved"
	// SubmissionCompletionNotRunning: the record was not running and is
	// left as it is (an abandoned record stays abandoned).
	SubmissionCompletionNotRunning SubmissionCompletion = "not_running"
)

// SubmissionCompletionVocabulary is the closed set of completions.
func SubmissionCompletionVocabulary() [4]SubmissionCompletion {
	return [4]SubmissionCompletion{SubmissionCompletionAlreadyFinal, SubmissionCompletionServedStored, SubmissionCompletionServedUnsaved, SubmissionCompletionNotRunning}
}

// SubmissionSweep counts what one sweep changed.
type SubmissionSweep struct {
	// Completed: running records whose reserved result is stored, made done.
	Completed int64
	// Abandoned: running records past ExpiresAt, made abandoned.
	Abandoned int64
}

// SubmissionStore persists submission records. Every method takes the
// caller's clock, so the expiry that admission computed and the reconcile
// that reads it use one time source.
//
// Reconcile rule, applied by AdmitSubmission (to the existing record),
// GetSubmission and SweepSubmissions: a running or abandoned record whose
// ReservedResultID is stored in the organization's results AND bound to the
// record's owner becomes done with that id; otherwise a running record at
// or past ExpiresAt becomes abandoned. A saved result always wins over the
// clock. Invariant: a done record names a result bound to its own owner --
// never another principal's result, never an unbound one (FinishSubmission
// and the replay completion keep it too).
//
// The atomic completion is not here: the result store's Save completes the
// running (or abandoned) record whose ReservedResultID is the saved result's
// id and whose owner is the saving principal, in the same statement as the
// result row, and records that owner on the row -- whatever the record's
// state, so a late save is never stored unbound.
type SubmissionStore interface {
	// AdmitSubmission inserts the record in state running. admitted is
	// true when this call inserted it; false returns the existing record
	// for the key, reconciled. The unique key decides every race: exactly
	// one concurrent admission of a key is admitted. A NEW record over a
	// shared limit is refused with *SubmissionRefusedError and nothing is
	// written; the count, the check, the record and the aggregate
	// increment are one transaction, serialized per organization.
	AdmitSubmission(ctx context.Context, admission SubmissionAdmission, now time.Time) (record SubmissionRecord, admitted bool, err error)
	// GetSubmission returns the key's record, reconciled, or
	// ErrSubmissionNotFound.
	GetSubmission(ctx context.Context, key SubmissionKey, now time.Time) (SubmissionRecord, error)
	// FinishSubmission is the serve path's backstop after a run served
	// servedResultID: a running record becomes done when the served id is
	// stored bound to the record's owner (its own save, or its own earlier
	// answer reused), and done_unsaved otherwise. A final record is not
	// changed.
	FinishSubmission(ctx context.Context, key SubmissionKey, servedResultID string, now time.Time) (SubmissionRecord, SubmissionCompletion, error)
	// AbandonSubmission ends a running record whose run ended without a
	// served result. A final record is not changed.
	AbandonSubmission(ctx context.Context, key SubmissionKey, now time.Time) (SubmissionRecord, error)
	// SweepSubmissions applies the reconcile rule to every running record.
	SweepSubmissions(ctx context.Context, now time.Time) (SubmissionSweep, error)
}

// SubmissionUnsavedMarker is the engine's write at the exit that serves an
// answer without storing it (saveTerminalResult's payload-rejected branch):
// the running record whose ReservedResultID is resultID, owned by principal,
// becomes done_unsaved. marked is false when no running record matched.
type SubmissionUnsavedMarker interface {
	MarkSubmissionUnsaved(ctx context.Context, principal storage.Principal, resultID string, now time.Time) (marked bool, err error)
}

// NewInvestigationResultID mints a result id of the shape the hosted engine
// mints: "result_" and 128 random bits in hex. crypto/rand.Read does not
// fail on a supported platform (it aborts the process instead).
func NewInvestigationResultID() string {
	buf := make([]byte, 16)
	//nolint:errcheck // see the doc comment: unreachable by construction.
	rand.Read(buf)
	return "result_" + hex.EncodeToString(buf)
}

// submissionDigestPrefix versions the digest's canonical form.
const submissionDigestPrefix = "context_fabric_submission_digest.v1\n"

// SubmissionRequestDigest is the semantic digest a submission is bound to:
// sha256 of the request's canonical JSON with the fields that do not change
// what is asked removed -- request_id (a per-attempt correlation id) and the
// consumer's name and version (who is asking). Everything else is included:
// the question, conversation, requested scope with its subject hints,
// parent_result_id, every prior_*_receipts namespace, expected kinds and
// handles, supplied interpretation and synthesis, synthesis mode, time
// context, options, and consumer.surface, which the engine reads as
// behaviour (the MCP-only calendar-window commit and reuse bypass, the
// explicit-structure authority, clarification capture), compared as the
// engine compares it, trimmed. Headers (wait budget, thread, correlation) are
// not part of the body and never reach it.
//
// encoding/json writes struct fields in declaration order and sorts map keys,
// so one decoded request has one encoding.
func SubmissionRequestDigest(request InvestigationRequest) (string, error) {
	semantic := request
	semantic.RequestID = ""
	semantic.Consumer = contractsv1.ContextFabricConsumerInfo{Surface: strings.TrimSpace(request.Consumer.Surface)}
	encoded, err := json.Marshal(semantic)
	if err != nil {
		return "", fmt.Errorf("context fabric: encode submission digest: %w", err)
	}
	sum := sha256.New()
	sum.Write([]byte(submissionDigestPrefix))
	sum.Write(encoded)
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}

// resultIDReservation is the result id admission reserved for one run. Every
// id the run mints is this id: a run serves one result, and an exit that
// discards a first result (a save-time supersession veto, say) and builds a
// second must still save under the reserved id, or the record could not be
// completed and the result would be saved unbound. Two different results
// saved under one id in one run fail the store's immutability check loudly
// instead.
type resultIDReservation struct {
	id    string
	mints atomic.Int64
}

type resultIDReservationKey struct{}

// WithReservedResultID carries the reserved result id of a submission into
// the run (plan §2.7.5: passed by ctx into the injected NewResultID).
func WithReservedResultID(ctx context.Context, resultID string) context.Context {
	if resultID == "" {
		return ctx
	}
	return context.WithValue(ctx, resultIDReservationKey{}, &resultIDReservation{id: resultID})
}

// ReservedResultID is the reserved result id ctx carries, if any. It does
// not mint.
func ReservedResultID(ctx context.Context) (string, bool) {
	reservation, ok := ctx.Value(resultIDReservationKey{}).(*resultIDReservation)
	if !ok || reservation == nil {
		return "", false
	}
	return reservation.id, true
}

// MintReservedResultID is what the engine's id mint does when ctx carries a
// reservation: it returns the reserved id and counts the mint. ok is false
// when ctx carries none. mints is this call's ordinal within the run.
func MintReservedResultID(ctx context.Context) (resultID string, mints int64, ok bool) {
	reservation, found := ctx.Value(resultIDReservationKey{}).(*resultIDReservation)
	if !found || reservation == nil {
		return "", 0, false
	}
	return reservation.id, reservation.mints.Add(1), true
}

// SubmissionTelemetryRecorder is the optional, decision-basis side of the
// engine's submission writes. SlogEngineTelemetry implements it.
type SubmissionTelemetryRecorder interface {
	// RecordReservedResultIDMintedAgain: a run minted its result id more
	// than once (mints >= 2), so two results of the run carry the reserved
	// id.
	RecordReservedResultIDMintedAgain(ctx context.Context, mints int64)
	// RecordSubmissionUnsaved: the outcome of the engine's done_unsaved write.
	RecordSubmissionUnsaved(ctx context.Context, principal storage.Principal, outcome SubmissionUnsavedOutcome, err error)
}

// SubmissionUnsavedOutcome is the closed outcome of the engine's
// done_unsaved write at the not-stored exit.
type SubmissionUnsavedOutcome string

const (
	SubmissionUnsavedMarked     SubmissionUnsavedOutcome = "marked"
	SubmissionUnsavedNotRunning SubmissionUnsavedOutcome = "not_running"
	SubmissionUnsavedNoMarker   SubmissionUnsavedOutcome = "no_marker"
	SubmissionUnsavedFailed     SubmissionUnsavedOutcome = "failed"
)

// SubmissionUnsavedOutcomeVocabulary is the closed set of outcomes.
func SubmissionUnsavedOutcomeVocabulary() [4]SubmissionUnsavedOutcome {
	return [4]SubmissionUnsavedOutcome{SubmissionUnsavedMarked, SubmissionUnsavedNotRunning, SubmissionUnsavedNoMarker, SubmissionUnsavedFailed}
}

// SubmissionWriteTimeout bounds a submission write made after the request
// context may have ended (a client that disconnected must still leave its
// record in a final state).
const SubmissionWriteTimeout = 5 * time.Second

// mintResultID is the engine's one result id mint. With a reservation in ctx
// every mint of the run is the reserved id; without one it is the injected
// generator. TestEveryResultIDMintHonoursTheReservation holds every mint
// site to this function.
func (e *Engine) mintResultID(ctx context.Context) string {
	if resultID, mints, ok := MintReservedResultID(ctx); ok {
		if mints > 1 {
			if recorder, isRecorder := e.telemetry.(SubmissionTelemetryRecorder); isRecorder {
				recorder.RecordReservedResultIDMintedAgain(ctx, mints)
			}
		}
		return resultID
	}
	return e.newResultID()
}

// markSubmissionUnsaved is the done_unsaved write where the save is skipped
// (plan §2.7.5). It never fails the served answer: the serve path's
// FinishSubmission backstop applies the same rule to a record still running.
func (e *Engine) markSubmissionUnsaved(ctx context.Context, principal storage.Principal, resultID string) {
	reserved, ok := ReservedResultID(ctx)
	if !ok || reserved != resultID {
		return
	}
	recorder, _ := e.telemetry.(SubmissionTelemetryRecorder)
	marker, ok := e.rawResults.(SubmissionUnsavedMarker)
	if !ok {
		if recorder != nil {
			recorder.RecordSubmissionUnsaved(ctx, principal, SubmissionUnsavedNoMarker, nil)
		}
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SubmissionWriteTimeout)
	defer cancel()
	marked, err := marker.MarkSubmissionUnsaved(writeCtx, principal, resultID, e.now())
	if recorder == nil {
		return
	}
	switch {
	case err != nil:
		recorder.RecordSubmissionUnsaved(ctx, principal, SubmissionUnsavedFailed, err)
	case marked:
		recorder.RecordSubmissionUnsaved(ctx, principal, SubmissionUnsavedMarked, nil)
	default:
		recorder.RecordSubmissionUnsaved(ctx, principal, SubmissionUnsavedNotRunning, nil)
	}
}

// SubmissionUnsavedLogMessage and ReservedResultIDMintedAgainLogMessage are
// the engine's submission lines.
const (
	SubmissionUnsavedLogMessage           = "context fabric submission unsaved"
	ReservedResultIDMintedAgainLogMessage = "context fabric reserved result id minted again"
)

func (t SlogEngineTelemetry) RecordReservedResultIDMintedAgain(ctx context.Context, mints int64) {
	args := []any{"mints", mints}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, ReservedResultIDMintedAgainLogMessage, args...)
}

func (t SlogEngineTelemetry) RecordSubmissionUnsaved(ctx context.Context, principal storage.Principal, outcome SubmissionUnsavedOutcome, err error) {
	args := []any{"org_id", SanitizeLogAttr(principal.OrgID), "outcome", string(outcome)}
	if err != nil {
		args = append(args, "error_class", SanitizeLogAttr(submissionErrorClass(err)))
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	if outcome == SubmissionUnsavedFailed || outcome == SubmissionUnsavedNoMarker {
		t.logger.WarnContext(ctx, SubmissionUnsavedLogMessage, args...)
		return
	}
	t.logger.InfoContext(ctx, SubmissionUnsavedLogMessage, args...)
}

// submissionErrorClass names a store failure without its text.
func submissionErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "store_error"
	}
}
