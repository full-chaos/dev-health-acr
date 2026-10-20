package api

import (
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// writeEntitlementLookupError answers a failed entitlement lookup at every
// entitlement gate (capabilities, the runtime and read gates, the service model
// gate). Dev Health
// positively saying the organization does not exist means the credential
// belongs to an organization that is gone, so it is an invalid credential
// (401), the same answer every auth path gives. Any other failure is an
// outage of the lookup and stays a retryable 503. Each class logs once,
// without the organization id.
func (a *App) writeEntitlementLookupError(w http.ResponseWriter, r *http.Request, err error) {
	class := "entitlement_unavailable"
	if errors.Is(err, entitlements.ErrOrganizationUnknown) {
		class = "entitlement_organization_unknown"
	}
	a.logger.ErrorContext(r.Context(), "entitlement lookup failed", "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "failure_class", class, "reason", entitlements.RejectionReason(err))
	if class == "entitlement_organization_unknown" {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Entitlement service is temporarily unavailable", true, nil)
}
