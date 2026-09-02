package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
		result, err := investigateRecovered(r.Context(), investigator, principal, request)
		if err != nil {
			a.writeContextFabricError(w, r, err)
			return
		}
		maximumBytes := min(int64(a.config.MaxSerializedBytes), int64(request.Options.MaxSerializedBytes))
		itemCounts := contextFabricResultItemCounts(result)
		encoded, measuredBytes, sizeErr := marshalContextFabricResponse(result)
		if sizeErr != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation response could not be serialized", false, nil)
			return
		}
		estimatedTokens := (measuredBytes + 3) / 4
		if measuredBytes > maximumBytes {
			// CHAOS-4355 response-bound follow-up: this is a legitimate
			// "the answer does not fit" outcome (a Rows-bearing result over
			// ACR_MAX_SERIALIZED_BYTES, or a caller-requested
			// options.max_serialized_bytes below what the answer needs),
			// never a server bug -- classify and disclose it exactly like
			// the CompleteUsage budget below, instead of the misleading
			// 500 "internal_error" this branch used to return with no
			// measurement at all.
			a.logContextFabricResponseBudgetExceeded(r, "bytes", measuredBytes, maximumBytes, estimatedTokens, itemCounts)
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Context Fabric investigation response exceeded service limits", false, map[string]any{
				"measured_bytes": measuredBytes, "max_serialized_bytes": maximumBytes,
			})
			return
		}
		// CHAOS-4355 codex R2 ruling (team-lead): usage.Tokens carries the
		// REAL estimate -- accounting must stay truthful, per
		// window.org.Tokens/credential.Tokens (internal/limits/manager.go's
		// complete()), which Manager.Usage() reports back as-is. What
		// changes is the CEILING that estimate is checked against: the
		// shared RequestClassContext budget's MaxTokens is
		// a.config.MaxOutputTokens (the SAME value
		// cmd/acr-api/server_build.go advertises as
		// CapabilityLimits.MaxOutputTokens and internal/contracts/v1's
		// Context Packet/MCP wire validators cap a caller-REQUESTED budget
		// at, all sized for a text-only answer -- see
		// internal/config/config.go's defaultMaxOutputTokens doc comment
		// for why that must stay untouched). A Context Fabric investigation
		// response carries no caller-declared token budget and can
		// legitimately be a Rows-bearing result several times that size
		// while still comfortably inside ACR_MAX_SERIALIZED_BYTES -- the
		// authoritative "does this fit the wire" gate already enforced
		// above. CompleteUsageWithBudget evaluates against an override
		// budget (MaxTokens: 0, i.e. unlimited per
		// limits.ResourceBudget.allows) instead of the class's shared one,
		// without creating a new RequestClass that would fragment
		// RequestClassContext's shared rate-limit window across
		// context-packets/context-fabric/model-config -- see
		// limits.Claim.CompleteWithBudget's doc comment.
		//
		// CHAOS-4523 codex P2 finding: the SAME truthful-accounting rule
		// applies to Items. usage.Items carries the REAL total
		// (itemCounts.Total(), Paths included) -- Manager.Usage() records
		// this verbatim into the org/credential window (manager.go's
		// complete()), so charging the budgeted() subset here would
		// silently under-report every response that carries any Paths, the
		// same false-accounting defect CompleteWithBudget's own doc
		// comment warns against. What changes is the CEILING: override's
		// MaxItems is widened by itemCounts.Paths, so the gate condition
		// `total <= MaxItems+Paths` is exactly `budgeted <= MaxItems` --
		// Paths stops binding the gate without the recorded usage ever
		// diverging from what was actually served.
		usage := limits.ResourceUsage{
			Items:  int64(itemCounts.Total()),
			Tokens: estimatedTokens,
			Bytes:  measuredBytes,
		}
		override := limits.ResourceBudget{
			MaxItems: int64(a.config.MaxItems) + int64(itemCounts.Paths), MaxTokens: 0, MaxBytes: int64(a.config.MaxSerializedBytes),
		}
		if err := CompleteUsageWithBudget(r.Context(), usage, override); err != nil {
			a.logContextFabricResponseBudgetExceeded(r, "items", measuredBytes, maximumBytes, estimatedTokens, itemCounts)
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Context Fabric investigation response exceeded service limits", false, map[string]any{
				"measured_bytes": usage.Bytes, "measured_items": usage.Items, "estimated_tokens": estimatedTokens,
				"max_items":       a.config.MaxItems,
				"items_breakdown": itemCounts,
			})
			return
		}
		a.logContextFabricResponseBudgetMeasured(r, measuredBytes, maximumBytes, estimatedTokens, itemCounts)
		a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_completed", "context_fabric_investigation", result.ResultID, "success", map[string]any{"investigation_status": result.Status})
		writeEncodedJSON(w, http.StatusOK, encoded)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

