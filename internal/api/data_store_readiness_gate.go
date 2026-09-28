package api

import "net/http"

// dataStoresReady (CHAOS-6745) runs every configured a.dataStoreChecks entry
// live, on this request's own context. It returns true when every check
// passed (the caller should proceed). On the first failing check it writes
// the typed, retryable "store_unavailable" 503 itself and returns false --
// the caller must return immediately without writing anything else.
//
// This never gates /readyz, and it never gates a route that does not
// actually depend on the failing store: it is called only from the
// ClickHouse-backed handlers (see requireDataStoresReady for the routes
// wrapped with it, and ContextFabricInvestigationHandler for the one that
// calls this directly, inside its own body, AFTER auth -- same placement
// rule as that handler's nil-investigator check, and for the identical
// reason: an unauthenticated caller must never learn a store is down).
//
// The check is loud by construction, not a silently-swallowed fallback: it
// logs at Warn (matching handleReady's own "readiness check failed"
// convention) AND goes through writeError, which the existing
// accessLogMiddleware/ObserveRequest path already turns into a per-request
// observability event for every denial code (see denialForError) -- so a
// ClickHouse outage is visible on the SAME dashboards a rate-limit or auth
// denial already is, without inventing a second reporting path.
func (a *App) dataStoresReady(w http.ResponseWriter, r *http.Request) bool {
	for _, check := range a.dataStoreChecks {
		if err := check.Check(r.Context()); err != nil {
			a.logger.WarnContext(r.Context(), "data store readiness check failed",
				"request_id", RequestID(r.Context()),
				"store", check.Name(),
				"failure_class", "data_store_readiness_check",
			)
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable",
				"A required data store is temporarily unavailable", true, nil)
			return false
		}
	}
	return true
}

// requireDataStoresReady wraps next with the same dataStoresReady check a
// route without its own auth/nil-dependency handler body can use directly.
// See dataStoresReady's doc comment for the placement rule this must
// respect: it must only ever wrap the innermost handler, inside
// protectedRuntimeHandler, never outside it.
func (a *App) requireDataStoresReady(next http.Handler) http.Handler {
	if len(a.dataStoreChecks) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.dataStoresReady(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}
