package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type limitClaimContextKey struct{}

// limitSubjectOf is the key principal's requests count under. For a user's
// credentials it is the owner of contextfabric.SameSubmissionOwner, the user on
// one surface: a refresh and a new Connect are new credential rows of one owner,
// so they neither take a new tracked subject nor start a new quota. A browser
// credential lives 10 minutes, so keying it on its id filled the organization's
// tracked credentials and refused its new tokens. Every other principal keys on
// its subject, as the authenticator set it (a workload's binding id).
func limitSubjectOf(principal storage.Principal) string {
	if owner, err := contextfabric.SubmissionOwnerOf(principal); err == nil && owner.UserID != "" {
		return "owner:" + string(owner.Surface) + ":" + owner.UserID
	}
	return principal.Subject
}

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
			CredentialID: limitSubjectOf(principal),
		}, class)
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Request controls are temporarily unavailable", true, nil)
			return
		}
		if !decision.Allowed || claim == nil {
			writeRateLimitError(w, r, decision.RetryAfter, limitTokenForClass(class))
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

// limitTokenForClass names the budget a refusal came from: the capabilities
// class has its own token so a caller can tell the discovery budget from the
// organization's request budget.
func limitTokenForClass(class limits.RequestClass) string {
	if class == limits.RequestClassCapabilities {
		return contractsv1.RateLimitCapabilitiesBudget
	}
	return contractsv1.RateLimitRequestBudget
}

func writeRateLimitError(w http.ResponseWriter, r *http.Request, retryAfter time.Duration, limit string) {
	details := map[string]any{contractsv1.RateLimitDetailsLimitKey: limit}
	if retryAfter > 0 {
		seconds := max(1, int((retryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		details["retry_after_seconds"] = seconds
	}
	writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Request rate limit exceeded", true, details)
}
