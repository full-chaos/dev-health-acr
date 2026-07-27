package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (a *Authenticator) authenticateWebAssertion(w http.ResponseWriter, r *http.Request, ip string, now time.Time, allow bool, next http.Handler) {
	if !allow || a.webAssertions == nil || len(r.Header.Values("Authorization")) != 0 {
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
	ctx := context.WithValue(r.Context(), principalKey{}, principal)
	response := &responseStatusWriter{ResponseWriter: w}
	next.ServeHTTP(response, r.WithContext(ctx))
	if response.successful() {
		a.usageTelemetry.Enqueue(UsageRecord{
			OrgID: principal.OrgID, ActorType: string(principal.AuthenticationMethod), ActorID: principal.Subject,
			Action: "web_assertion_used", ResourceType: "web_assertion", ResourceID: principal.Subject,
			RequestID: requestID(r), UsedAt: now,
		})
	}
}

func (a *Authenticator) recordWebAssertionReplay(r *http.Request, principal storage.Principal, now time.Time) {
	a.limiter.RecordFailure(a.clientIP(r), now)
	a.recordDenialAudit(r, auditEvent(principal, "web_assertion_replay", "web_assertion", "replayed", "denied", requestID(r), nil, now))
}
