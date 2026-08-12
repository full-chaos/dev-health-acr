package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ContextFabricInvestigationsPath is the consumer-neutral ACR investigation
// endpoint (CHAOS-3755). It is registered in App.Handler behind the same
// protectedRuntimeHandler auth/entitlement/scope/limits/timeout/audit
// boundary every other /api/v1/agent-context/* route uses.
const ContextFabricInvestigationsPath = "/api/v1/context-fabric/investigations"

// investigator returns the configured contextfabric.Investigator, or nil if
// the hosted runtime (or the investigator within it) is not configured.
// Handler() calls this at mux-construction time, when a.runtime may itself
// be nil (see TestDevelopmentStub_protected_routes_fail_closed_without_runtime)
// -- a direct a.runtime.Investigator field access there would panic.
func (a *App) investigator() contextfabric.Investigator {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.Investigator
}

// ContextFabricInvestigationHandler returns the fully protected endpoint seam
// for the Reset 1 engine. Hosting composition supplies the investigator; API
// code does not choose a graph backend or canonical fact adapter.
func (a *App) ContextFabricInvestigationHandler(investigator contextfabric.Investigator) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The nil check MUST live inside the handler body, after
		// protectedRuntimeHandler has already run (auth, scope, rate
		// limit) -- not as an early return from this factory that skips
		// wrapping entirely. An early-return-unwrapped 503 would let an
		// unauthenticated caller observe "investigator not configured"
		// without ever being authenticated, rate-limited, or audited
		// (CHAOS-3755 adversarial review finding H5).
		if investigator == nil {
			a.handleRuntimeUnavailable(w, r)
			return
		}
		// Principal is read FIRST so every failure path below can audit
		// (CHAOS-3755 adversarial review finding M5). recordReadAudit
		// needs principal.OrgID, so a failure that returned before this
		// line could never be audited at all.
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		var request contextfabric.InvestigationRequest
		if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil {
			status := http.StatusBadRequest
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			a.recordContextFabricFailure(r, principal, "invalid_request")
			writeError(w, r, status, "invalid_request", "Context Fabric investigation request is invalid", false, nil)
			return
		}
		request.RequestID = RequestID(r.Context())
		if err := request.Validate(); err != nil || request.Options.MaxSerializedBytes > a.config.MaxSerializedBytes {
			a.recordContextFabricFailure(r, principal, "invalid_request")
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Context Fabric investigation request is invalid", false, nil)
			return
		}
		result, err := investigator.Investigate(r.Context(), principal, request)
		if err != nil {
			a.writeContextFabricError(w, r, principal, err)
			return
		}
		maximumBytes := min(int64(a.config.MaxSerializedBytes), int64(request.Options.MaxSerializedBytes))
		encoded, err := encodeBounded(result, maximumBytes)
		if err != nil {
			a.recordContextFabricFailure(r, principal, "response_over_budget")
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation response exceeded service limits", false, nil)
			return
		}
		usage := limits.ResourceUsage{
			Items:  int64(contextFabricResultItems(result)),
			Tokens: int64((len(encoded) + 3) / 4),
			Bytes:  int64(len(encoded)),
		}
		if err := CompleteUsage(r.Context(), usage); err != nil {
			a.recordContextFabricFailure(r, principal, "response_over_budget")
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Context Fabric investigation response exceeded service limits", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_completed", "context_fabric_investigation", result.ResultID, "success", map[string]any{"investigation_status": result.Status})
		writeEncodedJSON(w, http.StatusOK, encoded)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

// recordContextFabricFailure audits one failed investigation attempt
// (CHAOS-3755 adversarial review finding M5). Only the fixed failure
// classification is recorded -- never the question, the error text, or any
// investigation content -- so the audit trail gains the attempt without
// becoming a new disclosure surface. There is no result to name yet, so
// the resource ID is the fixed string "unavailable", matching the
// convention the other pre-result denial audits in this package already
// use.
func (a *App) recordContextFabricFailure(r *http.Request, principal storage.Principal, failureClass string) {
	a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_failed", "context_fabric_investigation", "unavailable", "failed", map[string]any{"failure_class": failureClass})
}

func (a *App) writeContextFabricError(w http.ResponseWriter, r *http.Request, principal storage.Principal, err error) {
	// A canceled request is the caller hanging up: there is no response to
	// write and nothing was denied or failed server-side, so it is
	// deliberately not audited.
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		a.recordContextFabricFailure(r, principal, "timeout")
		writeError(w, r, http.StatusGatewayTimeout, "upstream_unavailable", "The Context Fabric investigation timed out", true, nil)
		return
	}
	// A historical/point-in-time question this engine cannot answer.
	// 400, not 5xx: the request was well-formed but asked for something
	// unsupported, and it is not retryable as-is (CHAOS-3755 finding H6).
	if errors.Is(err, contextfabric.ErrUnsupportedTimeAxis) {
		a.recordContextFabricFailure(r, principal, "unsupported_time_axis")
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Context Fabric can only answer questions about current state", false, nil)
		return
	}
	// Rate limiting: contextfabric.ErrRateLimited is the vendor-neutral
	// classification every graph backend adapter wraps its own
	// rate-limit error into (see zepgraph.safeDependencyError);
	// ErrModelRateLimited is the pre-existing, distinct classification
	// for the model runtime (ADR 0008). Both mean the same thing to a
	// caller: back off and retry later.
	if errors.Is(err, contextfabric.ErrRateLimited) || errors.Is(err, contextfabric.ErrModelRateLimited) {
		a.recordContextFabricFailure(r, principal, "rate_limited")
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Context Fabric is rate limited; retry later", true, nil)
		return
	}
	// contextfabric.ErrUnavailable already covers both a graph/model
	// dependency being down AND a graph backend rejecting ACR's own
	// service credential (zepgraph.safeDependencyError wraps that case
	// into ErrUnavailable too -- see its comment: an ACR-side credential
	// problem is never presented to the caller as "you are unauthorized").
	// ErrModelUnavailable joins the same bucket for the model runtime.
	if errors.Is(err, contextfabric.ErrUnavailable) || errors.Is(err, contextfabric.ErrModelUnavailable) {
		a.recordContextFabricFailure(r, principal, "dependency_unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Context Fabric is temporarily unavailable", true, nil)
		return
	}
	// The model produced output that failed grounding/evidence-closure
	// validation (SynthesisDraft.ValidateAgainst). This is an upstream
	// data-quality failure, not an ACR bug: 502 (not 500) so a caller can
	// tell the two apart, and retryable because a fresh model call may
	// succeed even though this one didn't.
	if errors.Is(err, contextfabric.ErrModelOutput) {
		a.recordContextFabricFailure(r, principal, "invalid_model_output")
		writeError(w, r, http.StatusBadGateway, "upstream_invalid_output", "Context Fabric produced an invalid answer; retry", true, nil)
		return
	}
	a.recordContextFabricFailure(r, principal, "internal_error")
	a.logger.ErrorContext(r.Context(), "context fabric investigation failed", "request_id", RequestID(r.Context()), "failure_class", "context_fabric_investigation")
	writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation failed", false, nil)
}

func contextFabricResultItems(result contextfabric.InvestigationResult) int {
	items := len(result.SubjectResolution.Candidates) + len(result.Drivers) + len(result.Paths) + len(result.RemainingWork) + len(result.ReadinessGaps) + len(result.Conflicts) + len(result.ClaimedFacts)
	if result.Cohort != nil {
		items += len(result.Cohort.Members)
	}
	return items
}
