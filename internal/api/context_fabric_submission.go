package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Submission admission on the investigation route (CHAOS-11912, plan
// §2.7.5, INV-2). A request with X-ACR-Submission-ID is one user submit: its
// key (organization, owner, submission id) is admitted once, the run saves
// under the result id the record reserved, and every repeat of the key is
// answered from the record without a second execution.

// ContextFabricSubmissionLogMessage is the admission decision line: one per
// request that names a submission, before anything runs or is served.
const ContextFabricSubmissionLogMessage = "context fabric investigation submission"

// ContextFabricSubmissionCompletionLogMessage is the line that records how an
// admitted run's record reached its final state.
const ContextFabricSubmissionCompletionLogMessage = "context fabric investigation submission completion"

// SubmissionAdmissionOutcome is the closed outcome of the admission line.
type SubmissionAdmissionOutcome string

const (
	SubmissionOutcomeAdmitted          SubmissionAdmissionOutcome = "admitted"
	SubmissionOutcomeReplayed          SubmissionAdmissionOutcome = "replayed"
	SubmissionOutcomeRefusedMismatch   SubmissionAdmissionOutcome = "refused_mismatch"
	SubmissionOutcomeRefusedInProgress SubmissionAdmissionOutcome = "refused_in_progress"
	SubmissionOutcomeRefusedUnsaved    SubmissionAdmissionOutcome = "refused_unsaved"
	SubmissionOutcomeRefusedAbandoned  SubmissionAdmissionOutcome = "refused_abandoned"
	SubmissionOutcomeStoreUnavailable  SubmissionAdmissionOutcome = "store_unavailable"
	SubmissionOutcomeOwnerUnknown      SubmissionAdmissionOutcome = "owner_unknown"
)

// SubmissionAdmissionOutcomeVocabulary is the closed set of outcomes.
func SubmissionAdmissionOutcomeVocabulary() [8]SubmissionAdmissionOutcome {
	return [8]SubmissionAdmissionOutcome{
		SubmissionOutcomeAdmitted, SubmissionOutcomeReplayed, SubmissionOutcomeRefusedMismatch, SubmissionOutcomeRefusedInProgress,
		SubmissionOutcomeRefusedUnsaved, SubmissionOutcomeRefusedAbandoned, SubmissionOutcomeStoreUnavailable, SubmissionOutcomeOwnerUnknown,
	}
}

// submissionCompletionAbandonedOnError is the completion of a run that ended
// with an error: AbandonSubmission, not FinishSubmission, decided it.
const submissionCompletionAbandonedOnError = "abandoned_on_error"

// admittedInvestigationSubmission is the record this request admitted.
type admittedInvestigationSubmission struct {
	key    contextfabric.SubmissionKey
	record contextfabric.SubmissionRecord
	store  contextfabric.SubmissionStore
}

// investigationSubmissionID reads X-ACR-Submission-ID. Absent is ("", true);
// one well-formed value is (value, true); anything else is ("", false).
func investigationSubmissionID(r *http.Request) (string, bool) {
	return investigationCorrelationHeader(r, contractsv1.ContextFabricSubmissionIDHeader)
}

// investigationThreadID reads X-ACR-Thread-ID by the same rule.
func investigationThreadID(r *http.Request) (string, bool) {
	return investigationCorrelationHeader(r, contractsv1.ContextFabricThreadIDHeader)
}

func investigationCorrelationHeader(r *http.Request, header string) (string, bool) {
	values := r.Header.Values(header)
	switch {
	case len(values) == 0:
		return "", true
	case len(values) > 1 || !contextfabric.ValidSubmissionID(values[0]):
		return "", false
	}
	return values[0], true
}

func (a *App) investigationSubmissions() contextfabric.SubmissionStore {
	if a.runtime == nil || storage.IsNil(a.runtime.InvestigationSubmissions) {
		return nil
	}
	return a.runtime.InvestigationSubmissions
}

func (a *App) reserveResultID() string {
	if a.runtime != nil && a.runtime.NewResultID != nil {
		return a.runtime.NewResultID()
	}
	return contextfabric.NewInvestigationResultID()
}

// submissionExpiry is the record's abandon time: the end of this request's
// run budget (the deadline timeoutMiddleware set, narrowed by the wait
// budget) plus SubmissionAbandonGrace, on the App's clock.
func (a *App) submissionExpiry(ctx context.Context) time.Time {
	budget := a.config.RequestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline)
	}
	if budget < 0 {
		budget = 0
	}
	return a.now().Add(budget + contextfabric.SubmissionAbandonGrace)
}