// errContextFabricPanic classifies a panic that escaped the investigator
// (CHAOS-3810 codex round-1 P3). It is a FIXED sentinel carrying nothing from
// the panic value: a panic value is arbitrary, caller-influenced data, so it
// must never become part of an error the classifier, the log line, or the
// response body can render.
var errContextFabricPanic = errors.New("context fabric investigation panicked")

// investigateRecovered calls the investigator and converts a panic into
// errContextFabricPanic, so a panic exits through writeContextFabricError
// like every other failure -- with a stage and a bounded classification --
// instead of reaching the global recovery middleware, which writes a 500
// carrying no context-fabric signal at all.
//
// The recovered value is deliberately DROPPED: not logged, not wrapped, not
// stringified. The global middleware never logged it either (App.recovery-
// Middleware records only the request ID and status), so nothing is lost by
// handling the panic here; what is gained is failure_stage and
// failure_classification on a failure that previously had neither. The stage
// resolves to "unknown" -- a panic carries no stage, and inventing one would
// be a guess.
//
// Scope is exactly the investigator call. A panic anywhere else in the
// handler still reaches the global middleware unchanged.
func investigateRecovered(ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, request contextfabric.InvestigationRequest) (result contextfabric.InvestigationResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = contextfabric.InvestigationResult{}, errContextFabricPanic
		}
	}()
	return investigator.Investigate(ctx, principal, request)
}

// Bounded failure classifications for the investigation endpoint
// (CHAOS-3811). Each value names the SENTINEL the classifier matched, never
// anything derived from the error's own text: a raw provider, driver, or
// model message must not reach a log line at any level -- there is no debug
// hatch that widens this set, because a guarantee that holds only at some log
// levels is not a guarantee.
//
// contextFabricClassUnclassified is the one value that means "the classifier
// recognized nothing". Before this existed, EVERY failure looked like that
// from outside; now it is a specific, alertable signal that something reached
// the route with no sentinel of its own.
const (
	contextFabricClassDeadline          = "deadline_exceeded"
	contextFabricClassTimeBound         = "invalid_time_bound"
	contextFabricClassRateLimited       = "rate_limited"
	contextFabricClassUnavailable       = "dependency_unavailable"
	contextFabricClassInterpretRejected = "interpretation_rejected"
	contextFabricClassSynthesisRejected = "synthesis_rejected"
	contextFabricClassModelOutput       = "model_output_invalid"
	contextFabricClassNoSubjects        = "no_investigation_subjects"
	contextFabricClassInvalidResult     = "invalid_result"
	contextFabricClassPanic             = "panic"
	// contextFabricClassBudgetRefusal (CHAOS-4636) is decision D5's PLANNED
	// refusal: the engine measured its own assembled answer, re-synthesized
	// once with a smaller input, and it still did not fit. It is a distinct
	// class from the two 413s below it because it is the only one that
	// carries a diagnosis -- the plan says which ceiling was exceeded and
	// what narrower question would fit -- and because the fix for it is a
	// planner change, not an operator one.
	contextFabricClassBudgetRefusal       = "budget_refusal"
	contextFabricClassUnclassified        = "unclassified"
	contextFabricInvestigationFailureName = "context_fabric_investigation"
)

