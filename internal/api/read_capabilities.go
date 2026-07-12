package api

import (
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

func (a *App) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
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
	entitled, err := a.runtime.Entitlements.HasEntitlement(r.Context(), principal.OrgID, agentContextRuntimeEntitlement)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Entitlement service is temporarily unavailable", true, nil)
		return
	}
	capabilities.Entitlements.AgentContextRuntime = entitled
	capabilities.Permissions.ContextRead = auth.HasScope(principal.Permissions, auth.ScopeContextRead)
	capabilities.Permissions.EvidenceRead = auth.HasScope(principal.Permissions, auth.ScopeEvidenceRead)
	capabilities.Permissions.EpisodeWrite = auth.HasScope(principal.Permissions, auth.ScopeEpisodeWrite)
	capabilities.EnabledTools = []string{}
	if entitled && capabilities.Permissions.ContextRead {
		capabilities.EnabledTools = append(capabilities.EnabledTools, "context_for_task")
	}
	if entitled && capabilities.Permissions.EvidenceRead {
		capabilities.EnabledTools = append(capabilities.EnabledTools, "source_evidence")
	}
	if entitled && capabilities.Permissions.EpisodeWrite && a.runtime.Episodes != nil {
		capabilities.EnabledTools = append(capabilities.EnabledTools, "record_episode")
	}
	encoded, err := encodeBounded(capabilities, a.config.MaxEvidenceResponseBytes)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Capabilities response exceeded service limits", false, nil)
		return
	}
	a.recordReadAudit(r.Context(), principal, "capabilities_read", "acr_capabilities", "capabilities", "success", nil)
	writeEncodedJSON(w, http.StatusOK, encoded)
}
