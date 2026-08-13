package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/limits"
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
		var request contextfabric.InvestigationRequest
		if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil {
			status := http.StatusBadRequest
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			writeError(w, r, status, "invalid_request", "Context Fabric investigation request is invalid", false, nil)
			return
		}
		request.RequestID = RequestID(r.Context())
		if err := request.Validate(); err != nil || request.Options.MaxSerializedBytes > a.config.MaxSerializedBytes {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Context Fabric investigation request is invalid", false, nil)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		result, err := investigator.Investigate(r.Context(), principal, request)
		if err != nil {
			a.writeContextFabricError(w, r, err)
			return
		}
		maximumBytes := min(int64(a.config.MaxSerializedBytes), int64(request.Options.MaxSerializedBytes))
		encoded, err := encodeBounded(result, maximumBytes)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation response exceeded service limits", false, nil)
			return
		}
		usage := limits.ResourceUsage{
			Items:  int64(contextFabricResultItems(result)),
			Tokens: int64((len(encoded) + 3) / 4),
			Bytes:  int64(len(encoded)),
		}
		if err := CompleteUsage(r.Context(), usage); err != nil {
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Context Fabric investigation response exceeded service limits", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_completed", "context_fabric_investigation", result.ResultID, "success", map[string]any{"investigation_status": result.Status})
		writeEncodedJSON(w, http.StatusOK, encoded)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