func (a *App) writeContextFabricError(w http.ResponseWriter, r *http.Request, err error) {
	// A panic is classified FIRST, ahead of both context checks (CHAOS-3811
	// codex round-2 F3). It lost twice to them before: a panic racing a
	// client disconnect returned at the cancellation check with no log at
	// all, and a panic under an exceeded deadline was reported as a timeout
	// -- so the failure mode P3 exists to make visible was invisible exactly
	// when a request was already going badly.
	//
	// The LOG fires regardless of context state, because observability is
	// the whole point of the branch; the RESPONSE still obeys the
	// cancellation rule below, since a disconnected client has nothing to
	// receive. Under an exceeded deadline the response is written and says
	// panic, not timeout: the request did not run out of time, it broke.
	if errors.Is(err, errContextFabricPanic) {
		a.logContextFabricFailure(r, err, contextFabricClassPanic, http.StatusInternalServerError)
		if errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation failed", false, nil)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	// CHAOS-4636 / decision D5: the PLANNED, EXPLAINED refusal.
	//
	// Classified BEFORE the deadline check on purpose. The engine refuses
	// rather than retrying when too little of the deadline remains to run a
	// second synthesis safely, so this error and an exceeded deadline
	// legitimately arrive together -- and reporting it as a timeout would
	// hide the one diagnosis that says what to do about it. The request did
	// not run out of time; the answer did not fit, and the engine declined
	// to gamble the remaining deadline on a retry that would have 504'd.
	//
	// Still a 413, and still not retryable: the same question asked again
	// produces the same oversized answer. What is new is that the response
	// says which ceiling was exceeded, by how much, and along which axis a
	// narrower question would fit -- instead of a bare acr_rejected_request.
	//
	// CHAOS-4735: `narrower_question` -- a fixed English sentence the engine
	// picked by switching on the question family -- is GONE from this body,
	// replaced by `narrower_continuation`, an object of closed tokens. The
	// engine does not author user language (chris, 2026-08-31 13:35/13:40);
	// naming the axis is a structural claim it can make, phrasing it is not.
	//
	// The shape change is safe for the deployed consumer and that was
	// verified rather than assumed: ask-dev's parseUpstreamError reads only
	// `request_id`, `error.code` and `error.retryable`, and `error.message`
	// is deliberately absent from its UpstreamError type -- it never reads
	// `error.details` at all. error.v1 types `details` as an open object
	// (additionalProperties: true), so this is not a schema change either
	// and needs no pin bump.
	var budgetRefusal contextfabric.AnswerBudgetRefusal
	if errors.As(err, &budgetRefusal) {
		details := map[string]any{
			"overrun":              string(budgetRefusal.Overrun),
			"measured_items":       budgetRefusal.MeasuredItems,
			"measured_bytes":       budgetRefusal.MeasuredBytes,
			"max_items":            budgetRefusal.MaxItems,
			"max_serialized_bytes": budgetRefusal.MaxSerializedBytes,
			"question_family":      string(budgetRefusal.Family),
			"retry_attempted":      budgetRefusal.RetryAttempted,
		}
		// OMITTED, not served as "none". A continuation is advice; when no
		// axis could be named there is no advice, and an object saying
		// `{"axis": "none"}` invites a consumer to render "narrow by: none".
		// Absence is the honest encoding of "we have nothing to suggest",
		// and it is the same discipline the answer contract uses elsewhere:
		// missing is not a value.
		if budgetRefusal.NarrowerContinuationAxis != contextfabric.NarrowingContinuationNone &&
			budgetRefusal.NarrowerContinuationAxis != "" {
			details["narrower_continuation"] = map[string]any{
				"family": string(budgetRefusal.Family),
				"axis":   string(budgetRefusal.NarrowerContinuationAxis),
			}
		}
		a.writeContextFabricFailure(w, r, err, contextFabricClassBudgetRefusal, http.StatusRequestEntityTooLarge, "invalid_request", "The Context Fabric answer did not fit the response budget", false, details)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		a.writeContextFabricFailure(w, r, err, contextFabricClassDeadline, http.StatusGatewayTimeout, "upstream_unavailable", "The Context Fabric investigation timed out", true, nil)
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
	//
	// The classification travels with the sentinel, not with the retired
	// one's name: an operator alerting on this signal must read "these
	// bounds are not answerable", never "historical questions are
	// refused", which CHAOS-3781 made false.
	if errors.Is(err, contextfabric.ErrInvalidTimeBound) {
		a.writeContextFabricFailure(w, r, err, contextFabricClassTimeBound, http.StatusBadRequest, "invalid_request", "The requested time is not answerable: it must not be in the future, and a range must be narrower than the supported window", false, nil)
		return
	}
	// Rate limiting: contextfabric.ErrRateLimited is the vendor-neutral
	// classification a graph backend adapter wraps its own rate-limit
	// error into -- falkorgraph.ErrRateLimited is DECLARED wrapping this
	// sentinel (falkorgraph/config.go), so it carries the pair however it
	// is constructed, and falkorgraph/client.go's neutralClass table is the
	// specification that declaration is tested against.
	// ErrModelRateLimited is the pre-existing, distinct classification for
	// the model runtime (ADR 0008). Both mean the same thing to a caller:
	// back off and retry later.
	//
	// REPORTED LIMIT, so this comment stays true (CHAOS-4874): the MAPPING
	// exists, but at this commit NOTHING IN falkorgraph PRODUCES a
	// rate-limit error -- classifyFalkorError has no rate-limit arm,
	// because no FalkorDB rate-limit message has been verified live and
	// every other text in that switch has been. So this branch is reachable
	// for the model runtime and for a caller-supplied
	// falkorgraph.ErrRateLimited, and NOT yet by a real FalkorDB
	// backpressure response. Before CHAOS-4874 this comment claimed the
	// wrap already happened; it did not, and a graph rate limit would have
	// answered 500.
	if errors.Is(err, contextfabric.ErrRateLimited) || errors.Is(err, contextfabric.ErrModelRateLimited) {
		a.writeContextFabricFailure(w, r, err, contextFabricClassRateLimited, http.StatusTooManyRequests, "rate_limited", "Context Fabric is rate limited; retry later", true, nil)
		return
	}
	// contextfabric.ErrUnavailable already covers both a graph/model
	// dependency being down AND a graph backend rejecting ACR's own
	// service credential: falkorgraph's neutralClass table pairs
	// falkorgraph.ErrUnauthorized (its WRONGPASS/NOAUTH classification)
	// with ErrUnavailable, so an ACR-side credential problem is never
	// presented to the caller as "you are unauthorized". The same table
	// puts falkorgraph's genuinely UNCLASSIFIED residual here too -- a
	// connection refused, a mid-handshake EOF, a TLS alert -- which before
	// CHAOS-4874 carried no sentinel at all and fell through to the generic
	// 500 below. ErrModelUnavailable joins the same bucket for the model
	// runtime.
	if errors.Is(err, contextfabric.ErrUnavailable) || errors.Is(err, contextfabric.ErrModelUnavailable) {
		a.writeContextFabricFailure(w, r, err, contextFabricClassUnavailable, http.StatusServiceUnavailable, "upstream_unavailable", "Context Fabric is temporarily unavailable", true, nil)
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
		a.writeContextFabricRejectionError(w, r, err, contextFabricClassInterpretRejected, "interpretation_rejected", "Context Fabric's interpretation of the question violated a v1 bound")
		return
	}
	if errors.Is(err, contextfabric.ErrSynthesisRejected) {
		a.writeContextFabricRejectionError(w, r, err, contextFabricClassSynthesisRejected, "synthesis_rejected", "Context Fabric's synthesized answer violated a v1 bound")
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
		a.writeContextFabricFailure(w, r, err, contextFabricClassModelOutput, http.StatusBadGateway, "upstream_invalid_output", "Context Fabric produced an invalid answer; retry", true, nil)
		return
	}
	// CHAOS-3810/CHAOS-3811: the two ACR-side invariant breaches. Both stay
	// 500/non-retryable -- reaching either means ACR produced something it
	// should not have, and no amount of retrying fixes that -- but they are
	// now NAMED instead of anonymous. ErrNoInvestigationSubjects in
	// particular was the whole of CHAOS-3810's observable symptom: every
	// real-corpus investigation landed in the fallthrough below, which is
	// exactly why nobody could tell an unresolved subject apart from a
	// genuine ACR fault.
	if errors.Is(err, contextfabric.ErrNoInvestigationSubjects) {
		a.writeContextFabricFailure(w, r, err, contextFabricClassNoSubjects, http.StatusInternalServerError, "internal_error", "Context Fabric investigation failed", false, nil)
		return
	}
	if errors.Is(err, contextfabric.ErrInvalidResult) {
		a.writeContextFabricFailure(w, r, err, contextFabricClassInvalidResult, http.StatusInternalServerError, "internal_error", "Context Fabric investigation failed", false, nil)
		return
	}
	a.writeContextFabricFailure(w, r, err, contextFabricClassUnclassified, http.StatusInternalServerError, "internal_error", "Context Fabric investigation failed", false, nil)
}

// writeContextFabricFailure is the single exit for every failed
// investigation: it emits ONE structured log line carrying the bounded stage
// and classification alongside the pre-existing failure_class, then writes
// the caller-facing error.
//
// Both new fields are closed enums (contextfabric.InvestigationStage and the
// contextFabricClass* constants above). Nothing derived from the error's own
// message is logged, at any level -- the whole point is that an operator can
// tell a resolution failure from a fact-read failure from an ACR-side
// invariant breach WITHOUT anyone ever being tempted to log the raw
// dependency text to find out.
//
// Level tracks who has to act: 5xx (and anything unclassified) is ACR's
// problem and logs at Error; a classified 4xx is the caller's and logs at
// Warn, so a spike in refusals is still visible without drowning the error
// stream.
func (a *App) writeContextFabricFailure(w http.ResponseWriter, r *http.Request, err error, classification string, status int, code, message string, retryable bool, details map[string]any) {
	a.logContextFabricFailure(r, err, classification, status)
	writeError(w, r, status, code, message, retryable, details)
}

// logContextFabricFailure emits the one structured failure line. It is split
// out from writeContextFabricFailure so the panic branch can record a failure
// whose RESPONSE is deliberately skipped (a canceled request has no reader)
// without the log depending on whether anything was written.
//
// The logger is called with context.WithoutCancel: slog handlers may consult
// the context, and this line must survive the very cancellation that makes it
// worth having. RequestID is read from the same context, which carries its
// values either way.
//
// CHAOS-4088: an "unclassified" line ONLY names the taxonomy's own gap, never
// what specifically fell into it. failure_error_type closes that for the
// residual unclassified branch (the population left after CHAOS-4077 carved
// the never-projected-org case out of it): the Go %T type name of the
// innermost error in err's chain, e.g. "*net.OpError" -- never err.Error()
// text, which is exactly the raw dependency content this whole classifier
// exists to keep out of a log line. It is omitted for every classified
// branch, where the sentinel already names the cause.
func (a *App) logContextFabricFailure(r *http.Request, err error, classification string, status int) {
	stage, _ := contextfabric.FailureStage(err)
	level := slog.LevelWarn
	if status >= http.StatusInternalServerError || classification == contextFabricClassUnclassified {
		level = slog.LevelError
	}
	fields := []any{
		"request_id", RequestID(r.Context()),
		"failure_class", contextFabricInvestigationFailureName,
		"failure_stage", string(stage),
		"failure_classification", classification,
		"http_status", status,
	}
	if classification == contextFabricClassUnclassified {
		fields = append(fields, "failure_error_type", contextFabricInnermostErrorType(err))
	}
	// CHAOS-4355 follow-up: violated_bound was previously ONLY in the HTTP
	// response body (writeContextFabricRejectionError below), never logged
	// server-side -- the 2026-08-27 diagnosis session had to re-derive it
	// from source instead of the live 422's own logs. Bound is always one
	// of the fixed, ACR-owned registry names (never model output, per
	// ModelBoundViolation's own doc comment), so it is exactly as safe to
	// log as failure_classification above. claim_index is included only
	// when the violated bound is claim-scoped (>= 0); omitted otherwise so
	// a driver/finding/interpretation bound doesn't carry a meaningless -1.
	var violation *contextfabric.ModelBoundViolation
	if errors.As(err, &violation) {
		fields = append(fields, "violated_bound", violation.Bound)
		if violation.ClaimIndex >= 0 {
			fields = append(fields, "claim_index", violation.ClaimIndex)
		}
	}
	// CHAOS-4522: violated_bound above covers only the SUBSET of synthesis
	// rejections attributable to a contracts/v1 model-facing bound. Every
	// business-rule and grounding rejection -- the majority, and the whole
	// class this ticket was filed on -- carried no name at all, so a live
	// 422 said "synthesis_rejected" and nothing more, and diagnosing it
	// required re-running the server with instrumentation added after the
	// fact. rejection_reason names the exact ValidateAgainst statement from
	// a CLOSED vocabulary (contextfabric.SynthesisRejectionReason); it is
	// as content-safe as failure_classification, since every value is a
	// fixed identifier chosen at the rejecting statement, never model
	// output. Emitted only for a rejection error, so an unrelated failure
	// never carries a meaningless "unclassified".
	var rejection *contextfabric.SynthesisRejection
	if errors.As(err, &rejection) {
		fields = append(fields, "rejection_reason", string(contextfabric.SynthesisRejectionReasonOf(err)))
	}
	// The INTERPRET-side half of the same field, on the same line, for the
	// same reason. Without it a 422 interpretation_rejected reaching this
	// sink says only failure_classification=interpretation_rejected and
	// nothing more -- which is the exact complaint that produced this
	// ticket, one layer further out than the model decision line.
	//
	// It matters most for the two fact_registry producers: those rejections
	// never pass through genkitruntime at all, so the model decision line
	// (which is where the interpret reason otherwise appears) is not even
	// emitted for them. This event is their ONLY telemetry surface.
	//
	// Mutually exclusive with the synthesis branch above in practice -- one
	// error is not both -- so the two never write the same key twice.
	var interpretationRejection *contextfabric.InterpretationRejection
	if errors.As(err, &interpretationRejection) {
		fields = append(fields, "rejection_reason", string(contextfabric.InterpretationRejectionReasonOf(err)))
	}
	// CHAOS-4726: a rejected synthesis never reaches stage 3, so
	// RecordPlanNarrowing's "context fabric plan narrowing" assembled_result
	// line -- the only place the narrowing basis was previously visible --
	// is structurally absent for every synthesis_rejected 422 (40/40 live,
	// proven on org 70d529e0). narrowing_basis is stage 1's always-declared
	// order; narrowing_last_stage/narrowing_last_basis name whichever of
	// stage 1/2 most recently narrowed before synthesis was invoked, empty
	// when neither did. Present for ANY error at or after the synthesis
	// call site (Engine.Investigate), not only a classified rejection --
	// the state is equally true and equally diagnosable for an upstream
	// synthesis fault.
	if snapshot, ok := contextfabric.SynthesisNarrowingSnapshotOf(err); ok {
		fields = append(fields,
			"narrowing_basis", string(snapshot.DeclaredBasis),
			"narrowing_last_stage", string(snapshot.LastStage),
			"narrowing_last_basis", string(snapshot.LastBasis),
		)
	}
	a.logger.Log(context.WithoutCancel(r.Context()), level, "context fabric investigation failed", fields...)
}

// contextFabricInnermostErrorType walks err's Unwrap chain to the deepest
// node and returns its Go type name (%T). It never reads Error() text, so
// the result is corpus-safe by construction: a type name is static per
// error implementation, never a rendering of the dependency's own message,
// an org identifier, or any other request-derived content.
//
// A multi-error node (Unwrap() []error, e.g. a fmt.Errorf("%w: %w", ...)
// double-wrap) ends the walk there rather than picking one branch to
// descend into arbitrarily -- errors.Unwrap only follows the single-error
// Unwrap() error shape, so the walk naturally stops at the first node that
// doesn't implement it, and that node's own type is still real signal.
func contextFabricInnermostErrorType(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return fmt.Sprintf("%T", err)
		}
		err = next
	}
}