func (a *App) logSubmissionAdmission(r *http.Request, principal storage.Principal, outcome SubmissionAdmissionOutcome, state contextfabric.SubmissionState, err error) {
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"outcome", string(outcome),
		"state", string(state),
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
	}
	if err != nil {
		args = append(args, "error_class", submissionStoreErrorClass(err))
		a.logger.WarnContext(r.Context(), ContextFabricSubmissionLogMessage, args...)
		return
	}
	a.logger.InfoContext(r.Context(), ContextFabricSubmissionLogMessage, args...)
}

func submissionStoreErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, contextfabric.ErrSubmissionOwnerUnknown):
		return "owner_unknown"
	default:
		return "store_error"
	}
}

// admitInvestigationSubmission admits the request's submission, or answers a
// repeat of its key. proceed is true only for a newly admitted record; every
// other path has written the response.
func (a *App) admitInvestigationSubmission(w http.ResponseWriter, r *http.Request, principal storage.Principal, request contextfabric.InvestigationRequest, submissionID, threadID string) (*admittedInvestigationSubmission, bool) {
	store := a.investigationSubmissions()
	if store == nil {
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeStoreUnavailable, "", nil)
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation submissions are not available", true, nil)
		return nil, false
	}
	key, err := contextfabric.SubmissionKeyFor(principal, submissionID)
	if err != nil {
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeOwnerUnknown, "", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation submission could not be admitted", false, nil)
		return nil, false
	}
	digest, err := contextfabric.SubmissionRequestDigest(request)
	if err != nil {
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeStoreUnavailable, "", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation submission could not be admitted", false, nil)
		return nil, false
	}
	record, admitted, err := store.AdmitSubmission(r.Context(), contextfabric.SubmissionAdmission{
		Key: key, RequestDigest: digest, ReservedResultID: a.reserveResultID(), ThreadID: threadID, ExpiresAt: a.submissionExpiry(r.Context()),
	}, a.now())
	if err != nil {
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeStoreUnavailable, "", err)
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation submissions are temporarily unavailable", true, nil)
		return nil, false
	}
	if admitted {
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeAdmitted, record.State, nil)
		return &admittedInvestigationSubmission{key: key, record: record, store: store}, true
	}
	// A repeat that raced the pre-gate lookup (answerSubmissionRepeat): the
	// other request admitted the key between the two reads.
	a.answerSubmissionRecord(w, r, principal, record, digest)
	return nil, false
}

// answerSubmissionRepeat answers a request whose submission key is already
// admitted, BEFORE the service-model gate (round-3 F1): a repeat runs no
// model, so the decision to execute on the deployment's model does not apply
// to it; the stored-result gate re-authorizes a replay like a read by id.
// answered is false when the key is new (or the lookup cannot be made here;
// admission then decides and answers any failure).
func (a *App) answerSubmissionRepeat(w http.ResponseWriter, r *http.Request, principal storage.Principal, request contextfabric.InvestigationRequest, submissionID string) (answered bool) {
	store := a.investigationSubmissions()
	if store == nil {
		return false
	}
	key, err := contextfabric.SubmissionKeyFor(principal, submissionID)
	if err != nil {
		return false
	}
	record, err := store.GetSubmission(r.Context(), key, a.now())
	switch {
	case errors.Is(err, contextfabric.ErrSubmissionNotFound):
		return false
	case err != nil:
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeStoreUnavailable, "", err)
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation submissions are temporarily unavailable", true, nil)
		return true
	}
	digest, err := contextfabric.SubmissionRequestDigest(request)
	if err != nil {
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeStoreUnavailable, "", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation submission could not be admitted", false, nil)
		return true
	}
	a.answerSubmissionRecord(w, r, principal, record, digest)
	return true
}

