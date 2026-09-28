package api

import "net/http"

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