// writeContextFabricRejectionError writes the shared 422 response shape for
// ErrInterpretationRejected/ErrSynthesisRejected, attaching
// details.violated_bound only when err carries a
// *contextfabric.ModelBoundViolation.
func (a *App) writeContextFabricRejectionError(w http.ResponseWriter, r *http.Request, err error, classification, code, message string) {
	var violation *contextfabric.ModelBoundViolation
	var details map[string]any
	if errors.As(err, &violation) {
		details = map[string]any{"violated_bound": violation.Bound}
	}
	// CHAOS-4522: rejection_reason is deliberately NOT disclosed in the
	// response body, only in the server-side failure log below (codex R2
	// findings 2 and 3). Publishing it here would put a closed vocabulary
	// on an externally visible surface while `details` stays an untyped map
	// in the Go contract and an open object in the JSON Schema/OpenAPI --
	// so a generated consumer could neither discover the vocabulary nor
	// have parity checks detect drift in it. Doing that properly is a
	// contract widening (Go type + schema + OpenAPI + MCP + fixtures +
	// parity tests, and an ask-dev pin bump), which is a deliberate,
	// separately-ratified change and not a side effect of a defect fix.
	//
	// Nothing is lost for the audience the field exists for: AGENTS.md's
	// diagnosis-in-artifacts rule is satisfied by "a trace event, a report
	// field, or a structured log field", and the operator diagnosing a 422
	// has the log line, which carries the reason, the group size, and the
	// request id that ties them to this response.
	a.writeContextFabricFailure(w, r, err, classification, http.StatusUnprocessableEntity, code, message, true, details)
}

