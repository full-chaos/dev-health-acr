package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// requestIDLogAttrs is the request id of ctx as a sanitized log attribute, or
// nothing when the context carries none. It reads the context only.
func requestIDLogAttrs(ctx context.Context) []any {
	if requestID, ok := observability.RequestIDFromContext(ctx); ok {
		return []any{"request_id", contextfabric.SanitizeLogAttr(string(requestID))}
	}
	return nil
}
