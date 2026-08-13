package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ContextFabricInvestigationResultPath re-reads one immutable investigation
// result by its opaque result_id (CHAOS-3746).
//
// It exists so a bounded consumer does not have to choose between a usable
// answer and a complete one. MCP receives a small answer projection plus
// this result_id; when an agent needs the full canonical detail behind a
// claim it fetches it here, rather than every answer being inflated for the
// rare caller that wants everything. Replay and diagnostics read the same
// way.
//
// The path parameter is a frozen, opaque handle. Nothing parses it.
const ContextFabricInvestigationResultPath = "/api/v1/context-fabric/investigations/{result_id}"

// investigationResults returns the configured result store, or nil when the
// hosted runtime (or the store within it) is not configured. Handler()
// calls this at mux-construction time, when a.runtime may itself be nil --
// a direct field access there would panic, exactly as for investigator().
func (a *App) investigationResults() contextfabric.InvestigationResultStore {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.InvestigationResults
}

// ContextFabricInvestigationResultHandler returns the protected retrieval
// endpoint. It is strictly read-only: it never runs an investigation, never
// touches the graph, the canonical fact sources, or a model, and never
// writes. Reading a stored answer is meant to be cheap.
func (a *App) ContextFabricInvestigationResultHandler(results contextfabric.InvestigationResultStore) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The nil check lives inside the handler body, after
		// protectedRuntimeHandler has already authenticated, scoped,
		// rate-limited, and audited the request -- never as an early
		// return from this factory that would skip the wrapping
		// entirely. An unwrapped 503 would let an unauthenticated
		// caller observe "result store not configured" (CHAOS-3755
		// adversarial review finding H5, which applies here
		// identically).
		if results == nil {
			a.handleRuntimeUnavailable(w, r)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		resultID := r.PathValue("result_id")
		// Bounds-check before touching the store, and answer a
		// malformed ID with the SAME not-found response an unknown one
		// gets. A distinct "malformed" error would tell a caller
		// something about the identifier space it is not entitled to
		// learn.
		if strings.TrimSpace(resultID) != resultID || len([]rune(resultID)) < 8 || len([]rune(resultID)) > 256 {
			a.writeInvestigationResultNotFound(w, r, principal)
			return
		}
		// Organization scoping is the store's binding precondition (see
		// contextfabric.InvestigationResultStore). The principal comes
		// from authentication, never from the path or a payload.
		result, err := results.Get(r.Context(), principal, resultID)
		if err != nil {
			a.writeInvestigationResultError(w, r, principal, err)
			return
		}
		encoded, err := encodeBounded(result, int64(a.config.MaxSerializedBytes))
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation result exceeded service limits", false, nil)
			return
		}
		usage := limits.ResourceUsage{
			Items:  int64(contextFabricResultItems(result)),
			Tokens: int64((len(encoded) + 3) / 4),
			Bytes:  int64(len(encoded)),
		}
		if err := CompleteUsage(r.Context(), usage); err != nil {
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Context Fabric investigation result exceeded service limits", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_result_read", "context_fabric_investigation", result.ResultID, "success", map[string]any{"investigation_status": result.Status})
		writeEncodedJSON(w, http.StatusOK, encoded)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

// writeInvestigationResultNotFound answers the one response a caller gets
// for every "you cannot have this" case: unknown ID, malformed ID, and an
// ID belonging to another organization are indistinguishable on the wire.
//
// Making them distinguishable would turn result_id into a cross-tenant
// existence oracle: a caller could probe identifiers and learn which ones
// are real inside organizations it cannot read. The audit trail still
// records the denial with the real principal, so the distinction survives
// where it belongs -- server-side.
func (a *App) writeInvestigationResultNotFound(w http.ResponseWriter, r *http.Request, principal storage.Principal) {
	a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_result_denied", "context_fabric_investigation", "unavailable", "denied", nil)
	writeError(w, r, http.StatusNotFound, "not_found", "Context Fabric investigation result was not found", false, nil)
}

// writeInvestigationResultError maps retrieval failures.
//
// This is deliberately a separate, additive mapping rather than a reuse of
// writeContextFabricError: retrieval cannot produce the graph, model, or
// synthesis failures that mapping exists to classify, and routing a plain
// store read through it would invite a future reader to think a stored
// result could fail those ways.
func (a *App) writeInvestigationResultError(w http.ResponseWriter, r *http.Request, principal storage.Principal, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	if errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
		a.writeInvestigationResultNotFound(w, r, principal)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		writeError(w, r, http.StatusGatewayTimeout, "upstream_unavailable", "The Context Fabric result read timed out", true, nil)
		return
	}
	if errors.Is(err, contextfabric.ErrRateLimited) {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Context Fabric is rate limited; retry later", true, nil)
		return
	}
	if errors.Is(err, contextfabric.ErrUnavailable) {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Context Fabric is temporarily unavailable", true, nil)
		return
	}
	a.logger.ErrorContext(r.Context(), "context fabric investigation result read failed", "request_id", RequestID(r.Context()), "failure_class", "context_fabric_investigation_result")
	writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation result read failed", false, nil)
}
