package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The direct data route group of CHAOS-7036 (section E.5). CHAOS-7071 (S0)
// registers the routes in app.go with their full protection chain -- bearer
// only (no web assertions), scope, rate class, client version, entitlement --
// and a typed "not implemented yet" answer. Each handler lives in its own file
// (context_fabric_data_<name>_route.go), so the slices that fill them in
// parallel (S1a: catalog, subjects, operations; S2: facts) never edit the
// same file. Every handler that reads data must pass the direct-read subject
// gate (CHAOS-7071, package directread) before it reads.
//
// Scopes and rate classes: run_operation needs the separate data:read scope
// and the Data rate class (decision K4); the catalog, subject lookup and fact
// read are context:read, Context class.
//
// Each path is a single-line const so ci/discover_acr_routes.go resolves it
// (it reads only that shape) and the route inventory sees every route.
const ContextFabricDataCatalogPath = "/api/v1/context-fabric/data/catalog"

const ContextFabricDataSubjectsPath = "/api/v1/context-fabric/data/subjects"

const ContextFabricDataFactsPath = "/api/v1/context-fabric/data/facts"

const ContextFabricDataOperationsPath = "/api/v1/context-fabric/data/operations"

// contextFabricDataNotImplementedReason is the details.reason of a stub
// answer. The error code is the existing feature_not_enabled, so the error
// contract's closed code set does not change in S0.
const contextFabricDataNotImplementedReason = "not_implemented"

// writeContextFabricDataNotImplemented is the stub answer of a route whose
// slice has not landed. It runs only after authentication, scope, rate
// limit, client version and entitlement all passed.
func writeContextFabricDataNotImplemented(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotImplemented, "feature_not_enabled", "This direct data route is not implemented yet", false, map[string]any{"reason": contextFabricDataNotImplementedReason})
}

// contextFabricDataNotConfiguredReason is the details.reason of the
// operations route when run_operation cannot serve in this deployment: the
// internal query service URL is not configured, or the operation policy
// artifact did not load (design E.5). Same code path and error code as the
// S0 stub, so the closed error code set does not change.
const contextFabricDataNotConfiguredReason = "data_query_not_configured"

// contextFabricDataRequestBytes bounds a direct data request body. It is the
// ops query service body limit (16 KiB): no direct data request needs more.
const contextFabricDataRequestBytes = int64(directread.MaxQueryRequestBytes)

// contextFabricDataResponseBytes bounds a direct data response: the largest
// data payload a caller may ask for (max_bytes cap) plus the fixed envelope.
const contextFabricDataResponseBytes = int64(directread.MaxOperationMaxBytes + 64*1024)

// DataOperationRunner is run_operation's pipeline
// (*directread.OperationRunner implements it).
type DataOperationRunner interface {
	Run(ctx context.Context, principal storage.Principal, request directread.OperationRequest) (directread.OperationResponse, error)
}

// DataSubjectFinder is find_subjects' read (*directread.SubjectLookup
// implements it).
type DataSubjectFinder interface {
	Find(ctx context.Context, principal storage.Principal, request directread.FindRequest) (directread.FindResponse, error)
}

func (a *App) dataCatalogue() *directread.Catalogue {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.DataCatalogue
}

// dataOperations returns the runner only when it can serve: a runner AND
// its loaded catalogue (design E.5). Never a typed nil.
func (a *App) dataOperations() DataOperationRunner {
	if a.runtime == nil || a.runtime.DataCatalogue == nil || storage.IsNil(a.runtime.DataOperations) {
		return nil
	}
	return a.runtime.DataOperations
}

// dataGateComposed reports the S0 subject gate over a real graph. Without
// it run_operation cannot authorize a subject, so the operations route
// answers 503 and run_operation is not advertised.
func (a *App) dataGateComposed() bool {
	return a.runtime != nil && a.runtime.DirectReadGate != nil
}

func (a *App) dataSubjects() DataSubjectFinder {
	if a.runtime == nil || storage.IsNil(a.runtime.DataSubjects) {
		return nil
	}
	return a.runtime.DataSubjects
}

// writeContextFabricDataNotConfigured answers the operations route when
// run_operation cannot serve (after the whole protection chain passed).
func writeContextFabricDataNotConfigured(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotImplemented, "feature_not_enabled", "Direct data operations are not configured in this deployment", false, map[string]any{"reason": contextFabricDataNotConfiguredReason})
}

// writeContextFabricDataDecodeError answers a request body that is not one
// JSON object of the route's shape (unknown fields refused) or is too large.
func writeContextFabricDataDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusBadRequest
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		status = http.StatusRequestEntityTooLarge
	}
	writeError(w, r, status, "invalid_request", "Direct data request is invalid", false, map[string]any{"reason": "malformed_request"})
}

// writeContextFabricDataInternal answers a direct read that could not keep
// its own authorization rules: a gate decision presented after it expired,
// presented twice, or never issued to this caller. That is a defect of the
// tool, never a caller refusal, so it is an internal error with a fixed
// body -- not denied_or_not_found.
func (a *App) writeContextFabricDataInternal(w http.ResponseWriter, r *http.Request, route string) {
	a.logger.ErrorContext(r.Context(), "context fabric direct data internal error", "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "route", route, "failure_class", "authorization_contract")
	writeError(w, r, http.StatusInternalServerError, "internal_error", "Direct data request failed", false, nil)
}

// writeContextFabricDataUnavailable answers a dependency the read needs that
// is down or absent (graph, gate, query plane). Fixed text, retryable.
func (a *App) writeContextFabricDataUnavailable(w http.ResponseWriter, r *http.Request, route, reason string) {
	a.logger.WarnContext(r.Context(), "context fabric direct data unavailable", "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "route", route, "reason", contextfabric.SanitizeLogAttr(reason))
	writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Direct data is temporarily unavailable", true, map[string]any{"reason": reason})
}

// isDirectReadContractError reports the three errors a direct read returns
// when the handler path broke the single-use proof rules.
func isDirectReadContractError(err error) bool {
	return errors.Is(err, directread.ErrAuthorizationExpired) || errors.Is(err, directread.ErrAuthorizationSpent) || errors.Is(err, directread.ErrUngatedRead)
}