// contextFabricItemCounts is the CHAOS-4523 item-budget breakdown: one
// field per closed-vocabulary category contextFabricResultItems sums, so a
// 413 -- or a bytes-exceeded response, which logs the same breakdown for
// consistency -- is diagnosable from its own log line and error details
// without re-running with instrumentation added after the fact (CANONICAL
// ARCHITECTURE's diagnosis-in-artifacts rule).
//
// CHAOS-4636: the struct, its Total/Budgeted arithmetic and the counting
// itself MOVED to internal/contracts/v1 (context_fabric_response_budget.go)
// so the ENGINE can charge the identical numbers before it validates and
// persists an answer. internal/api imports internal/contextfabric and not
// the reverse, so a measurement defined here is unreachable from below; only
// internal/contracts/v1 is imported by both planes. This alias keeps every
// existing call site in this package reading the same way while there is now
// exactly ONE definition -- the route's gate is an assertion over the
// numbers the engine already checked, not a second, drifting measurement.
//
// The Paths-exclusion rationale that used to live on budgeted() here is
// preserved verbatim beside the code that now performs it. In short: a
// ContextFabricRelationshipPath is graph-evidence provenance whose count
// scales with graph density around the resolved subject, not with the size
// of the answer a client renders, and charging one path the same as one
// claimed fact 413'd plain repository-status questions on provenance nobody
// renders (CHAOS-4450 Run J Wall C). Both call sites still record
// usage.Items = Total() (truthful accounting -- Manager.Usage() writes it
// verbatim into the org/credential window) and widen override.MaxItems by
// Paths, so the GATE condition `total <= MaxItems+Paths` stays algebraically
// identical to `Budgeted() <= MaxItems` without ever recording a number
// smaller than what was actually served.
type contextFabricItemCounts = contractsv1.ContextFabricResultItemCounts