func (a *App) writeContextFabricError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		writeError(w, r, http.StatusGatewayTimeout, "upstream_unavailable", "The Context Fabric investigation timed out", true, nil)
		return
	}
	// A historical question whose BOUNDS this engine will not answer --
	// an as-of time in the future, or a range wider than it will read
	// (CHAOS-3781, contextfabric.ErrInvalidTimeBound). 400, not 5xx: the
	// request was well-formed but asked for something outside those
	// bounds, so presenting it as an ACR outage would be wrong -- and it
	// is not retryable, because the same request can never start
	// succeeding.
	//
	// This REPLACES the CHAOS-3755 H6 mapping of ErrUnsupportedTimeAxis,
	// which refused every non-current axis outright. Historical questions
	// are answered now; only unanswerable bounds are refused. AC-3781-6
	// required that removal land in the same change as the engine's and
	// the providers' -- a layer left refusing would either contradict the
	// others, or answer with current data under a historical label.
	if errors.Is(err, contextfabric.ErrInvalidTimeBound) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The requested time is not answerable: it must not be in the future, and a range must be narrower than the supported window", false, nil)
		return
	}
	// Rate limiting: contextfabric.ErrRateLimited is the vendor-neutral
	// classification every graph backend adapter wraps its own
	// rate-limit error into (see falkorgraph.safeDependencyError);
	// ErrModelRateLimited is the pre-existing, distinct classification
	// for the model runtime (ADR 0008). Both mean the same thing to a
	// caller: back off and retry later.
	if errors.Is(err, contextfabric.ErrRateLimited) || errors.Is(err, contextfabric.ErrModelRateLimited) {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Context Fabric is rate limited; retry later", true, nil)
		return
	}
	// contextfabric.ErrUnavailable already covers both a graph/model
	// dependency being down AND a graph backend rejecting ACR's own
	// service credential (falkorgraph.safeDependencyError wraps that case
	// into ErrUnavailable too -- see its comment: an ACR-side credential
	// problem is never presented to the caller as "you are unauthorized").
	// ErrModelUnavailable joins the same bucket for the model runtime.
	if errors.Is(err, contextfabric.ErrUnavailable) || errors.Is(err, contextfabric.ErrModelUnavailable) {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Context Fabric is temporarily unavailable", true, nil)
		return
	}
	// Interpretation/synthesis bound violations (CHAOS-3784). The model
	// produced structurally parseable output that ACR's OWN contracts/v1
	// bounds rejected (InterpretedQuestion.Validate /
	// SynthesisDraft.ValidateAgainst) -- distinct from a raw provider/
	// schema failure below, and worth a distinct code plus (when
	// determinable) the violated bound's name: CHAOS-3770 evidence showed 5
	// of 8 real failures were exactly one bound (requested_judgment's
	// 256-character cap) with zero signal at this endpoint. Checked BEFORE
	// the plain ErrModelOutput branch: both these sentinels also wrap
	// ErrModelOutput (contextfabric.RuntimeQuestionInterpreter.Interpret /
	// RuntimeAnswerSynthesizer.Synthesize), so the more specific check must
	// run first.
	//
	// Status is 422 (Unprocessable Entity), deliberately NOT 502: a bound
	// violation is not the provider misbehaving (that stays 502, below) and
	// not an ACR bug (that stays 500) -- it is ACR's own validator
	// rejecting a derived artifact, the same "well-formed but semantically
	// rejected" shape 422 already names elsewhere in HTTP. Retryable
	// because a fresh model call, independent of the rejected one, may
	// produce compliant output.
	//
	// Only the violated bound's fixed, ACR-owned registry NAME goes in
	// details (contractsv1.ContextFabricModelFacingBounds) -- e.g.
	// "interpretation.requested_judgment.max_length" -- never the rejected
	// value or any other model-generated text. A business-rule rejection
	// (an invalid enum, a claim-binding/grounding failure) has no single
	// bound to name, so details is omitted for those.
	if errors.Is(err, contextfabric.ErrInterpretationRejected) {
		writeContextFabricRejectionError(w, r, err, "interpretation_rejected", "Context Fabric's interpretation of the question violated a v1 bound")
		return
	}
	if errors.Is(err, contextfabric.ErrSynthesisRejected) {
		writeContextFabricRejectionError(w, r, err, "synthesis_rejected", "Context Fabric's synthesized answer violated a v1 bound")
		return
	}
	// A provider/transport-level failure: the raw generation call failed,
	// or Genkit itself could not parse the provider's response into the
	// expected schema (genkitruntime.classifyModelError). This is still an
	// upstream data-quality failure, not an ACR bug: 502 (not 500) so a
	// caller can tell the two apart, and retryable because a fresh model
	// call may succeed even though this one didn't. Never a bound
	// violation -- no violated_bound in details.
	//
	// Code stays upstream_invalid_output (unchanged from before CHAOS-3784,
	// deliberately NOT renamed to a new "provider_error" value): round-2
	// review flagged a rename here as gratuitous breakage for any existing
	// upstream_invalid_output matcher outside this repo, for zero benefit
	// -- the new interpretation_rejected/synthesis_rejected codes above
	// already carry the distinguishing signal this ticket asked for.
	if errors.Is(err, contextfabric.ErrModelOutput) {
		writeError(w, r, http.StatusBadGateway, "upstream_invalid_output", "Context Fabric produced an invalid answer; retry", true, nil)
		return
	}
	a.logger.ErrorContext(r.Context(), "context fabric investigation failed", "request_id", RequestID(r.Context()), "failure_class", "context_fabric_investigation")
	writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation failed", false, nil)
}

// writeContextFabricRejectionError writes the shared 422 response shape for
// ErrInterpretationRejected/ErrSynthesisRejected, attaching
// details.violated_bound only when err carries a
// *contextfabric.ModelBoundViolation.
func writeContextFabricRejectionError(w http.ResponseWriter, r *http.Request, err error, code, message string) {
	var violation *contextfabric.ModelBoundViolation
	var details map[string]any
	if errors.As(err, &violation) {
		details = map[string]any{"violated_bound": violation.Bound}
	}
	writeError(w, r, http.StatusUnprocessableEntity, code, message, true, details)
}

func contextFabricResultItems(result contextfabric.InvestigationResult) int {
	items := len(result.SubjectResolution.Candidates) + len(result.Drivers) + len(result.Paths) + len(result.RemainingWork) + len(result.ReadinessGaps) + len(result.Conflicts) + len(result.ClaimedFacts)
	if result.Cohort != nil {
		items += len(result.Cohort.Members)
	}
	return items
}
