package api

import (
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// responseOwnerMiddleware creates the request's response lifetime owner
// immediately outside recoveryMiddleware. That placement keeps the owner
// alive while recovery writes a 500 body, including when the route panics.
// The owner is completed only by the scope that creates it. If a caller has
// already installed one, this middleware borrows it and leaves completion to
// that enclosing scope.
func (a *App) responseOwnerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := contextfabric.WorkItemResponseOwnerFromContext(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}

		ctx, owner := contextfabric.NewWorkItemResponseOwnerContext(r.Context())
		defer owner.Complete()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
