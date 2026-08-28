package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
		// CHAOS-3898 §2.4: Get now returns the metadata-bearing
		// StoredInvestigationResult carrier; this route serves only the
		// canonical/projected result, so it unwraps to .Result immediately
		// -- persistence metadata (GraphEpoch) is not part of the public
		// response contract.
		stored, err := results.Get(r.Context(), principal, resultID)
		if err != nil {
			a.writeInvestigationResultError(w, r, principal, err)
			return
		}
		result := stored.Result
		// The consumer projection is served from THIS route, through the
		// same answerprojection.Project the MCP tool calls (CHAOS-3746
		// codex round-1 F2). Before this, the API only ever returned the
		// canonical result, so "API and MCP agree" could only be checked
		// by calling the projection helper directly -- which proves the
		// helper is deterministic, not that the two SURFACES agree. With
		// the view here, the differential check compares two real
		// handlers and the parity guarantee is structural end to end.
		view, budget, ok := investigationResultView(r)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Context Fabric investigation result view or budget is invalid", false, nil)
			return
		}
		var payload any = result
		if view == investigationViewProjection {
			projection := answerprojection.Project(result, budget)
			// Validate before emitting (codex round-6 F2). The route
			// serves a DERIVED document, and the derivation runs over
			// stored rows that are validated leniently -- so a projection
			// bug shows up here as a schema-invalid body a client cannot
			// parse, with nothing server-side having noticed. The
			// canonical view needs no equivalent check: it re-serves a
			// document the store already validated on read.
			if err := projection.Validate(); err != nil {
				a.logger.ErrorContext(r.Context(), "context fabric projection failed contract validation",
					"request_id", RequestID(r.Context()), "failure_class", "context_fabric_projection")
				writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric answer projection could not be produced", false, nil)
				return
			}
			payload = projection
		}
		maximumBytes := int64(a.config.MaxSerializedBytes)
		items := contextFabricResultItems(result)
		encoded, measuredBytes, sizeErr := marshalContextFabricResponse(payload)
		if sizeErr != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation result could not be serialized", false, nil)
			return
		}
		estimatedTokens := (measuredBytes + 3) / 4
		if measuredBytes > maximumBytes {
			// CHAOS-4355 response-bound follow-up -- see the matching
			// branch in ContextFabricInvestigationHandler
			// (context_fabric_routes.go) for why this is a 413, not the
			// 500 "internal_error" this branch used to return with no
			// measurement at all: a stored result over
			// ACR_MAX_SERIALIZED_BYTES is a legitimate "does not fit"
			// outcome, not a server bug. This also confirms the retrieval
			// route enforces the SAME bound the investigation route wrote
			// under, so a result that returns once can also be re-read.
			a.logContextFabricResponseBudgetExceeded(r, "bytes", measuredBytes, maximumBytes, estimatedTokens, items)
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Context Fabric investigation result exceeded service limits", false, map[string]any{
				"measured_bytes": measuredBytes, "max_serialized_bytes": maximumBytes,
			})
			return
		}
		// CHAOS-4355 codex R1 P2: usage.Tokens is deliberately 0 -- see the
		// matching comment in ContextFabricInvestigationHandler
		// (context_fabric_routes.go) for why this route does not charge the
		// shared RequestClassContext Tokens budget either. estimatedTokens
		// is still measured and disclosed below for diagnostics.
		usage := limits.ResourceUsage{
			Items:  int64(items),
			Tokens: 0,
			Bytes:  measuredBytes,
		}
		if err := CompleteUsage(r.Context(), usage); err != nil {
			a.logContextFabricResponseBudgetExceeded(r, "items", measuredBytes, maximumBytes, estimatedTokens, items)
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Context Fabric investigation result exceeded service limits", false, map[string]any{
				"measured_bytes": usage.Bytes, "measured_items": usage.Items, "estimated_tokens": estimatedTokens,
				"max_items": a.config.MaxItems,
			})
			return
		}
		a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_result_read", "context_fabric_investigation", result.ResultID, "success", map[string]any{"investigation_status": result.Status, "view": string(view)})
		writeEncodedJSON(w, http.StatusOK, encoded)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

// investigationResultView is the closed set of representations this route
// can return. Both describe the SAME stored investigation; they differ only
// in how much of it a consumer receives.
type investigationResultViewName string

const (
	investigationViewCanonical  investigationResultViewName = "canonical"
	investigationViewProjection investigationResultViewName = "projection"
)

// investigationResultView reads the view and its optional budget from the
// query string.
//
// The budget knobs exist so a caller can request the SAME projection an MCP
// client would receive. Without them the two surfaces could only ever be
// compared at their respective defaults, and a parity check that cannot
// vary the budget cannot police the truncation paths -- which is exactly
// where a consumer-specific divergence would hide.
//
// It returns ok=false for anything outside the closed set or outside the
// contract bounds, rather than silently falling back to a default: a caller
// who asked for a projection and got a canonical result would not notice.
func investigationResultView(r *http.Request) (investigationResultViewName, answerprojection.Budget, bool) {
	query := r.URL.Query()
	view := investigationViewCanonical
	switch value := strings.TrimSpace(query.Get("view")); value {
	case "", string(investigationViewCanonical):
	case string(investigationViewProjection):
		view = investigationViewProjection
	default:
		return "", answerprojection.Budget{}, false
	}

	budget := answerprojection.Budget{}
	fields := []struct {
		name    string
		target  *int
		maximum int
	}{
		{"max_drivers", &budget.MaxDrivers, contractsv1.ContextFabricProjectedDriversMaxCount},
		{"max_cohort_members", &budget.MaxCohortMembers, contractsv1.ContextFabricProjectedCohortMaxCount},
		{"max_evidence_refs", &budget.MaxEvidenceRefs, contractsv1.ContextFabricProjectedEvidenceMaxCount},
	}
	for _, field := range fields {
		raw := strings.TrimSpace(query.Get(field.name))
		if raw == "" {
			continue
		}
		// A budget on the canonical view would be meaningless, and
		// accepting it would suggest it did something.
		if view != investigationViewProjection {
			return "", answerprojection.Budget{}, false
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > field.maximum {
			return "", answerprojection.Budget{}, false
		}
		*field.target = value
	}
	return view, budget, true
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
