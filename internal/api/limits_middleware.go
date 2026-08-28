package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

type limitClaimContextKey struct{}

func LimitMiddleware(manager *limits.Manager, class limits.RequestClass, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Request controls are temporarily unavailable", true, nil)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		claim, decision, err := manager.Claim(r.Context(), limits.Subject{
			OrgID:        principal.OrgID,
			CredentialID: principal.Subject,
		}, class)
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Request controls are temporarily unavailable", true, nil)
			return
		}
		if !decision.Allowed || claim == nil {
			writeRateLimitError(w, r, decision.RetryAfter)
			return
		}
		defer claim.DoneClaim()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), limitClaimContextKey{}, claim)))
	})
}

func CompleteUsage(ctx context.Context, usage limits.ResourceUsage) error {
	claim, _ := ctx.Value(limitClaimContextKey{}).(*limits.Claim)
	return claim.Complete(usage)
}

// CompleteUsageWithBudget is CompleteUsage, except it evaluates usage
// against override instead of the request's RequestClass's own configured
// resource budget -- see limits.Claim.CompleteWithBudget's doc comment for
// why a route needs this rather than a new RequestClass or a change to the
// class's shared policy.
func CompleteUsageWithBudget(ctx context.Context, usage limits.ResourceUsage, override limits.ResourceBudget) error {
	claim, _ := ctx.Value(limitClaimContextKey{}).(*limits.Claim)
	return claim.CompleteWithBudget(usage, override)
}

func writeRateLimitError(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	var details map[string]any
	if retryAfter > 0 {
		seconds := max(1, int((retryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		details = map[string]any{"retry_after_seconds": seconds}
	}
	writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Request rate limit exceeded", true, details)
}
