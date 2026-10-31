package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Ask Dev answer feedback (CHAOS-11950, docs/design/ask-dev-conversation-contract.md
// §11). The route reads or sets the caller's feedback on one of its own
// results. It first takes the read by id's serving decision
// (decideStoredResultServing, surface feedback): a result the read by id
// would not serve to the caller gets the read by id's own answer, so the
// route tells a caller nothing the read by id does not. A served result that
// completed no Ask Dev submission (an MCP or Workbench result, which has no
// owner) takes no feedback and gets the same 404.

// ContextFabricResultFeedbackPath reads (GET) or sets (POST) the feedback.
const ContextFabricResultFeedbackPath = "/api/v1/context-fabric/investigations/feedback/{result_id}"

// ContextFabricResultFeedbackLogMessage is the route's decision line: one per
// request.
const ContextFabricResultFeedbackLogMessage = "context fabric result feedback"

// ResultFeedbackOutcome is the closed outcome of the feedback line.
type ResultFeedbackOutcome string

const (
	ResultFeedbackRead           ResultFeedbackOutcome = "read"
	ResultFeedbackSet            ResultFeedbackOutcome = "set"
	ResultFeedbackInvalidRequest ResultFeedbackOutcome = "invalid_request"
	// ResultFeedbackNotServed: the read by id would not serve the result
	// (unknown, another owner's, denied, withheld); the read by id's answer.
	ResultFeedbackNotServed ResultFeedbackOutcome = "not_served"
	// ResultFeedbackNotOwned: a served result that completed no submission.
	ResultFeedbackNotOwned ResultFeedbackOutcome = "not_owned"
	// ResultFeedbackGateUnavailable: the serving decision could not be
	// taken.
	ResultFeedbackGateUnavailable ResultFeedbackOutcome = "gate_unavailable"
	// ResultFeedbackNotFound: the store refused the result after the
	// decision (it was closed or removed in between).
	ResultFeedbackNotFound         ResultFeedbackOutcome = "not_found"
	ResultFeedbackStoreUnavailable ResultFeedbackOutcome = "store_unavailable"
	ResultFeedbackInvalidRecord    ResultFeedbackOutcome = "invalid_record"
)

// ResultFeedbackOutcomeVocabulary is the closed set of outcomes.
func ResultFeedbackOutcomeVocabulary() [9]ResultFeedbackOutcome {
	return [9]ResultFeedbackOutcome{
		ResultFeedbackRead, ResultFeedbackSet, ResultFeedbackInvalidRequest, ResultFeedbackNotServed, ResultFeedbackNotOwned,
		ResultFeedbackGateUnavailable, ResultFeedbackNotFound, ResultFeedbackStoreUnavailable, ResultFeedbackInvalidRecord,
	}
}

// resultFeedbackRoute names the route on the line.
type resultFeedbackRoute string

const (
	resultFeedbackRouteGet resultFeedbackRoute = "get"
	resultFeedbackRouteSet resultFeedbackRoute = "set"
)

func (a *App) logResultFeedback(r *http.Request, principal storage.Principal, route resultFeedbackRoute, outcome ResultFeedbackOutcome, err error, extra ...any) {
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"route", string(route),
		"outcome", string(outcome),
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
	}
	args = append(args, extra...)
	if err != nil {
		args = append(args, "error_class", submissionStoreErrorClass(err))
		a.logger.WarnContext(r.Context(), ContextFabricResultFeedbackLogMessage, args...)
		return
	}
	a.logger.InfoContext(r.Context(), ContextFabricResultFeedbackLogMessage, args...)
}

func (a *App) resultFeedbackStore() contextfabric.ResultFeedbackStore {
	if a.runtime == nil || storage.IsNil(a.runtime.ResultFeedback) {
		return nil
	}
	return a.runtime.ResultFeedback
}

