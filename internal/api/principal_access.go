package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// PrincipalAccessReader is the Dev Health principal access read (ops
// CHAOS-12084): the live membership, impersonation, session, flags and
// repository inventory of one user in one organization. It must not cache:
// the per-call re-check (CHAOS-12092, North Star check 18) reads it on every
// protected call by a user-class credential.
type PrincipalAccessReader interface {
	PrincipalAccess(ctx context.Context, orgID, userID, sessionID string) (entitlements.PrincipalAccess, error)
}

// Deny reasons of the per-call access read. They are logged and audited,
// never served: every one of them is answered 403 access_denied.
const (
	accessDenyNoApprovingUser = "no_approving_user"
	accessDenyNoBoundSession  = "no_bound_session"
	accessDenyUnknown         = "principal_unknown"
	accessDenyNotMember       = "not_member"
	accessDenyImpersonating   = "impersonating"
	accessDenySessionNotLive  = "session_not_live"
)

const askDevEntitlement = "ask_dev"

type liveAccessKey struct{}

// liveAccess is the one access read of a request, reused inside it.
type liveAccess struct {
	access entitlements.PrincipalAccess
}

func liveAccessFromContext(ctx context.Context) (liveAccess, bool) {
	value, ok := ctx.Value(liveAccessKey{}).(liveAccess)
	return value, ok
}

// userClassCredential reports whether principal authenticated with a
// user-class credential: browser, OAuth user or native device (design §2.5).
// Operator, workload, legacy credentials and web assertions carry no user
// whose access could be read.
func userClassCredential(principal storage.Principal) bool {
	if principal.AuthenticationMethod != storage.AuthenticationMethodCredential {
		return false
	}
	switch principal.ClientClass {
	case storage.CredentialClientClassBrowser, storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice:
		return true
	}
	return false
}

func (a *App) principalAccess() PrincipalAccessReader {
	if a.runtime == nil || storage.IsNil(a.runtime.PrincipalAccess) {
		return nil
	}
	return a.runtime.PrincipalAccess
}