// answerSubmissionRecord is the one answer to a repeat of an admitted key.
// The digest is decided before the state: a key is only ever a retry of the
// identical request. Nothing here executes.
func (a *App) answerSubmissionRecord(w http.ResponseWriter, r *http.Request, principal storage.Principal, record contextfabric.SubmissionRecord, digest string) {
	if record.RequestDigest != digest {
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeRefusedMismatch, record.State, nil)
		writeError(w, r, http.StatusConflict, contractsv1.ErrorCodeSubmissionMismatch, "The submission id was already used for a different request", false, nil)
		return
	}
	switch record.State {
	case contextfabric.SubmissionDone:
		// Re-authorized: the stored result is read and decided by the gate
		// like any read by id, never served from the record alone.
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeReplayed, record.State, nil)
		results := a.investigationResults()
		if results == nil {
			a.handleRuntimeUnavailable(w, r)
			break
		}
		a.serveStoredInvestigationResult(w, r, principal, results, a.storedResultGate(), record.ResultID, "context_fabric_investigation_submission_replayed")
	case contextfabric.SubmissionRunning:
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeRefusedInProgress, record.State, nil)
		w.Header().Set("Retry-After", strconv.Itoa(contractsv1.ContextFabricSubmissionInProgressRetryAfterSeconds))
		writeError(w, r, http.StatusConflict, contractsv1.ErrorCodeSubmissionInProgress, "The submission is still running", true, nil)
	case contextfabric.SubmissionDoneUnsaved:
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeRefusedUnsaved, record.State, nil)
		writeError(w, r, http.StatusConflict, contractsv1.ErrorCodeSubmissionUnsaved, "The submission was answered but its answer was not saved", false, nil)
	default:
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeRefusedAbandoned, record.State, nil)
		writeError(w, r, http.StatusConflict, contractsv1.ErrorCodeSubmissionAbandoned, "The submission ended without an answer", false, nil)
	}
}

// submissionWriteContext outlives the request: a client that disconnected
// must still leave its record final.
func submissionWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), contextfabric.SubmissionWriteTimeout)
}

// submissionReservationUnused is the completion line's flag for a served
// answer that is neither the reserved id nor a reused answer: the run saved
// (or served) under an id admission did not reserve, so the answer is not
// bound to its submission. It is a defect, logged at Error.
const submissionReservationUnused = "reservation_unused"

func (a *App) logSubmissionCompletion(r *http.Request, principal storage.Principal, completion string, state contextfabric.SubmissionState, servedIsReserved, reused bool, err error) {
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"completion", completion,
		"state", string(state),
		"served_is_reserved", servedIsReserved,
		"reused", reused,
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
	}
	switch {
	case err != nil:
		args = append(args, "error_class", submissionStoreErrorClass(err))
		a.logger.ErrorContext(r.Context(), ContextFabricSubmissionCompletionLogMessage, args...)
	case completion != submissionCompletionAbandonedOnError && !servedIsReserved && !reused:
		args = append(args, "defect", submissionReservationUnused)
		a.logger.ErrorContext(r.Context(), ContextFabricSubmissionCompletionLogMessage, args...)
	default:
		a.logger.InfoContext(r.Context(), ContextFabricSubmissionCompletionLogMessage, args...)
	}
}

// finishInvestigationSubmission makes an admitted record final after its run
// served servedResultID, before the response is written. The save
// transaction normally made it done already (already_final); a reused answer
// makes it done with the reused id (served_stored); an answer that was not
// stored makes it done_unsaved (served_unsaved). A failure here is logged at
// Error and the answer is still served: the sweeper and the read path
// reconcile a record left running.
func (a *App) finishInvestigationSubmission(r *http.Request, principal storage.Principal, submission *admittedInvestigationSubmission, result contextfabric.InvestigationResult) {
	if submission == nil {
		return
	}
	ctx, cancel := submissionWriteContext(r.Context())
	defer cancel()
	record, completion, err := submission.store.FinishSubmission(ctx, submission.key, result.ResultID, a.now())
	a.logSubmissionCompletion(r, principal, string(completion), record.State, result.ResultID == submission.record.ReservedResultID, result.Reused, err)
}

// abandonInvestigationSubmission ends an admitted record whose run returned
// an error. A record the run's save already completed is left done.
func (a *App) abandonInvestigationSubmission(r *http.Request, principal storage.Principal, submission *admittedInvestigationSubmission) {
	if submission == nil {
		return
	}
	ctx, cancel := submissionWriteContext(r.Context())
	defer cancel()
	record, err := submission.store.AbandonSubmission(ctx, submission.key, a.now())
	a.logSubmissionCompletion(r, principal, submissionCompletionAbandonedOnError, record.State, false, false, err)
}