// ContextFabricResultFeedbackHandler serves GET and POST
// /api/v1/context-fabric/investigations/feedback/{result_id}.
func (a *App) ContextFabricResultFeedbackHandler() http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := resultFeedbackRouteGet
		if r.Method == http.MethodPost {
			route = resultFeedbackRouteSet
		}
		// After protectedRuntimeHandler, never before it: an
		// unauthenticated caller must not learn whether a store is
		// configured.
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		feedback, results := a.resultFeedbackStore(), a.investigationResults()
		if feedback == nil || results == nil {
			a.logResultFeedback(r, principal, route, ResultFeedbackStoreUnavailable, errSubmissionStoreNotConfigured)
			a.handleRuntimeUnavailable(w, r)
			return
		}
		// The body first, before anything about the result is decided: a
		// malformed body is 400 whether or not the result exists.
		var request contractsv1.ContextFabricResultFeedbackRequest
		if route == resultFeedbackRouteSet {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, a.config.MaxRequestBodyBytes))
			if err == nil {
				request, err = contractsv1.ParseContextFabricResultFeedbackRequest(raw)
			}
			if err != nil {
				a.logResultFeedback(r, principal, route, ResultFeedbackInvalidRequest, nil)
				writeError(w, r, http.StatusBadRequest, "invalid_request", "Context Fabric feedback request is invalid", false, invalidBodyDetails(err))
				return
			}
		}
		// A malformed id gets the unknown id's answer, as on the read by id.
		resultID := r.PathValue("result_id")
		if strings.TrimSpace(resultID) != resultID || len([]rune(resultID)) < 8 || len([]rune(resultID)) > 256 {
			a.logResultFeedback(r, principal, route, ResultFeedbackNotServed, nil)
			a.writeInvestigationResultNotFound(w, r, principal)
			return
		}
		serving := decideStoredResultServing(r.Context(), principal, results, a.storedResultGate(), resultID, contextfabric.StoredResultSurfaceFeedback)
		if serving.gated {
			authorizationArgs := contextfabric.StoredResultAuthorizationLogArgs(principal, serving.authorization)
			authorizationArgs = append(authorizationArgs, "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())))
			a.logger.InfoContext(r.Context(), contextfabric.StoredResultAuthorizationLogMessage, authorizationArgs...)
		}
		switch serving.outcome {
		case servingServed:
		case servingUnavailable:
			a.logResultFeedback(r, principal, route, ResultFeedbackGateUnavailable, serving.err)
			a.writeInvestigationResultError(w, r, principal, serving.err)
			return
		default:
			a.logResultFeedback(r, principal, route, ResultFeedbackNotServed, nil, "serving", string(serving.outcome))
			a.writeInvestigationResultNotFound(w, r, principal)
			return
		}
		if serving.stored.SubmissionOwner == nil {
			a.logResultFeedback(r, principal, route, ResultFeedbackNotOwned, nil)
			a.writeInvestigationResultNotFound(w, r, principal)
			return
		}
		var stored contextfabric.ResultFeedback
		var err error
		now := a.now()
		if route == resultFeedbackRouteSet {
			stored, err = feedback.SetResultFeedback(r.Context(), principal, resultID, request.Correct, request.Useful, now)
		} else {
			stored, err = feedback.GetResultFeedback(r.Context(), principal, resultID, now)
		}
		if err != nil {
			a.writeResultFeedbackStoreError(w, r, principal, route, err)
			return
		}
		body := resultFeedbackBody(resultID, stored)
		if err := body.Validate(); err != nil {
			a.logResultFeedback(r, principal, route, ResultFeedbackInvalidRecord, err)
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric feedback could not be served", false, nil)
			return
		}
		outcome, action := ResultFeedbackRead, "context_fabric_result_feedback_read"
		if route == resultFeedbackRouteSet {
			outcome, action = ResultFeedbackSet, "context_fabric_result_feedback_set"
		}
		a.logResultFeedback(r, principal, route, outcome, nil)
		a.recordReadAudit(r.Context(), principal, action, "context_fabric_investigation", "", "success", nil)
		writeJSON(w, http.StatusOK, body)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

func (a *App) writeResultFeedbackStoreError(w http.ResponseWriter, r *http.Request, principal storage.Principal, route resultFeedbackRoute, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	if errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
		a.logResultFeedback(r, principal, route, ResultFeedbackNotFound, nil)
		a.writeInvestigationResultNotFound(w, r, principal)
		return
	}
	a.logResultFeedback(r, principal, route, ResultFeedbackStoreUnavailable, err)
	writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric feedback is temporarily unavailable", true, nil)
}

// resultFeedbackBody is the wire form of stored.
func resultFeedbackBody(resultID string, stored contextfabric.ResultFeedback) contractsv1.ContextFabricResultFeedback {
	body := contractsv1.ContextFabricResultFeedback{
		SchemaVersion: contractsv1.ContextFabricResultFeedbackSchema,
		ResultID:      resultID, Correct: stored.Correct, Useful: stored.Useful,
	}
	if !stored.UpdatedAt.IsZero() {
		at := stored.UpdatedAt.UTC().Truncate(time.Microsecond)
		body.UpdatedAt = &at
	}
	return body
}