// contextFabricResultItemCounts charges every collection the item budget
// covers. It delegates: see contextFabricItemCounts above for why the
// definition is not here.
func contextFabricResultItemCounts(result contextfabric.InvestigationResult) contextFabricItemCounts {
	return contractsv1.CountContextFabricResultItems(result)
}

// contextFabricResultItems is contextFabricResultItemCounts(result).Total()
// -- kept as a standalone function (CHAOS-3755 M3 probe,
// TestContextFabricResultItemsCountsClaimedFacts) so the "every category
// counts toward measured size" contract that test pins stays expressed
// exactly as before. It is no longer what is charged against ACR_MAX_ITEMS
// -- see contractsv1.ContextFabricResultItemCounts.Budgeted.
func contextFabricResultItems(result contextfabric.InvestigationResult) int {
	return contextFabricResultItemCounts(result).Total()
}

// marshalContextFabricResponse encodes a Context Fabric response payload
// and reports its measured size unconditionally -- unlike encodeBounded
// (read_decode.go), which discards the encoded bytes on an over-budget
// result, leaving nothing to disclose (CHAOS-4355 response-bound
// follow-up). Both context-fabric response routes (the investigation POST
// handler and the result GET handler) share this so a measurement, once
// taken, is never thrown away before the caller sees why their response
// did not fit.
func marshalContextFabricResponse(payload any) (encoded []byte, measuredBytes int64, err error) {
	encoded, err = json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	return encoded, int64(len(encoded)), nil
}

