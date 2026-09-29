package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// logDevicePreview records the decision basis of a device approval preview:
// which scopes the page was told the grant asked for, and where that answer
// came from. Scope names are a closed vocabulary; the user code and any id are
// never logged. Without it a wrong or defaulted scope answer that still
// returns 200 leaves nothing at Info to diagnose it from.
func (a *App) logDevicePreview(r *http.Request, preview auth.DeviceApprovalPreview) {
	a.logger.InfoContext(r.Context(), "device approval preview",
		"requested_scopes", strings.Join(preview.RequestedScopes, " "),
		"requested_scopes_source", preview.RequestedScopesSource,
		"request_id", RequestID(r.Context()))
}

// logDevicePreviewFailure records why a preview failed when the cause is the
// grant lookup (a storage failure). The wrapped cause stays out of the log.
func (a *App) logDevicePreviewFailure(r *http.Request, err error) {
	if errors.Is(err, auth.ErrDeviceGrantLookup) {
		a.logger.WarnContext(r.Context(), "device approval preview scope lookup failed",
			"reason", "device_grant_lookup_failed",
			"request_id", RequestID(r.Context()))
	}
}
