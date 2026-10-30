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
	// CHAOS-11914: a new submission over a shared limit.
	SubmissionOutcomeRefusedPrincipalConcurrency SubmissionAdmissionOutcome = "refused_concurrency_principal"
	SubmissionOutcomeRefusedOrgConcurrency       SubmissionAdmissionOutcome = "refused_concurrency_org"
	SubmissionOutcomeRefusedAllowance            SubmissionAdmissionOutcome = "refused_allowance_exhausted"
	// CHAOS-11997: a create without X-ACR-Submission-ID on the deployment's
	// model, admitted under a server-minted key.
	SubmissionOutcomeAdmittedImplicit SubmissionAdmissionOutcome = "admitted_implicit"
)

// SubmissionAdmissionOutcomeVocabulary is the closed set of outcomes.
func SubmissionAdmissionOutcomeVocabulary() [13]SubmissionAdmissionOutcome {
	return [13]SubmissionAdmissionOutcome{
		SubmissionOutcomeAdmitted, SubmissionOutcomeReplayed, SubmissionOutcomeRefusedMismatch, SubmissionOutcomeRefusedInProgress,
		SubmissionOutcomeRefusedUnsaved, SubmissionOutcomeRefusedAbandoned, SubmissionOutcomeStoreUnavailable, SubmissionOutcomeOwnerUnknown,
		SubmissionOutcomeRefusedHeader,
		SubmissionOutcomeRefusedPrincipalConcurrency, SubmissionOutcomeRefusedOrgConcurrency, SubmissionOutcomeRefusedAllowance,
		SubmissionOutcomeAdmittedImplicit,
	}
}

// submissionCompletionAbandonedOnError is the completion of a run that ended
// with an error: AbandonSubmission, not FinishSubmission, decided it.
const submissionCompletionAbandonedOnError = "abandoned_on_error"

// submissionCompletionImplicitReleased is the completion of an implicit
// admission's run that served an answer (CHAOS-11997): the record leaves
// running (done_unsaved: it names no result, by design) and frees its
// concurrency slot. It is not a defect that the served id is not the
// reserved one: the reservation is never handed to the engine.
const submissionCompletionImplicitReleased = "implicit_released"

// admittedInvestigationSubmission is the record this request admitted.
type admittedInvestigationSubmission struct {
	key    contextfabric.SubmissionKey
	record contextfabric.SubmissionRecord
	store  contextfabric.SubmissionStore
	// implicit is an admission of a create without X-ACR-Submission-ID
	// (CHAOS-11997): it counts against the shared limits, its reserved id is
	// never handed to the engine, and its record is released, not completed
	// with a result, when the run ends.
	implicit bool
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

// SubmissionOutcomeRefusedHeader is the admission line's outcome for a
// malformed or repeated X-ACR-Submission-ID or X-ACR-Thread-ID: refused
// before anything is admitted.
const SubmissionOutcomeRefusedHeader SubmissionAdmissionOutcome = "refused_header"

// logSubmissionHeaderRefused records a header refusal on the admission line,
// naming the header (a closed pair), never its value.
func (a *App) logSubmissionHeaderRefused(r *http.Request, header string) {
	a.logger.InfoContext(r.Context(), ContextFabricSubmissionLogMessage,
		"outcome", string(SubmissionOutcomeRefusedHeader),
		"header", header,
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())))
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

// errSubmissionStoreNotConfigured is the admission line's error for a
// runtime composed without a submission store.
var errSubmissionStoreNotConfigured = errors.New("context fabric: no investigation submission store is configured")