// MarshalContextFabricResponse is marshalContextFabricResponse's exported
// form (CHAOS-4386): the trial harness (internal/runtime/hosted) calls
// investigator.Investigate() in-process and never traverses this route's
// own limits.Claim.CompleteWithBudget gate, so every measured "did this
// response fit" number the harness reports up to now was the responder's
// raw model-completion output_bytes -- a smaller upstream proxy, never the
// assembled InvestigationResult (Rows, evidence, drivers) this gate
// actually bounds. Exporting this lets the harness measure a case's
// per-result byte count with the EXACT SAME encoder this route uses,
// instead of a second, independently-drifting json.Marshal call -- see
// this ticket's own reuse-not-duplicate instruction.
func MarshalContextFabricResponse(payload any) (encoded []byte, measuredBytes int64, err error) {
	return marshalContextFabricResponse(payload)
}

// contextFabricResponseBudgetFields (CHAOS-4540) is the closed field set
// shared by the exceed-path WARN and the passing-path INFO measurement line
// below -- the SAME numbers either way, so a passing run's headroom is
// exactly as diagnosable as a failing run's overrun. estimatedTokens is
// reported for diagnostics only -- these routes deliberately do not gate on
// it (see the usage.Tokens comment at both call sites). counts carries the
// CHAOS-4523 per-category breakdown so a reader can see exactly which
// categories drove the measured count -- e.g. distinguishing 27 provenance
// Paths (not charged, see contractsv1.ContextFabricResultItemCounts.
// Budgeted) from 30 genuine ClaimedFacts/Drivers (charged) -- without
// re-running the request with a debugger attached, on a pass exactly as
// much as on a 413.
func contextFabricResponseBudgetFields(maxItems int, measuredBytes, maximumBytes, estimatedTokens int64, counts contextFabricItemCounts) []any {
	return []any{
		"measured_bytes", measuredBytes, "max_serialized_bytes", maximumBytes,
		// measured_items is the TRUTHFUL total (counts.Total(), Paths
		// included) -- the same value recorded as usage.Items -- not the
		// budgeted() subset the gate actually compares against; that
		// subset and its effective ceiling are max_items+items_paths, so
		// a reader can derive "budgeted = measured_items - items_paths"
		// without a false accounting record (CHAOS-4523 codex P2 finding).
		"estimated_tokens", estimatedTokens, "measured_items", counts.Total(),
		"max_items", maxItems, "max_items_effective_for_paths_exclusion", maxItems + counts.Paths,
		"items_candidates", counts.Candidates, "items_drivers", counts.Drivers, "items_paths", counts.Paths,
		"items_remaining_work", counts.RemainingWork, "items_readiness_gaps", counts.ReadinessGaps,
		"items_conflicts", counts.Conflicts, "items_claimed_facts", counts.ClaimedFacts, "items_cohort_members", counts.CohortMembers,
	}
}

