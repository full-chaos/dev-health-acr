package api

import (
	"context"
	"net/http"
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

func requestObservation(operation observability.Operation, status int, denial observability.DenialClass, duration time.Duration) observability.RequestObservation {
	observation := observability.RequestObservation{Operation: operation, StatusClass: statusClass(status), Outcome: observability.OutcomeFailure, Denial: denial, Duration: duration}
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		observation.Outcome = observability.OutcomeSuccess
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
		observation.Outcome = observability.OutcomeDenied
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
	default:
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
