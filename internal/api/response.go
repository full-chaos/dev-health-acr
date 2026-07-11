package api

import (
	"encoding/json"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

type statusWriter struct {
	http.ResponseWriter
	status     int
	bytes      int
	denialCode string
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func (w *statusWriter) SetDenialCode(code string) { w.denialCode = code }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool, details map[string]any) {
	if marker, ok := w.(interface{ SetDenialCode(string) }); ok {
		marker.SetDenialCode(code)
	}
	writeJSON(w, status, contractsv1.ErrorEnvelope{
		SchemaVersion: contractsv1.ErrorSchema,
		RequestID:     RequestID(r.Context()),
		Error: contractsv1.ErrorDetail{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
			Retryable:  retryable,
			Details:    details,
		},
	})
}

func denialForError(code string) observability.DenialClass {
	switch code {
	case "":
		return observability.DenialNone
	case "invalid_token":
		return observability.DenialAuthentication
	case "insufficient_scope":
		return observability.DenialPermissionScope
	case "repo_forbidden":
		return observability.DenialRepositoryScope
	case "feature_not_enabled":
		return observability.DenialLicense
	case "rate_limited":
		return observability.DenialRateLimit
	default:
		return observability.DenialUnknown
	}
}