func submissionStoreErrorClass(err error) string {
	switch {
	case errors.Is(err, errSubmissionStoreNotConfigured):
		return "store_not_configured"
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
func (a *App) admitInvestigationSubmission(w http.ResponseWriter, r *http.Request, principal storage.Principal, request contextfabric.InvestigationRequest, submissionID, threadID string, platformRun bool) (*admittedInvestigationSubmission, bool) {
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
		// CHAOS-11914: the shared limits, and whether the run counts to the
		// monthly allowance (it runs on the deployment's model).
		Limits: a.config.AskDevLimits, PlatformRun: platformRun,
	}, a.now())
	var refused *contextfabric.SubmissionRefusedError
	if errors.As(err, &refused) {
		a.writeSubmissionRefused(w, r, principal, refused, false)
		return nil, false
	}
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

// admitImplicitInvestigation admits a create WITHOUT X-ACR-Submission-ID that
// runs on the deployment's model (CHAOS-11997) under a server-minted key, in
// the same transaction and against the same shared limits as a submission:
// the concurrency counts and the monthly allowance count it, and over a
// limit it is refused 429 like a submission. Its reserved result id is
// fresh and never handed to the engine, so the answer stays unbound
// (organization-bearer reads by id, unchanged) and nothing can repeat the
// key. With no submission store configured the create runs as before
// (proceed, nil) and a Warn admission line. Any other failure fails closed: the spend ceiling is not
// skipped because its store cannot be reached.
func (a *App) admitImplicitInvestigation(w http.ResponseWriter, r *http.Request, principal storage.Principal, request contextfabric.InvestigationRequest, threadID string) (*admittedInvestigationSubmission, bool) {
	store := a.investigationSubmissions()
	if store == nil {
		// A runtime composed without a submission store (the hosted runtime
		// always has one): the create runs uncounted, as before, and says
		// so on every such request.
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeStoreUnavailable, "", errSubmissionStoreNotConfigured)
		return nil, true
	}
	key, err := contextfabric.SubmissionKeyFor(principal, contextfabric.NewImplicitSubmissionID())
	if err != nil {
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeOwnerUnknown, "", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation could not be admitted", false, nil)
		return nil, false
	}
	digest, err := contextfabric.SubmissionRequestDigest(request)
	if err != nil {
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeStoreUnavailable, "", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation could not be admitted", false, nil)
		return nil, false
	}
	record, admitted, err := store.AdmitSubmission(r.Context(), contextfabric.SubmissionAdmission{
		Key: key, RequestDigest: digest, ReservedResultID: a.reserveResultID(), ThreadID: threadID, ExpiresAt: a.submissionExpiry(r.Context()),
		Limits: a.config.AskDevLimits, PlatformRun: true,
	}, a.now())
	var refused *contextfabric.SubmissionRefusedError
	switch {
	case errors.As(err, &refused):
		a.writeSubmissionRefused(w, r, principal, refused, true)
		return nil, false
	case err != nil:
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeStoreUnavailable, "", err)
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation admission is temporarily unavailable", true, nil)
		return nil, false
	case !admitted:
		// A fresh 128-bit key that already exists: never expected; refused
		// rather than run uncounted.
		a.logSubmissionAdmission(r, principal, SubmissionOutcomeStoreUnavailable, record.State, errors.New("implicit submission key already admitted"))
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation admission is temporarily unavailable", true, nil)
		return nil, false
	}
	a.logSubmissionAdmission(r, principal, SubmissionOutcomeAdmittedImplicit, record.State, nil)
	return &admittedInvestigationSubmission{key: key, record: record, store: store, implicit: true}, true
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

// writeSubmissionRefused answers an admission the shared limits refused
// (CHAOS-11914): 429 with Retry-After; nothing was admitted or counted.
func (a *App) writeSubmissionRefused(w http.ResponseWriter, r *http.Request, principal storage.Principal, refused *contextfabric.SubmissionRefusedError, implicit bool) {
	retryAfter := max(1, int((refused.RetryAfter+time.Second-1)/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"count", refused.Count, "limit", refused.Limit, "retry_after_seconds", retryAfter,
		// CHAOS-11997: true for a create without X-ACR-Submission-ID.
		"implicit", implicit,
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
	}
	switch refused.Refusal {
	case contextfabric.SubmissionRefusedAllowanceExhausted:
		a.logger.WarnContext(r.Context(), ContextFabricSubmissionLogMessage, append([]any{"outcome", string(SubmissionOutcomeRefusedAllowance)}, args...)...)
		writeError(w, r, http.StatusTooManyRequests, contractsv1.ErrorCodeAllowanceExhausted, "The organization used its monthly Ask Dev allowance", true, nil)
	default:
		outcome, limit := SubmissionOutcomeRefusedOrgConcurrency, "org"
		if refused.Refusal == contextfabric.SubmissionRefusedPrincipalConcurrency {
			outcome, limit = SubmissionOutcomeRefusedPrincipalConcurrency, "principal"
		}
		a.logger.InfoContext(r.Context(), ContextFabricSubmissionLogMessage, append([]any{"outcome", string(outcome)}, args...)...)
		writeError(w, r, http.StatusTooManyRequests, contractsv1.ErrorCodeConcurrencyLimited, "Too many Ask Dev investigations are running", true, map[string]any{contractsv1.ContextFabricConcurrencyLimitDetailsKey: limit})
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
	case completion != submissionCompletionAbandonedOnError && completion != submissionCompletionImplicitReleased && !servedIsReserved && !reused:
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
	if submission.implicit {
		// The answer is not the record's: release the record (done_unsaved,
		// no result named) so its concurrency slot is free.
		record, _, err := submission.store.FinishSubmission(ctx, submission.key, "", a.now())
		a.logSubmissionCompletion(r, principal, submissionCompletionImplicitReleased, record.State, false, result.Reused, err)
		return
	}
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
