package api

import (
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
		a.logger.ErrorContext(r.Context(), "capabilities resolution failed", "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "failure_class", "capabilities_provider")
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
	// The CHAOS-3746 answer tools are advertised only when the
	// investigation surface can actually serve them. context:read alone is
	// not enough: investigating needs a composed Investigator and
	// re-reading a result needs the result store. Advertising a tool the
	// sidecar would then fail on turns a composition gap into a confusing
	// per-call error instead of an honest capability answer -- the same
	// reason record_episode below is gated on a.runtime.Episodes.
	if entitled && capabilities.Permissions.ContextRead && a.investigator() != nil {
		capabilities.EnabledTools = append(capabilities.EnabledTools, "investigate_question")
	}
	if entitled && capabilities.Permissions.ContextRead && a.investigationResults() != nil {
		capabilities.EnabledTools = append(capabilities.EnabledTools, "investigation_result")
	}
	// CHAOS-7073: read_facts needs a composed direct facts reader, exactly as
	// the answer tools need their composed dependencies.
	if entitled && capabilities.Permissions.ContextRead && a.directFacts() != nil {
		capabilities.EnabledTools = append(capabilities.EnabledTools, "read_facts")
	}
	// CHAOS-7072 (S1a, design E.5): the direct data tools, each advertised
	// only when this deployment can serve it for this caller. data_catalog
	// and find_subjects need context:read and a composed graph (the lookup);
	// run_operation needs the separate data:read scope AND a runner, which
	// exists only when the internal query service URL is configured and the
	// operation policy loaded, AND the subject gate over a real graph.
	if entitled && capabilities.Permissions.ContextRead && a.dataSubjects() != nil {
		capabilities.EnabledTools = append(capabilities.EnabledTools, "data_catalog", "find_subjects")
	}
	if entitled && auth.HasScope(principal.Permissions, auth.ScopeDataRead) && a.dataOperations() != nil && a.dataGateComposed() {
		capabilities.EnabledTools = append(capabilities.EnabledTools, "run_operation")
	}
	// CHAOS-7074: read_relationships needs its composed reader.
	if entitled && capabilities.Permissions.ContextRead && a.directRelationships() != nil {
		capabilities.EnabledTools = append(capabilities.EnabledTools, "read_relationships")
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
