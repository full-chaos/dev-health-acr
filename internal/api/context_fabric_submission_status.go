package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ContextFabricSubmissionStatusPath is the caller's own submission record
// (CHAOS-11913, plan §2.7.5). A client that lost a create response polls it
// and replays result_id when the state is done.
const ContextFabricSubmissionStatusPath = "/api/v1/context-fabric/investigations/submissions/{submission_id}"

// ContextFabricSubmissionStatusLogMessage is the status read's decision line.
const ContextFabricSubmissionStatusLogMessage = "context fabric investigation submission status"

// SubmissionStatusOutcome is the closed outcome of the status line.
type SubmissionStatusOutcome string

const (
	SubmissionStatusServed           SubmissionStatusOutcome = "served"
	SubmissionStatusNotFound         SubmissionStatusOutcome = "not_found"
	SubmissionStatusStoreUnavailable SubmissionStatusOutcome = "store_unavailable"
	SubmissionStatusInvalidRecord    SubmissionStatusOutcome = "invalid_record"
)

// SubmissionStatusOutcomeVocabulary is the closed set of outcomes.
func SubmissionStatusOutcomeVocabulary() [4]SubmissionStatusOutcome {
	return [4]SubmissionStatusOutcome{SubmissionStatusServed, SubmissionStatusNotFound, SubmissionStatusStoreUnavailable, SubmissionStatusInvalidRecord}
}

func (a *App) logSubmissionStatus(r *http.Request, principal storage.Principal, outcome SubmissionStatusOutcome, state contextfabric.SubmissionState, err error, extra ...any) {
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"surface", "submission_status",
		"outcome", string(outcome),
		"state", string(state),
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
	}
	args = append(args, extra...)
	if err != nil {
		args = append(args, "error_class", submissionStoreErrorClass(err))
		a.logger.WarnContext(r.Context(), ContextFabricSubmissionStatusLogMessage, args...)
		return
	}
	a.logger.InfoContext(r.Context(), ContextFabricSubmissionStatusLogMessage, args...)
}

// writeSubmissionNotFound is the one answer for an unknown id, a malformed
// id, and another principal's submission (INV-1, existence-neutral): the key
// includes the owner, so another principal's record is not found by
// construction, and the response cannot tell the three apart.
func (a *App) writeSubmissionNotFound(w http.ResponseWriter, r *http.Request, principal storage.Principal) {
	a.logSubmissionStatus(r, principal, SubmissionStatusNotFound, "", nil)
	writeError(w, r, http.StatusNotFound, "not_found", "Context Fabric investigation submission was not found", false, nil)
}

// ContextFabricSubmissionStatusHandler serves the caller's own submission
// record. It is read-only and runs nothing; the read applies the store's
// reconcile rule (a running record whose reserved result is stored reads as
// done; one past its expiry reads as abandoned), like a repeat of the key.
func (a *App) ContextFabricSubmissionStatusHandler(submissions contextfabric.SubmissionStore) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// After protectedRuntimeHandler, never before it: an unauthenticated
		// caller must not learn whether the store is configured.
		if submissions == nil {
			a.logger.WarnContext(r.Context(), ContextFabricSubmissionStatusLogMessage,
				"surface", "submission_status", "outcome", string(SubmissionStatusStoreUnavailable), "error_class", "store_not_configured",
				"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())))
			a.handleRuntimeUnavailable(w, r)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		submissionID := r.PathValue("submission_id")
		if !contextfabric.ValidSubmissionID(submissionID) {
			a.writeSubmissionNotFound(w, r, principal)
			return
		}
		key, err := contextfabric.SubmissionKeyFor(principal, submissionID)
		if err != nil {
			a.writeSubmissionNotFound(w, r, principal)
			return
		}
		record, err := submissions.GetSubmission(r.Context(), key, a.now())
		switch {
		case errors.Is(err, contextfabric.ErrSubmissionNotFound):
			a.writeSubmissionNotFound(w, r, principal)
			return
		case errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled):
			return
		case err != nil:
			a.logSubmissionStatus(r, principal, SubmissionStatusStoreUnavailable, "", err)
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation submissions are temporarily unavailable", true, nil)
			return
		case record.ThreadClosed != "":
			// CHAOS-11948 (design §6 T6): a turn of a deleted or expired
			// thread is not found, like an unknown id.
			a.writeSubmissionNotFound(w, r, principal)
			return
		}
		record, lastStage, terminal, err := a.submissionRunDetail(r.Context(), submissions, key, record)
		switch {
		case errors.Is(err, contextfabric.ErrSubmissionNotFound):
			a.writeSubmissionNotFound(w, r, principal)
			return
		case errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled):
			return
		case err != nil:
			a.logSubmissionStatus(r, principal, SubmissionStatusStoreUnavailable, "", err)
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation submissions are temporarily unavailable", true, nil)
			return
		}
		status := submissionStatusOf(record)
		status.LastStage, status.Terminal = lastStage, terminal
		if err := status.Validate(); err != nil {
			a.logSubmissionStatus(r, principal, SubmissionStatusInvalidRecord, record.State, err)
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation submission could not be served", false, nil)
			return
		}
		a.logSubmissionStatus(r, principal, SubmissionStatusServed, record.State, nil, "owner_match", string(ownerMatchOf(principal, record)))
		a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_submission_status_read", "context_fabric_investigation_submission", record.ReservedResultID, "success", map[string]any{"state": string(record.State)})
		writeJSON(w, http.StatusOK, status)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, false, handler)
}

