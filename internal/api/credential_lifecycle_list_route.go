package api

import (
	"net/http"
	"strconv"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CredentialLifecycleListPath is the read-only credential listing for the Auth
// Control Plane reconciliation (CHAOS-11200).
const CredentialLifecycleListPath = "/internal/v1/credentials/lifecycle"

// credentialLifecycleListHandler is the listing's chain. The authenticator
// refuses an absent or malformed bearer and any web assertion before the
// credential lookup. Then, in order and each before the listing is read: the
// credential's class (known only from its row) must be workload, and its scope
// must be credential_lifecycle:read, which only the reconcile binding role
// carries. No client-version or entitlement check applies: the caller is a
// workload, not a user.
func (a *App) credentialLifecycleListHandler() http.Handler {
	if a.runtime == nil || a.authenticator == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	return a.authenticator.MiddlewareFor(false,
		a.requireWorkloadClass(
			a.authenticator.RequireScope(auth.ScopeCredentialLifecycleRead,
				LimitMiddleware(a.limits, limits.RequestClassAuth, http.HandlerFunc(a.handleCredentialLifecycleList)))))
}

func (a *App) requireWorkloadClass(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		if principal.ClientClass != storage.CredentialClientClassWorkload {
			a.recordReadAudit(r.Context(), principal, "credential_class_denied", "acr_credential_class", string(principal.ClientClass), "denied", nil)
			writeError(w, r, http.StatusForbidden, "credential_class_refused", "This route serves only a workload credential", false, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleCredentialLifecycleList(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok || principal.OrgID == "" {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	limit, after, valid := parseCredentialLifecycleQuery(r)
	if !valid {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500 and after a credential id; no other parameter is accepted", false, nil)
		return
	}
	if a.runtime == nil || storage.IsNil(a.runtime.CredentialLifecycle) {
		a.handleRuntimeUnavailable(w, r)
		return
	}
	// One extra item tells whether another page follows.
	items, err := a.runtime.CredentialLifecycle.ListCredentialLifecycle(r.Context(), principal.OrgID, after, limit+1)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "credential lifecycle listing failed", "request_id", RequestID(r.Context()), "failure_class", "credential_lifecycle_store")
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Credential listing is temporarily unavailable", true, nil)
		return
	}
	page := contractsv1.CredentialLifecycleList{SchemaVersion: contractsv1.CredentialLifecycleListSchema, Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		next := page.Items[limit-1].CredentialID
		page.NextAfter = &next
	}
	if page.Items == nil {
		page.Items = []contractsv1.CredentialLifecycleItem{}
	}
	if err := page.Validate(); err != nil {
		a.logger.ErrorContext(r.Context(), "credential lifecycle listing invalid", "request_id", RequestID(r.Context()), "failure_class", "credential_lifecycle_page")
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Credential listing is temporarily unavailable", true, nil)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// parseCredentialLifecycleQuery admits only limit and after, each at most once.
func parseCredentialLifecycleQuery(r *http.Request) (limit int, after string, valid bool) {
	limit = contractsv1.CredentialLifecycleListDefaultLimit
	query := r.URL.Query()
	for name, values := range query {
		if (name != "limit" && name != "after") || len(values) != 1 {
			return 0, "", false
		}
	}
	if values, present := query["limit"]; present {
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > contractsv1.CredentialLifecycleListMaxLimit {
			return 0, "", false
		}
		limit = parsed
	}
	if values, present := query["after"]; present {
		if !contractsv1.ValidCredentialLifecycleCursor(values[0]) {
			return 0, "", false
		}
		after = values[0]
	}
	return limit, after, true
}
