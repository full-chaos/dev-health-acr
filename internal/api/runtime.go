package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
	if r == nil || r.Credentials == nil || r.Audit == nil || r.Entitlements == nil || r.Assembler == nil || r.Evidence == nil {
		return errors.New("hosted read runtime dependencies must be configured together")
	}
	if len(r.ReadinessChecks) < 3 {
		return errors.New("hosted read runtime requires credential, entitlement, and evidence readiness checks")
	}
	for _, check := range r.ReadinessChecks {
		if check == nil || strings.TrimSpace(check.Name()) == "" {
			return errors.New("hosted read runtime readiness checks require a name")
		}
	}
	return nil
}

func (a *App) protectedRuntimeHandler(class limits.RequestClass, scope string, entitlement bool, next http.Handler) http.Handler {
	if a.runtime == nil || a.authenticator == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	handler := next
	if entitlement {
		handler = a.requireEntitlement(agentContextRuntimeEntitlement, handler)
	}
	handler = LimitMiddleware(a.limits, class, handler)
	handler = a.authenticator.RequireScope(scope, handler)
	return a.authenticator.Middleware(handler)
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
	if err := a.runtime.Audit.Record(auditCtx, storage.AuditEvent{
		OrgID: principal.OrgID, ActorType: "credential", ActorID: principal.CredentialID,
		Action: action, ResourceType: resourceType, ResourceID: resourceID, Status: status,
		RequestID: RequestID(ctx), Metadata: metadata, CreatedAt: a.now().UTC(),
	}); err != nil {
		a.logger.WarnContext(ctx, "read audit persistence failed", "failure_class", "audit_store")
	}
}
