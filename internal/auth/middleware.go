package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type principalKey struct{}

type AuthenticatorOptions struct {
	Now             func() time.Time
	Limiter         AttemptLimiter
	Logger          *slog.Logger
	DetachedTimeout time.Duration
	ClientIP        ClientIPResolver
	WebAssertions   *WebAssertionVerifier
	UsageTelemetry  *UsageTelemetry
}

type Authenticator struct {
	store           storage.CredentialStore
	audit           storage.AuditStore
	now             func() time.Time
	limiter         AttemptLimiter
	logger          *slog.Logger
	detachedTimeout time.Duration
	clientIP        ClientIPResolver
	webAssertions   *WebAssertionVerifier
	usageTelemetry  *UsageTelemetry
	ownsTelemetry   bool
}

func NewAuthenticator(store storage.CredentialStore, audit storage.AuditStore, options AuthenticatorOptions) (*Authenticator, error) {
	if storage.IsNil(store) {
		return nil, errors.New("credential store is required")
	}
	if storage.IsNil(audit) && audit != nil {
		return nil, errors.New("audit store must not be typed nil")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Limiter == nil {
		options.Limiter = NoopLimiter{}
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.DetachedTimeout <= 0 {
		options.DetachedTimeout = time.Second
	}
	if options.ClientIP == nil {
		options.ClientIP = RemoteAddressClientIP
	}
	telemetry := options.UsageTelemetry
	ownsTelemetry := false
	if telemetry == nil {
		var err error
		telemetry, err = NewUsageTelemetry(store, audit, UsageTelemetryOptions{Logger: options.Logger})
		if err != nil {
			return nil, err
		}
		ownsTelemetry = true
	}
	return &Authenticator{
		store: store, audit: audit, now: options.Now, limiter: options.Limiter, logger: options.Logger,
		detachedTimeout: options.DetachedTimeout, clientIP: options.ClientIP, webAssertions: options.WebAssertions,
		usageTelemetry: telemetry, ownsTelemetry: ownsTelemetry,
	}, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return a.MiddlewareFor(false, next)
}

func (a *Authenticator) WebAssertions() *WebAssertionVerifier {
	if a == nil {
		return nil
	}
	return a.webAssertions
}

func (a *Authenticator) MiddlewareFor(allowWebAssertions bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := a.now().UTC()
		ip := a.clientIP(r)
		if !a.limiter.AllowAttempt(ip, now) || a.limiter.FailureBlocked(ip, now) {
			a.writeRateLimitError(w, r, a.limiter.RetryAfter(ip, now))
			return
		}
		if len(r.Header.Values(WebAssertionHeader)) > 0 {
			a.authenticateWebAssertion(w, r, ip, now, allowWebAssertions, next)
			return
		}
		raw := extractBearer(r)
		if !IsTokenShapeValid(raw) {
			a.recordUnknownFailure(r, ip, "missing_or_malformed_bearer", now)
			a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		credential, err := a.store.FindByTokenHash(r.Context(), HashToken(raw))
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				a.recordUnknownFailure(r, ip, "unknown_token", now)
				a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
				return
			}
			a.logger.ErrorContext(r.Context(), "credential lookup failed", "request_id", requestID(r), "failure_class", "credential_store")
			a.writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Credential service is temporarily unavailable", true, nil)
			return
		}
		if credential.RevokedAt != nil {
			a.recordKnownFailure(r, credential, "revoked", now)
			a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		if credential.ExpiresAt != nil && !credential.ExpiresAt.After(now) {
			a.recordKnownFailure(r, credential, "expired", now)
			a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}

		// Subject is the credential's own ID for every ordinary credential,
		// but for a workload-exchanged token (CHAOS-4013) it is the STABLE
		// binding_id instead -- quotas (limits_middleware.go) key on
		// Subject, and a workload re-exchanges a fresh credential row
		// roughly every 10 minutes, so keying quotas on CredentialID would
		// reset them on every exchange and exhaust tracked-credential
		// capacity.
		subject := credential.CredentialID
		if credential.WorkloadBindingID != nil && *credential.WorkloadBindingID != "" {
			subject = *credential.WorkloadBindingID
		}
		principal := storage.Principal{
			AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: subject,
			OrgID: credential.OrgID, CredentialID: credential.CredentialID,
			RepositoryScopes: append([]string(nil), credential.RepositoryScopes...),
			Permissions:      append([]string(nil), credential.Scopes...),
		}
		ctx := context.WithValue(r.Context(), principalKey{}, principal)
		response := &responseStatusWriter{ResponseWriter: w}
		next.ServeHTTP(response, r.WithContext(ctx))
		if response.successful() {
			a.usageTelemetry.Enqueue(UsageRecord{
				OrgID: credential.OrgID, CredentialID: credential.CredentialID, ClientIP: ip,
				UserAgent: r.UserAgent(), RequestID: requestID(r), UsedAt: now,
			})
		}
	})
}