// logContextFabricResponseBudgetExceeded is the CHAOS-4355 response-bound
// follow-up's decision-basis telemetry (CANONICAL ARCHITECTURE's
// diagnosis-in-artifacts rule): every time a Context Fabric response fails
// to fit, the measured size and which check rejected it -- "bytes" for the
// MaxSerializedBytes gate (encodeBounded's replacement above), "items" for
// the CompleteUsage item-count budget -- land in the SAME log line the
// request_id already carries, so the defect is diagnosable from the run's
// own artifacts without re-running with instrumentation added after the
// fact. See writeError's "details" map on the caller side for the
// caller-visible half of this same disclosure.
func (a *App) logContextFabricResponseBudgetExceeded(r *http.Request, reason string, measuredBytes, maximumBytes, estimatedTokens int64, counts contextFabricItemCounts) {
	fields := append([]any{
		"request_id", RequestID(r.Context()), "failure_class", "context_fabric_response_budget", "reason", reason,
	}, contextFabricResponseBudgetFields(a.config.MaxItems, measuredBytes, maximumBytes, estimatedTokens, counts)...)
	a.logger.WarnContext(r.Context(), "context fabric response exceeded service limits", fields...)
}

// logContextFabricResponseBudgetMeasured (CHAOS-4540) is the exceed line's
// success-path counterpart: emitted UNCONDITIONALLY on every assembled
// answer that passes both budget gates, carrying the SAME closed fields at
// INFO rather than WARN. Before this, the only place measured_items/
// measured_bytes/estimated_tokens were ever logged was a run that had
// already failed -- so a passing configuration's own margin could only ever
// be stated as "it fits", never as a number, and #328's item-count
// regression from 40-42 down to under 30 had nowhere to be read off from a
// successful run. The exceed-path WARN above is unchanged in name, level and
// field set, so existing consumers of it are unaffected.
func (a *App) logContextFabricResponseBudgetMeasured(r *http.Request, measuredBytes, maximumBytes, estimatedTokens int64, counts contextFabricItemCounts) {
	fields := append([]any{"request_id", RequestID(r.Context())},
		contextFabricResponseBudgetFields(a.config.MaxItems, measuredBytes, maximumBytes, estimatedTokens, counts)...)
	a.logger.InfoContext(r.Context(), "context fabric response measured", fields...)
}
