package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (a *Authenticator) authenticateWebAssertion(w http.ResponseWriter, r *http.Request, ip string, now time.Time, allow bool, release func(), next http.Handler) {
	if !allow || a.webAssertions == nil || len(r.Header.Values("Authorization")) != 0 {
		reason := "invalid_web_assertion"
		if !allow {
			// CHAOS-12109: a Context Fabric route takes no web assertion; the
			// Warn line carries its own reason so a client still signing one is found.
			reason = "web_assertion_not_accepted_on_route"
		}
		a.recordUnknownFailure(r, ip, reason, now)
		a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	principal, err := a.webAssertions.Verify(r)
	if err != nil {
		if IsWebAssertionReplay(err) {
			a.recordWebAssertionReplay(r, principal, a.now().UTC())
			a.writeRateLimitError(w, r, time.Second)
			return
		}
		if errors.Is(err, ErrWebAssertionStoreUnavailable) {
			a.writeError(w, r, http.StatusServiceUnavailable, "temporarily_unavailable", "Web assertion could not be checked; retry", true, nil)
			return
		}
		// Counted when it is decided: Verify can outlive the window.
		a.recordUnknownFailure(r, ip, "invalid_web_assertion", a.now().UTC())
		a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	subjectKey := "web:" + principal.Subject
	if !a.limiter.AllowAttempt(subjectKey, now) || a.limiter.FailureBlocked(subjectKey, now) {
		a.writeRateLimitError(w, r, a.limiter.RetryAfter(subjectKey, now))
		return
	}
	ctx := context.WithValue(r.Context(), principalKey{}, principal)
	release()
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
