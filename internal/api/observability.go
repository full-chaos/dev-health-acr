package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/observability"
)

type operationContextKey struct{}

func (a *App) InstrumentedOperationHandler(operation observability.Operation, next http.Handler) http.Handler {
	instrumented := a.InstrumentedHandler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), operationContextKey{}, operation)
		instrumented.ServeHTTP(w, r.WithContext(ctx))
	})
}

// writeFailed (CHAOS-4330) overrides a 2xx/3xx status's derived
// OutcomeSuccess back to OutcomeFailure: `status` is decided before the
// body write that can still fail (e.g. http.Server.WriteTimeout firing
// mid-write), so a handler that intended 200 and then had its response
// cut off must not be counted as a success in this metric either -- the
// same reasoning accessLogMiddleware's own "request completed" log line
// applies.
func requestObservation(operation observability.Operation, status int, denial observability.DenialClass, duration time.Duration, writeFailed bool) observability.RequestObservation {
	observation := observability.RequestObservation{Operation: operation, StatusClass: statusClass(status), Outcome: observability.OutcomeFailure, Denial: denial, Duration: duration}
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		observation.Outcome = observability.OutcomeSuccess
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
		observation.Outcome = observability.OutcomeDenied
	}
	if writeFailed {
		observation.Outcome = observability.OutcomeFailure
	}
	return observation
}

func requestOperation(request *http.Request) observability.Operation {
	if operation, ok := request.Context().Value(operationContextKey{}).(observability.Operation); ok {
		return operation
	}
	switch request.URL.Path {
	case "/healthz":
		return observability.OperationHealth
	case "/readyz":
		return observability.OperationReadiness
	case "/api/v1/agent-context/capabilities":
		return observability.OperationCapabilities
	case "/api/v1/agent-context/context-packets":
		return observability.OperationContext
	case "/api/v1/agent-context/episodes":
		return observability.OperationEpisode
	default:
		if strings.HasPrefix(request.URL.Path, "/api/v1/agent-context/evidence/") {
			return observability.OperationEvidence
		}
		return observability.OperationUnknown
	}
}

func statusClass(status int) observability.HTTPStatusClass {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return observability.HTTPStatus2xx
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
		return observability.HTTPStatus4xx
	case status >= http.StatusInternalServerError:
		return observability.HTTPStatus5xx
	default:
		return observability.HTTPStatusUnknown
	}
}
