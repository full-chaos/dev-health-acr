package auth

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/logsanitize"
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
		// The per-address gate exists against credential guessing, so it
		// counts FAILED authentications only, and a request that presents no
		// credential is not one: it is answered 401, never counted and never
		// gated (no store work is done for it). A success does not reset the
		// count, so a guessing burst cannot be laundered through one valid
		// token; but a well-formed bearer that verifies is never refused by
		// the failure budget: an over-budget address keeps one verification
		// slot (OverBudgetVerificationSlots), which bounds its store lookups.
		// Web assertions keep the plain gate before verification.
		webAssertion := len(r.Header.Values(WebAssertionHeader)) > 0
		if !webAssertion && len(r.Header.Values("Authorization")) == 0 {
			a.logger.DebugContext(r.Context(), "ACR authentication failed", "reason", "missing_bearer", "remote_ip", logsanitize.SanitizeLogAttr(ip), "request_id", logsanitize.SanitizeLogAttr(requestID(r)))
			a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		raw := ""
		if !webAssertion {
			raw = extractBearer(r)
		}
		var release func()
		var decision AttemptDecision
		if !webAssertion && IsTokenShapeValid(raw) {
			release, decision = BeginVerificationDecision(a.limiter, ip, now)
		} else {
			release, decision = BeginAttemptDecision(a.limiter, ip, now)
		}
		if !decision.Admitted() {
			// Every bound answers with the same 429; the log line is where
			// an operator tells them apart.
			// Only the first failure_budget or verification_slot refusal per
			// address per window is Info; the retries of a locked-out address
			// are Debug so the retry rate does not set the Info volume.
			a.logRefusal(r, decision.Refusal, decision.InFlight, ip, decision.FirstRefusal)
			retryAfter := a.limiter.RetryAfter(ip, now)
			if retryAfter <= 0 {
				retryAfter = time.Second
			}
			a.writeRateLimitError(w, r, retryAfter)
			return
		}
		// Held only while the credential is being decided (failures are
		// recorded before the deferred release runs); released before the
		// wrapped handler so a long handler never occupies the budget.
		defer release()
		if webAssertion {
			a.authenticateWebAssertion(w, r, ip, now, allowWebAssertions, release, next)
			return
		}
		if !IsTokenShapeValid(raw) {
			a.recordUnknownFailure(r, ip, "missing_or_malformed_bearer", now)
			a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		credential, err := a.store.FindByTokenHash(r.Context(), HashToken(raw))
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				a.rejectCredential(w, r, ip, "unknown_token", nil, decision, now)
				return
			}
			cause := credentialLookupCause(err)
			if cause == credentialLookupCauseCanceled {
				a.logger.InfoContext(r.Context(), "credential lookup canceled", "request_id", logsanitize.SanitizeLogAttr(requestID(r)), "failure_class", "caller_canceled", "cause", cause, "db_class", credentialLookupDBClass(err), "error_kind", credentialLookupErrorKind(err), credentialLookupSQLStateAttr(err))
			} else {
				a.logger.ErrorContext(r.Context(), "credential lookup failed", "request_id", logsanitize.SanitizeLogAttr(requestID(r)), "failure_class", "credential_store", "cause", cause, "db_class", credentialLookupDBClass(err), "error_kind", credentialLookupErrorKind(err), credentialLookupSQLStateAttr(err))
			}
			a.writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Credential service is temporarily unavailable", true, nil)
			return
		}
		if credential.RevokedAt != nil {
			a.rejectCredential(w, r, ip, "revoked", &credential, decision, now)
			return
		}
		if credential.ExpiresAt != nil && !credential.ExpiresAt.After(now) {
			a.rejectCredential(w, r, ip, "expired", &credential, decision, now)
			return
		}
		if !resourceAdmitted(credential.Resource, r.Header.Values(ResourceHeader)) {
			a.rejectCredential(w, r, ip, "resource_mismatch", &credential, decision, now)
			return
		}
		if decision.OverBudget {
			a.logger.DebugContext(r.Context(), "ACR authentication admitted over failure budget", "remote_ip", logsanitize.SanitizeLogAttr(ip), "request_id", logsanitize.SanitizeLogAttr(requestID(r)))
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
		release()
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

// rejectCredential answers a well-formed credential the store rejected. It
// is counted; when it was admitted over the failure limit, or the address is
// at or over the limit when it is decided, it is
// answered with the same 429 as an attempt refused before verification, so
// a guess learns nothing from the slot it used, and it is logged as that
// refusal (Info once per window, then Debug).
func (a *Authenticator) rejectCredential(w http.ResponseWriter, r *http.Request, ip, reason string, credential *contractsv1.ClientCredential, decision AttemptDecision, now time.Time) {
	// Counted at the moment it is decided: a verification that outlives the
	// window is a failure of the window it ends in.
	decided := a.now().UTC()
	overBudget, first := RecordRejection(a.limiter, ip, decided, decision.OverBudget)
	if credential != nil {
		a.recordKnownDenialAudit(r, *credential, reason, now)
	}
	if overBudget {
		a.logRefusal(r, RefusalFailureBudget, decision.InFlight, ip, first)
		retryAfter := a.limiter.RetryAfter(ip, decided)
		if retryAfter <= 0 {
			retryAfter = time.Second
		}
		a.writeRateLimitError(w, r, retryAfter)
		return
	}
	if credential == nil {
		a.logger.WarnContext(r.Context(), "ACR authentication failed", "reason", reason, "remote_ip", logsanitize.SanitizeLogAttr(ip), "request_id", logsanitize.SanitizeLogAttr(requestID(r)))
	}
	a.writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
}

func (a *Authenticator) logRefusal(r *http.Request, refusal AttemptRefusal, inFlight int, ip string, first bool) {
	level := slog.LevelInfo
	if !first {
		level = slog.LevelDebug
	}
	a.logger.Log(r.Context(), level, "ACR authentication attempt refused", "reason", string(refusal), "in_flight", inFlight, "remote_ip", logsanitize.SanitizeLogAttr(ip), "request_id", logsanitize.SanitizeLogAttr(requestID(r)))
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

// recordUnknownFailure sanitizes `ip` at the log site so the invariant
// holds for any ClientIPResolver a caller configures, not only the two
// this repo ships.
func (a *Authenticator) recordUnknownFailure(r *http.Request, ip, reason string, now time.Time) {
	a.limiter.RecordFailure(ip, now)
	a.logger.WarnContext(r.Context(), "ACR authentication failed", "reason", reason, "remote_ip", logsanitize.SanitizeLogAttr(ip), "request_id", logsanitize.SanitizeLogAttr(requestID(r)))
}

func (a *Authenticator) recordKnownDenialAudit(r *http.Request, credential contractsv1.ClientCredential, reason string, now time.Time) {
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

// ResourceHeader names the protected resource a request is made on behalf of.
// The hosted MCP endpoint sets it to its own resource identifier on every
// call it forwards, from process configuration, never from the caller.
const ResourceHeader = "X-ACR-Resource"

// resourceAdmitted decides audience binding (RFC 8707, MCP authorization): a
// credential bound to a protected resource is accepted only on requests that
// carry exactly that resource. A credential with no binding (operator, device,
// workload issuance) is accepted as before.
func resourceAdmitted(bound string, presented []string) bool {
	if bound == "" {
		return true
	}
	return len(presented) == 1 && SameResource(bound, presented[0])
}

// SameResource reports whether two protected-resource identifiers name the
// same hosted MCP endpoint: equal, or the same scheme and host with paths in
// the alias set the endpoint answers on ("/" and "/mcp"; an empty path is "/").
// A token bound to one alias is therefore accepted on the other (CHAOS-6218).
func SameResource(a, b string) bool {
	if a == b {
		return true
	}
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil || ua.Host == "" || ua.Scheme != ub.Scheme || !strings.EqualFold(ua.Host, ub.Host) {
		return false
	}
	if ua.RawQuery != "" || ub.RawQuery != "" || ua.Fragment != "" || ub.Fragment != "" || ua.User != nil || ub.User != nil {
		return false
	}
	aliasPath := func(p string) bool { return p == "" || p == "/" || p == "/mcp" }
	return aliasPath(ua.Path) && aliasPath(ub.Path)
}

// The cause tokens are a closed set. credentialLookupCauseConn is the store's
// "connection_failure" class (connection-class SQLSTATE, dial failure, broken
// pipe, driver.ErrBadConn), named for the class and not for one of its members.
const (
	credentialLookupCauseCanceled = "context_canceled"
	credentialLookupCauseDeadline = "deadline_exceeded"
	credentialLookupCauseConn     = "connection_failure"
	credentialLookupCauseResource = "resource_exhausted"
	credentialLookupCauseServer   = "server_unavailable"
	credentialLookupCauseOther    = "other"
)

// connectionFailureSQLStates are the class 08 SQLSTATEs that mean the
// connection failed. 08P01 (protocol_violation) is not one of them.
var connectionFailureSQLStates = map[string]bool{"08000": true, "08001": true, "08003": true, "08004": true, "08006": true, "08007": true}

// serverUnavailableSQLStates are the class 57 SQLSTATEs of a server that is
// shutting down or not yet accepting connections. 57014 (query_canceled) is not
// one of them.
var serverUnavailableSQLStates = map[string]bool{"57P01": true, "57P02": true, "57P03": true}

func connectionFailureSQLState(state string) bool { return connectionFailureSQLStates[state] }

// credentialLookupCause maps a store error to a closed token; the error text
// never reaches the log.
func credentialLookupCause(err error) string {
	var class *storage.DependencyErrorClass
	switch {
	case errors.Is(err, context.Canceled):
		return credentialLookupCauseCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return credentialLookupCauseDeadline
	case errors.As(err, &class) && (class.Class == "connection_failure" || connectionFailureSQLState(class.SQLState)):
		return credentialLookupCauseConn
	case strings.HasPrefix(credentialLookupSQLState(err), "53"):
		return credentialLookupCauseResource
	case errors.As(err, &class) && serverUnavailableSQLStates[class.SQLState]:
		return credentialLookupCauseServer
	default:
		return credentialLookupCauseOther
	}
}

// sqlStateClassNames names the two-character SQLSTATE class of a store error,
// the closed vocabulary of the db_class log attribute. A class outside it
// logs "other"; an error with no SQLSTATE logs "none".
var sqlStateClassNames = map[string]string{
	"08": "connection_exception",
	"22": "data_exception",
	"23": "integrity_constraint_violation",
	"25": "invalid_transaction_state",
	"28": "invalid_authorization_specification",
	"40": "transaction_rollback",
	"42": "access_rule_violation",
	"53": "insufficient_resources",
	"54": "program_limit_exceeded",
	"55": "object_not_in_prerequisite_state",
	"57": "operator_intervention",
	"58": "system_error",
	"XX": "internal_error",
}

// credentialLookupDBClass maps a store error to the closed db_class token; the
// error text and the full SQLSTATE never reach the log.
func credentialLookupDBClass(err error) string {
	var class *storage.DependencyErrorClass
	if !errors.As(err, &class) || len(class.SQLState) < 2 {
		return "none"
	}
	if name, ok := sqlStateClassNames[class.SQLState[:2]]; ok {
		return name
	}
	return "other"
}

var sqlStatePattern = regexp.MustCompile(`^[0-9A-Z]{5}$`)

// credentialLookupSQLState returns the store error's SQLSTATE when it is a
// well-formed five-character code, and "" otherwise.
func credentialLookupSQLState(err error) string {
	var class *storage.DependencyErrorClass
	if errors.As(err, &class) && sqlStatePattern.MatchString(class.SQLState) {
		return class.SQLState
	}
	return ""
}

// credentialLookupSQLStateAttr is the sqlstate log attribute, absent when there
// is no well-formed code.
func credentialLookupSQLStateAttr(err error) slog.Attr {
	if state := credentialLookupSQLState(err); state != "" {
		return slog.String("sqlstate", state)
	}
	return slog.Attr{}
}

// credentialLookupErrorKind names the shape of a store error as a closed,
// never-empty token, so a failure with no SQLSTATE still says what it was.
func credentialLookupErrorKind(err error) string {
	var class *storage.DependencyErrorClass
	var recordHeader tls.RecordHeaderError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.As(err, &class):
		return "sqlstate"
	case errors.As(err, &recordHeader):
		return "tls"
	case errors.As(err, &network):
		return "network"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case errors.Is(err, driver.ErrBadConn):
		return "bad_conn"
	case errors.Is(err, sql.ErrConnDone):
		return "conn_done"
	case errors.Is(err, storage.ErrUnavailable):
		return "unavailable"
	default:
		return "unknown"
	}
}
