package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

const agentContextRuntimeEntitlement = "agent_context_runtime"

type EntitlementProvider interface {
	HasEntitlement(context.Context, string, string) (bool, error)
}

type EntitlementFunc func(context.Context, string, string) (bool, error)

func (f EntitlementFunc) HasEntitlement(ctx context.Context, orgID, entitlement string) (bool, error) {
	return f(ctx, orgID, entitlement)
}

type ContextPacketAssembler interface {
	Assemble(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ContextPacket, error)
}

type EpisodeCreator interface {
	Create(context.Context, storage.Principal, contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error)
}

type RuntimeDependencies struct {
	Credentials     storage.CredentialStore
	Audit           storage.AuditStore
	Entitlements    EntitlementProvider
	Assembler       ContextPacketAssembler
	Evidence        storage.EvidenceStore
	Episodes        EpisodeCreator
	ReadinessChecks []ReadinessCheck
}

func (r *RuntimeDependencies) validate() error {
	if r == nil || storage.IsNil(r.Credentials) || storage.IsNil(r.Audit) || storage.IsNil(r.Entitlements) || storage.IsNil(r.Assembler) || storage.IsNil(r.Evidence) {
		return errors.New("hosted read runtime dependencies must be configured together")
	}
	if r.Episodes != nil && storage.IsNil(r.Episodes) {
		return errors.New("hosted episode runtime must not be typed nil")
	}
	if len(r.ReadinessChecks) < 3 {
		return errors.New("hosted read runtime requires postgres, clickhouse, and entitlement readiness checks")
	}
	required := map[string]bool{"postgres": false, "clickhouse": false, "entitlement": false}
	for _, check := range r.ReadinessChecks {
		if storage.IsNil(check) {
			return errors.New("hosted read runtime readiness checks must not be nil")
		}
		name := strings.TrimSpace(check.Name())
		if name == "" {
			return errors.New("hosted read runtime readiness checks require a name")
		}
		if seen, ok := required[name]; ok {
			if seen {
				return errors.New("hosted read runtime readiness checks must not repeat postgres, clickhouse, or entitlement")
			}
			required[name] = true
		}
	}
	for _, name := range []string{"postgres", "clickhouse", "entitlement"} {
		if !required[name] {
			return fmt.Errorf("hosted read runtime requires a %s readiness check", name)
		}
	}
	return nil
}

func (a *App) protectedRuntimeHandler(class limits.RequestClass, scope string, entitlement, allowWebAssertions bool, next http.Handler) http.Handler {
	if a.runtime == nil || a.authenticator == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	handler := next
	if entitlement {
		handler = a.requireEntitlement(agentContextRuntimeEntitlement, handler)
	}
	handler = a.requireClientVersion(handler)
	handler = LimitMiddleware(a.limits, class, handler)
	handler = a.authenticator.RequireScope(scope, handler)
	return a.authenticator.MiddlewareFor(allowWebAssertions, handler)
}

func (a *App) requireClientVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequest := r.Clone(r.Context())
		providerRequest.Header = r.Header.Clone()
		providerRequest.Header.Del("Authorization")
		providerRequest.Body = nil
		capabilities, err := a.capabilities.Capabilities(r.Context(), providerRequest)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "capabilities resolution failed", "request_id", RequestID(r.Context()), "failure_class", "capabilities_provider")
			writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Capabilities are temporarily unavailable", true, nil)
			return
		}
		clientVersion := strings.TrimSpace(r.Header.Get("X-ACR-Client-Version"))
		if clientVersion == "" || !clientVersionCompatible(clientVersion, capabilities.MinimumSidecarVersion) || revokedClientVersion(clientVersion, a.config.RevokedClientVersions) {
			writeError(w, r, http.StatusUpgradeRequired, "version_mismatch", "ACR client version is not supported", false, map[string]any{"minimum_client_version": capabilities.MinimumSidecarVersion})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func revokedClientVersion(clientVersion string, revokedVersions []string) bool {
	return slices.ContainsFunc(revokedVersions, func(revoked string) bool {
		return version.Exact(clientVersion, revoked)
	})
}

func (a *App) requireEntitlement(entitlement string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		enabled, err := a.runtime.Entitlements.HasEntitlement(r.Context(), principal.OrgID, entitlement)
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Entitlement service is temporarily unavailable", true, nil)
			return
		}
		if !enabled {
			a.recordReadAudit(r.Context(), principal, "entitlement_denied", "acr_entitlement", entitlement, "denied", nil)
			writeError(w, r, http.StatusForbidden, "feature_not_enabled", "Agent Context Runtime is not enabled for this organization", false, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleRuntimeUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Hosted read runtime is temporarily unavailable", true, nil)
}

func (a *App) recordReadAudit(ctx context.Context, principal storage.Principal, action, resourceType, resourceID, status string, metadata map[string]any) {
	if a.runtime == nil || a.runtime.Audit == nil || strings.TrimSpace(principal.OrgID) == "" {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	actorType, actorID := principal.AuditActor()
	if err := a.runtime.Audit.Record(auditCtx, storage.AuditEvent{
		OrgID: principal.OrgID, ActorType: actorType, ActorID: actorID,
		Action: action, ResourceType: resourceType, ResourceID: resourceID, Status: status,
		RequestID: RequestID(ctx), Metadata: metadata, CreatedAt: a.now().UTC(),
	}); err != nil {
		a.logger.WarnContext(ctx, "read audit persistence failed", "failure_class", "audit_store")
	}
}
