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
}

type Authenticator struct {
	store           storage.CredentialStore
	audit           storage.AuditStore
	now             func() time.Time
	limiter         AttemptLimiter
	logger          *slog.Logger
	detachedTimeout time.Duration
	clientIP        ClientIPResolver
}

func NewAuthenticator(store storage.CredentialStore, audit storage.AuditStore, options AuthenticatorOptions) (*Authenticator, error) {
	if store == nil {
		return nil, errors.New("credential store is required")
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
	return &Authenticator{store: store, audit: audit, now: options.Now, limiter: options.Limiter, logger: options.Logger, detachedTimeout: options.DetachedTimeout, clientIP: options.ClientIP}, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := a.now().UTC()
		ip := a.clientIP(r)
		if !a.limiter.AllowAttempt(ip, now) || a.limiter.FailureBlocked(ip, now) {
			a.writeRateLimitError(w, r, a.limiter.RetryAfter(ip, now))
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
		if credential.RevokedAt != nil && !credential.RevokedAt.After(now) {
			a.recordKnownFailure(r, credential, "revoked", now)
			a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		if credential.ExpiresAt != nil && !credential.ExpiresAt.After(now) {
			a.recordKnownFailure(r, credential, "expired", now)
			a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}

		principal := storage.Principal{
			OrgID: credential.OrgID, CredentialID: credential.CredentialID,
			RepositoryScopes: append([]string(nil), credential.RepositoryScopes...),
			Permissions:      append([]string(nil), credential.Scopes...),
		}
		usageCtx, cancelUsage := a.detachedContext(r.Context())
		defer cancelUsage()
		if err := a.store.TouchLastUsed(usageCtx, credential.CredentialID, ip, r.UserAgent(), now); err != nil {
			a.logger.WarnContext(r.Context(), "credential last-used update failed", "failure_class", "credential_usage_store")
		}
		a.recordAudit(usageCtx, storage.AuditEvent{
			OrgID: credential.OrgID, ActorType: "credential", ActorID: credential.CredentialID,
			Action: "credential_used", ResourceType: "acr_credential", ResourceID: credential.CredentialID,
			Status: "success", RequestID: requestID(r), CreatedAt: now,
		})
		ctx := context.WithValue(r.Context(), principalKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
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
			auditCtx, cancelAudit := a.detachedContext(r.Context())
			defer cancelAudit()
			a.recordAudit(auditCtx, storage.AuditEvent{
				OrgID: principal.OrgID, ActorType: "credential", ActorID: principal.CredentialID,
				Action: "scope_denied", ResourceType: "acr_scope", ResourceID: required,
				Status: "denied", RequestID: requestID(r), Metadata: map[string]any{"required_scope": required}, CreatedAt: a.now().UTC(),
			})
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
			auditCtx, cancelAudit := a.detachedContext(r.Context())
			defer cancelAudit()
			a.recordAudit(auditCtx, storage.AuditEvent{
				OrgID: principal.OrgID, ActorType: "credential", ActorID: principal.CredentialID,
				Action: "repository_denied", ResourceType: "repository", ResourceID: repository,
				Status: "denied", RequestID: requestID(r), Metadata: map[string]any{"repository": repository}, CreatedAt: a.now().UTC(),
			})
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
	auditCtx, cancelAudit := a.detachedContext(r.Context())
	defer cancelAudit()
	a.recordAudit(auditCtx, storage.AuditEvent{
		OrgID: credential.OrgID, ActorType: "credential", ActorID: credential.CredentialID,
		Action: "credential_auth_denied", ResourceType: "acr_credential", ResourceID: credential.CredentialID,
		Status: "denied", RequestID: requestID(r), Metadata: map[string]any{"reason": reason}, CreatedAt: now,
	})
}

func (a *Authenticator) detachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), a.detachedTimeout)
}

func (a *Authenticator) recordAudit(ctx context.Context, event storage.AuditEvent) {
	if a.audit == nil {
		return
	}
	if err := a.audit.Record(ctx, event); err != nil {
		a.logger.WarnContext(ctx, "audit event persistence failed", "failure_class", "audit_store")
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