// requireLiveAccess is the per-call access read (CHAOS-12092, design
// §3.1-3.2). For a user-class credential it reads Dev Health once, with no
// cache, and refuses the call before the handler when the user is not a
// member, is impersonating, the browser credential's session is not live
// (403 access_denied), a required flag is off (403 feature_not_enabled), or
// the effective repository scope -- the grant within the live inventory -- is
// empty (409 no_repository). A read that fails is 503, never 401. Otherwise
// the handler runs with the principal narrowed to the effective scope and the
// read kept in the context for the rest of the request. With enforceFlags
// false (the capabilities route, which is how a client learns what is
// enabled) a flag that is off is not refused: the handler reports it from the
// read.
func (a *App) requireLiveAccess(enforceFlags bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		if !userClassCredential(principal) {
			next.ServeHTTP(w, r)
			return
		}
		reader := a.principalAccess()
		if reader == nil {
			// Local entitlement mode only (development and test); NewApp
			// logs a Warn when a runtime has no reader.
			next.ServeHTTP(w, r)
			return
		}
		if principal.ApprovingUserID == "" {
			a.refuseLiveAccess(w, r, principal, accessDenyNoApprovingUser, 0)
			return
		}
		sessionID := ""
		if principal.ClientClass == storage.CredentialClientClassBrowser {
			if principal.CredentialSessionID == "" {
				a.refuseLiveAccess(w, r, principal, accessDenyNoBoundSession, 0)
				return
			}
			sessionID = principal.CredentialSessionID
		}
		started := time.Now()
		access, err := reader.PrincipalAccess(r.Context(), principal.OrgID, principal.ApprovingUserID, sessionID)
		elapsed := time.Since(started)
		switch {
		case errors.Is(err, entitlements.ErrPrincipalUnknown):
			a.refuseLiveAccess(w, r, principal, accessDenyUnknown, elapsed)
			return
		case errors.Is(err, entitlements.ErrSessionInvalid):
			a.refuseLiveAccess(w, r, principal, accessDenySessionNotLive, elapsed)
			return
		case err != nil:
			a.logger.ErrorContext(r.Context(), "principal access read failed",
				"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "failure_class", "principal_access",
				"reason", entitlements.RejectionReason(err), "client_class", string(principal.ClientClass),
				"elapsed_ms", elapsed.Milliseconds())
			writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Access service is temporarily unavailable", true, nil)
			return
		}
		if reason := accessDenyReason(principal, access); reason != "" {
			a.refuseLiveAccess(w, r, principal, reason, elapsed)
			return
		}
		for _, entitlement := range requiredEntitlements(principal) {
			if enforceFlags && !accessGrants(access, entitlement) {
				a.recordReadAudit(r.Context(), principal, "entitlement_denied", "acr_entitlement", entitlement, "denied", map[string]any{"source": "principal_access"})
				a.logger.InfoContext(r.Context(), "principal access read refused",
					"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "reason", "entitlement_off",
					"entitlement", entitlement, "client_class", string(principal.ClientClass), "elapsed_ms", elapsed.Milliseconds())
				writeError(w, r, http.StatusForbidden, "feature_not_enabled", entitlementOffMessage(entitlement), false, nil)
				return
			}
		}
		effective := effectiveRepositoryScope(principal.RepositoryScopes, access.Repositories)
		ctx, err := auth.NarrowRepositoryScopes(r.Context(), effective)
		if err != nil {
			a.recordReadAudit(r.Context(), principal, "access_denied", "acr_access", contractsv1.ErrorCodeNoRepository, "denied", nil)
			a.logger.InfoContext(r.Context(), "principal access read refused",
				"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "reason", contractsv1.ErrorCodeNoRepository,
				"client_class", string(principal.ClientClass), "inventory", len(access.Repositories), "elapsed_ms", elapsed.Milliseconds())
			writeError(w, r, http.StatusConflict, contractsv1.ErrorCodeNoRepository, contractsv1.ContextFabricNoRepositoryMessage, false, nil)
			return
		}
		ctx = context.WithValue(ctx, liveAccessKey{}, liveAccess{access: access})
		a.logAccessGranted(ctx, principal, len(effective), len(access.Repositories), enforceFlags, elapsed)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// logAccessGranted writes a granted read's basis, at Debug (one line per
// user-class call): a narrowing regression is visible here, not only as a
// 2xx. A project-only admission's basis is at Info (CHAOS-12300, design V5),
// corpus-safe: the class, the project count and the digest class, never an id
// or the digest value.
func (a *App) logAccessGranted(ctx context.Context, principal storage.Principal, scopeRepositories, inventoryRepositories int, enforceFlags bool, elapsed time.Duration) {
	requestID := contextfabric.SanitizeLogAttr(RequestID(ctx))
	grantClass := auth.ClassifyGrant(principal)
	if grantClass != auth.GrantProjectOnly {
		a.logger.DebugContext(ctx, "principal access read granted",
			"request_id", requestID, "client_class", string(principal.ClientClass),
			"decision", "granted", "scope_repositories", scopeRepositories, "inventory_repositories", inventoryRepositories,
			"flags_enforced", enforceFlags, "elapsed_ms", elapsed.Milliseconds())
		return
	}
	a.logger.InfoContext(ctx, "principal access read granted",
		"request_id", requestID, "client_class", string(principal.ClientClass),
		"decision", "granted", "scope_repositories", scopeRepositories, "inventory_repositories", inventoryRepositories,
		"flags_enforced", enforceFlags, "elapsed_ms", elapsed.Milliseconds(),
		"grant_class", string(grantClass), "project_scope_count", len(principal.ProjectScopes),
		"grant_digest_class", string(contextfabric.GrantDigestClassOf(contextfabric.StoredResultGrantDigest(principal))))
}

// accessDenyReason is why the live answer refuses the principal ("" when it
// does not).
func accessDenyReason(principal storage.Principal, access entitlements.PrincipalAccess) string {
	switch {
	case !access.Member:
		return accessDenyNotMember
	case access.Impersonating:
		return accessDenyImpersonating
	case access.Session != nil && access.Session.Impersonating:
		return accessDenyImpersonating
	case principal.ClientClass == storage.CredentialClientClassBrowser && (access.Session == nil || !access.Session.Live):
		return accessDenySessionNotLive
	}
	return ""
}

// requiredEntitlements are the flags a user-class call needs (design §2.5):
// agent_context_runtime for every class, and ask_dev for the browser class.
func requiredEntitlements(principal storage.Principal) []string {
	if principal.ClientClass == storage.CredentialClientClassBrowser {
		return []string{askDevEntitlement, agentContextRuntimeEntitlement}
	}
	return []string{agentContextRuntimeEntitlement}
}

func accessGrants(access entitlements.PrincipalAccess, entitlement string) bool {
	switch entitlement {
	case askDevEntitlement:
		return access.AskDev
	case agentContextRuntimeEntitlement:
		return access.AgentContextRuntime
	}
	return false
}

func entitlementOffMessage(entitlement string) string {
	if entitlement == askDevEntitlement {
		return "Ask Dev is not enabled for this organization"
	}
	return "Agent Context Runtime is not enabled for this organization"
}

// effectiveRepositoryScope is the grant within the live inventory: every
// inventory repository the credential's repository scopes allow ("*" allows
// all of them), normalized, sorted and unique. An inventory entry that is not
// a repository slug is never in scope.
func effectiveRepositoryScope(grant, inventory []string) []string {
	effective := make([]string, 0, len(inventory))
	for _, repository := range inventory {
		normalized, err := auth.NormalizeRepositorySlug(repository)
		if err != nil || !auth.RepositoryAllowed(grant, normalized) {
			continue
		}
		effective = append(effective, normalized)
	}
	slices.Sort(effective)
	return slices.Compact(effective)
}

func (a *App) refuseLiveAccess(w http.ResponseWriter, r *http.Request, principal storage.Principal, reason string, elapsed time.Duration) {
	a.recordReadAudit(r.Context(), principal, "access_denied", "acr_access", reason, "denied", nil)
	a.logger.InfoContext(r.Context(), "principal access read refused",
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "reason", reason,
		"client_class", string(principal.ClientClass), "elapsed_ms", elapsed.Milliseconds())
	writeError(w, r, http.StatusForbidden, contractsv1.ErrorCodeAccessDenied, contractsv1.ContextFabricAccessDeniedMessage, false, nil)
}

// agentContextRuntimeEntitled answers agent_context_runtime for principal:
// from the request's live access read when one ran (a user-class credential;
// the 30-second entitlement cache is not used on that path), else from the
// entitlement provider.
func (a *App) agentContextRuntimeEntitled(ctx context.Context, principal storage.Principal) (bool, error) {
	if live, ok := liveAccessFromContext(ctx); ok {
		return live.access.AgentContextRuntime, nil
	}
	return a.runtime.Entitlements.HasEntitlement(ctx, principal.OrgID, agentContextRuntimeEntitlement)
}

// liveAccessBudgetExhausted reports whether an investigation create that ran
// the access read has less left of its request budget than the minimum the
// hosted API honours (design §3.2): it is refused before admission.
func liveAccessBudgetExhausted(ctx context.Context) bool {
	if _, ok := liveAccessFromContext(ctx); !ok {
		return false
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return false
	}
	return time.Until(deadline) < contractsv1.ContextFabricWaitBudgetMinSeconds*time.Second
}

func (a *App) writeBudgetExhausted(w http.ResponseWriter, r *http.Request, principal storage.Principal) {
	remaining := time.Duration(0)
	if deadline, ok := r.Context().Deadline(); ok {
		remaining = time.Until(deadline)
	}
	a.logger.InfoContext(r.Context(), "investigation refused: budget left after the access read is below the minimum",
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "client_class", string(principal.ClientClass),
		"remaining_ms", remaining.Milliseconds(), "minimum_seconds", contractsv1.ContextFabricWaitBudgetMinSeconds)
	writeError(w, r, http.StatusServiceUnavailable, contractsv1.ErrorCodeBudgetExhausted, contractsv1.ContextFabricBudgetExhaustedMessage, true, nil)
}
