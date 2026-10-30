package api

import (
	"context"
	"errors"
	"net/http"

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
		}
		status := contractsv1.ContextFabricSubmissionStatus{
			SchemaVersion: contractsv1.ContextFabricSubmissionStatusSchema, SubmissionID: record.Key.SubmissionID,
			State: contractsv1.ContextFabricSubmissionState(record.State), ReservedResultID: record.ReservedResultID,
			ResultID: record.ResultID, ThreadID: record.ThreadID, UpdatedAt: record.UpdatedAt.UTC(),
		}
		if err := status.Validate(); err != nil {
			a.logSubmissionStatus(r, principal, SubmissionStatusInvalidRecord, record.State, err)
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation submission could not be served", false, nil)
			return
		}
		a.logSubmissionStatus(r, principal, SubmissionStatusServed, record.State, nil, "owner_match", string(ownerMatchOf(principal, record)))
		a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_submission_status_read", "context_fabric_investigation_submission", record.ReservedResultID, "success", map[string]any{"state": string(record.State)})
		writeJSON(w, http.StatusOK, status)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}
