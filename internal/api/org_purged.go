package api

import (
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// writeOrgPurged answers a request whose write the database refused because
// its organization was purged while the request was in flight. The answer is
// the one every later request of that organization gets, since the purge
// removed its credentials. It reports whether it wrote the response.
func (a *App) writeOrgPurged(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, storage.ErrOrgPurged) {
		return false
	}
	a.logger.WarnContext(r.Context(), "request refused: organization was purged", "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "failure_class", "org_purged")
	writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
	return true
}
