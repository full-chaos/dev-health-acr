package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (a *Authenticator) authenticateWebAssertion(w http.ResponseWriter, r *http.Request, ip string, now time.Time, allow bool, next http.Handler) {
	if !allow || a.webAssertions == nil || strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		a.recordUnknownFailure(r, ip, "invalid_web_assertion", now)
		a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	principal, err := a.webAssertions.Verify(r)
	if err != nil {
		if IsWebAssertionReplay(err) {
			a.recordWebAssertionReplay(r, principal, now)
			a.writeRateLimitError(w, r, time.Second)
			return
		}
		a.recordUnknownFailure(r, ip, "invalid_web_assertion", now)
		a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	subjectKey := "web:" + principal.Subject
	if !a.limiter.AllowAttempt(subjectKey, now) || a.limiter.FailureBlocked(subjectKey, now) {
		a.writeRateLimitError(w, r, a.limiter.RetryAfter(subjectKey, now))
		return
	}
	usageCtx, cancelUsage := a.detachedContext(r.Context())
	defer cancelUsage()
	a.recordAudit(usageCtx, auditEvent(principal, "web_assertion_used", "web_assertion", principal.Subject, "success", requestID(r), nil, now))
	ctx := context.WithValue(r.Context(), principalKey{}, principal)
	next.ServeHTTP(w, r.WithContext(ctx))
}

func (a *Authenticator) recordWebAssertionReplay(r *http.Request, principal storage.Principal, now time.Time) {
	a.limiter.RecordFailure(a.clientIP(r), now)
	auditCtx, cancelAudit := a.detachedContext(r.Context())
	defer cancelAudit()
	a.recordAudit(auditCtx, auditEvent(principal, "web_assertion_replay", "web_assertion", "replayed", "denied", requestID(r), nil, now))
}