func (a *Authenticator) writeRateLimitError(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	var details map[string]any
	if retryAfter > 0 {
		seconds := max(1, int((retryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		details = map[string]any{"retry_after_seconds": seconds}
	}
	a.writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many authentication attempts", true, details)
}

func (a *Authenticator) RequireScope(required string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		if !HasScope(principal.Permissions, required) {
			a.recordDenialAudit(r, auditEvent(principal, "scope_denied", "acr_scope", required, "denied", requestID(r), map[string]any{"required_scope": required}, a.now().UTC()))
			a.writeError(w, r, http.StatusForbidden, "insufficient_scope", "Credential is missing the required scope", false, map[string]any{"required_scope": required})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Authenticator) RequireRepository(resolve func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		repository := ""
		if resolve != nil {
			repository = resolve(r)
		}
		if err := AuthorizeRepository(principal, repository); err != nil {
			a.recordDenialAudit(r, auditEvent(principal, "repository_denied", "repository", repository, "denied", requestID(r), map[string]any{"repository": repository}, a.now().UTC()))
			a.writeError(w, r, http.StatusForbidden, "repo_forbidden", "Credential is not authorized for this repository", false, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func PrincipalFromContext(ctx context.Context) (storage.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(storage.Principal)
	return principal, ok
}

func (a *Authenticator) recordUnknownFailure(r *http.Request, ip, reason string, now time.Time) {
	a.limiter.RecordFailure(ip, now)
	a.logger.WarnContext(r.Context(), "ACR authentication failed", "reason", reason, "remote_ip", ip, "request_id", requestID(r))
}

func (a *Authenticator) recordKnownFailure(r *http.Request, credential contractsv1.ClientCredential, reason string, now time.Time) {
	ip := a.clientIP(r)
	a.limiter.RecordFailure(ip, now)
	a.recordDenialAudit(r, storage.AuditEvent{
		OrgID: credential.OrgID, ActorType: "credential", ActorID: credential.CredentialID,
		Action: "credential_auth_denied", ResourceType: "acr_credential", ResourceID: credential.CredentialID,
		Status: "denied", RequestID: requestID(r), Metadata: map[string]any{"reason": reason}, CreatedAt: now,
	})
}

func (a *Authenticator) detachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), a.detachedTimeout)
}

func auditEvent(principal storage.Principal, action, resourceType, resourceID, status, requestID string, metadata map[string]any, createdAt time.Time) storage.AuditEvent {
	actorType, actorID := principal.AuditActor()
	return storage.AuditEvent{
		OrgID: principal.OrgID, ActorType: actorType, ActorID: actorID,
		Action: action, ResourceType: resourceType, ResourceID: resourceID,
		Status: status, RequestID: requestID, Metadata: metadata, CreatedAt: createdAt,
	}
}

func (a *Authenticator) recordDenialAudit(r *http.Request, event storage.AuditEvent) {
	if a.audit == nil {
		return
	}
	auditContext, cancel := a.detachedContext(r.Context())
	defer cancel()
	if err := a.audit.Record(auditContext, event); err != nil {
		a.logger.WarnContext(r.Context(), "credential denial audit delivery failed", "failure_class", "denial_audit_delivery")
	}
}

func (a *Authenticator) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool, details map[string]any) {
	if marker, ok := w.(interface{ SetDenialCode(string) }); ok {
		marker.SetDenialCode(code)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(contractsv1.ErrorEnvelope{
		SchemaVersion: contractsv1.ErrorSchema,
		RequestID:     requestID(r),
		Error:         contractsv1.ErrorDetail{Code: code, Message: message, HTTPStatus: status, Retryable: retryable, Details: details},
	})
}

func extractBearer(r *http.Request) string {
	if len(r.Header.Values("Authorization")) != 1 {
		return ""
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < 7 || !strings.EqualFold(header[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func requestID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); value != "" {
		return value
	}
	return "unknown"
}