// submissionRunDetail adds the run lease's view to a leased record, when
// this deployment runs leased runs: the last stage started and, once final,
// the terminal the run's event log holds. No terminal is named until the log
// holds one: the delivery can still end in an error after the record is
// final. Two reads are writes:
//
//   - a running record whose lease ended is taken over first (the same
//     single-run takeover an attach makes), so it answers abandoned with
//     run_lost;
//   - a final record whose lease ended with no terminal in its log (its
//     holder stopped between the record and the log, or the record was
//     reconciled final after both expiries) gets the terminal its state
//     implies -- run_lost for abandoned -- appended once: the log keeps the
//     first terminal, whoever writes it.
//
// A record with no lease, or a deployment without the run lease, gets no
// detail.
func (a *App) submissionRunDetail(ctx context.Context, submissions contextfabric.SubmissionStore, key contextfabric.SubmissionKey, record contextfabric.SubmissionRecord) (contextfabric.SubmissionRecord, contractsv1.ContextFabricInvestigationStage, *contractsv1.ContextFabricSubmissionTerminal, error) {
	runs := a.investigationRuns()
	if !a.runLeaseServed() || !record.Leased() {
		return record, "", nil, nil
	}
	run := contextfabric.RunKeyOf(record)
	if record.State == contextfabric.SubmissionRunning && !record.LeaseExpiresAt.After(a.now()) {
		if _, err := a.takeOverLostRuns(context.WithoutCancel(ctx), runs, &run); err != nil {
			return record, "", nil, err
		}
		reread, err := submissions.GetSubmission(ctx, key, a.now())
		if err != nil {
			return record, "", nil, err
		}
		record = reread
	}
	page, err := runs.ReadRunEvents(ctx, run, 0, 1)
	if err != nil {
		return record, "", nil, err
	}
	if record.State == contextfabric.SubmissionRunning {
		return record, page.LastStageStarted, nil, nil
	}
	logged, err := runs.RunTerminalOf(ctx, run)
	if err != nil {
		return record, "", nil, err
	}
	if logged == nil && !record.LeaseExpiresAt.After(a.now()) {
		if logged, err = a.appendFinalRecordTerminal(ctx, runs, run, record, page); err != nil {
			return record, "", nil, err
		}
	}
	if logged == nil || logged.Terminal == nil {
		return record, page.LastStageStarted, nil, nil
	}
	terminal := &contractsv1.ContextFabricSubmissionTerminal{Kind: contractsv1.ContextFabricSubmissionTerminalKind(logged.Terminal.Kind)}
	if logged.Terminal.Kind == contextfabric.RunTerminalError && logged.Terminal.Error != nil {
		terminal.ErrorCode = logged.Terminal.Error.Error.Code
	}
	return record, page.LastStageStarted, terminal, nil
}

// appendFinalRecordTerminal appends the terminal a final leased record's
// state implies to a log that holds none, and reads back the log's terminal:
// its own, or one another writer appended first.
func (a *App) appendFinalRecordTerminal(ctx context.Context, runs contextfabric.RunStore, run contextfabric.RunKey, record contextfabric.SubmissionRecord, page contextfabric.RunEventPage) (*contextfabric.RunEvent, error) {
	end := derivedRunTerminal(record)
	eventType := contractsv1.ContextFabricInvestigationEventResult
	if end.Kind == contextfabric.RunTerminalError {
		eventType = contractsv1.ContextFabricInvestigationEventError
	}
	writeCtx := context.WithoutCancel(ctx)
	appended, err := runs.AppendRunEvent(writeCtx, run, contextfabric.RunEvent{
		Seq: page.LastSeq + 1, Type: eventType, LastStageStarted: page.LastStageStarted, Terminal: &end,
	}, a.runLease.maxEvents, a.now())
	if err != nil && !errors.Is(err, contextfabric.ErrRunEventSeq) {
		return nil, err
	}
	a.logger.WarnContext(ctx, ContextFabricRunTakeoverLogMessage,
		"org_id", contextfabric.SanitizeLogAttr(run.OrgID),
		"request_id", contextfabric.SanitizeLogAttr(record.RunRequestID),
		"reserved_result_id", contextfabric.SanitizeLogAttr(run.ReservedResultID),
		"previous_holder", contextfabric.SanitizeLogAttr(record.LeaseHolder),
		"taker", contextfabric.SanitizeLogAttr(a.runLease.holder),
		"lease_expired_at", record.LeaseExpiresAt.UTC().Format(time.RFC3339Nano),
		"state", string(record.State), "terminal_appended", appended)
	return runs.RunTerminalOf(writeCtx, run)
}
