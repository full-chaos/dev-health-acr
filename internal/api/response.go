package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

type statusWriter struct {
	http.ResponseWriter
	status     int
	bytes      int
	denialCode string
	// writeErr (CHAOS-4330) is the first error a Write call returned, if
	// any -- e.g. http.Server.WriteTimeout firing mid-write on a slow
	// handler, or the client disconnecting. `status` alone cannot show
	// this: WriteHeader is called (and status recorded) BEFORE the body
	// write that can still fail, so a handler that decided 200 and then
	// had its response cut off would otherwise look identical, in this
	// writer's own state, to one that actually completed. accessLogMiddleware
	// reads this to keep "request completed" from claiming success the
	// client never received.
	//
	// KNOWN RESIDUAL GAP (codex review, CHAOS-4330): this only captures an
	// error the HANDLER's own Write call observed. Go's net/http server
	// wraps the connection in a buffered writer; a response small enough to
	// stay inside that buffer returns nil from every Write the handler
	// makes and is only actually flushed to the wire AFTER the handler
	// (and this middleware) has already returned, at which point a flush
	// failure is invisible to application code entirely -- the standard
	// library gives no hook to observe it. This does not weaken the fix
	// for what CHAOS-4330 actually reported: a real, several-KB investigation
	// response is large enough that its Write calls DO cross the buffer
	// boundary and DO surface a mid-write failure through this exact path
	// (confirmed empirically in the original incident). Closing the small-
	// response case fully would mean wrapping the raw net.Conn/Listener,
	// a much larger change than this ticket's own scope.
	writeErr error
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	return n, err
}

// classifyWriteError (CHAOS-4330) reduces a failed Write's error to a
// closed, low-cardinality bucket for logging -- never the raw error text.
// This repo's own observability rule (docs/observability.md: "Never add
// ... error text ... as an attribute or metric label") applies here just
// as much as it does to the observability snapshot path: a *net.OpError
// from a failed Write can carry a remote address or other transport
// incidental, not something to standing-log for every dropped connection.
func classifyWriteError(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, net.ErrClosed) {
		return "client_disconnected"
	}
	return "write_failed"
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
