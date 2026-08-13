package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ContextFabricOrgModelConfigPath is the per-organization BYO LLM
// configuration surface (CHAOS-3775). Not under /api/v1/agent-context/
// (same reasoning as ContextFabricInvestigationsPath): a distinct
// consumer-neutral surface, listed explicitly in
// internal/contractcheck/openapi.go's allowlist.
const ContextFabricOrgModelConfigPath = "/api/v1/context-fabric/model-config"

// orgModelConfigs returns the configured contextfabric.OrgModelConfigStore,
// or nil if the hosted runtime (or the store within it) is not configured.
// Mirrors (*App).investigator(): Handler() calls this at mux-construction
// time, when a.runtime may itself be nil.
func (a *App) orgModelConfigs() contextfabric.OrgModelConfigStore {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.OrgModelConfigs
}

// orgModelRuntimeEvictor returns the configured
// contextfabric.OrgModelRuntimeEvictor, or nil if none is wired (Codex
// round-1 finding F4). A nil evictor is a valid, non-error state: it means
// no per-organization runtime resolver was ever constructed to evict from
// (see internal/runtime/hosted), so there is nothing a delete could have
// left cached.
func (a *App) orgModelRuntimeEvictor() contextfabric.OrgModelRuntimeEvictor {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.OrgModelRuntimeEvictor
}

// reuseInvalidator returns the configured contextfabric.ReuseInvalidator,
// or nil if none is wired (CHAOS-3786, codex round-1 P1(b)). Mirrors
// orgModelRuntimeEvictor() exactly: a nil invalidator is a valid,
// non-error state (answer reuse itself may be disabled, or no
// reuse-capable investigation-result store was ever composed), not a
// misconfiguration -- see RuntimeDependencies.ReuseInvalidator's doc
// comment.
func (a *App) reuseInvalidator() contextfabric.ReuseInvalidator {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.ReuseInvalidator
}

// ContextFabricOrgModelConfigGetHandler returns the organization's BYO LLM
// configuration with a masked credential (AC-3775-4), or 404 if the
// organization has none configured (in which case the deployment default
// applies).
func (a *App) ContextFabricOrgModelConfigGetHandler(store contextfabric.OrgModelConfigStore) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The nil check lives inside the handler body, after
		// protectedRuntimeHandler has already run auth/scope/entitlement/
		// rate-limit checks -- see ContextFabricInvestigationHandler's
		// matching comment (CHAOS-3755 finding H5): an unauthenticated
		// caller must never observe "not configured" before being
		// authenticated.
		if store == nil {
			a.handleRuntimeUnavailable(w, r)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		config, err := store.GetOrgModelConfig(r.Context(), principal)
		if err != nil {
			if errors.Is(err, contextfabric.ErrOrgModelConfigNotFound) {
				writeError(w, r, http.StatusNotFound, "not_found", "This organization has no BYO LLM configuration; the deployment default applies", false, nil)
				return
			}
			a.writeContextFabricOrgModelConfigError(w, r, err)
			return
		}
		encoded, err := encodeBounded(config, int64(a.config.MaxSerializedBytes))
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric model configuration response exceeded service limits", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "context_fabric_org_model_config_read", "context_fabric_org_model_config", principal.OrgID, "success", nil)
		writeEncodedJSON(w, http.StatusOK, encoded)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextAdmin, true, true, handler)
}

// ContextFabricOrgModelConfigPutHandler upserts the organization's whole BYO
// LLM configuration (full replace, not a partial patch). The credential
// field is required on every write and is never echoed back in the
// response.
//
// invalidator may be nil (see (*App).reuseInvalidator). When non-nil, it
// is called immediately after a successful upsert (CHAOS-3786, codex
// round-1 P1(b)): a primary or fallback model change must invalidate
// reuse for this organization going forward, the same way a projection
// rebuild does -- a stored candidate's chain-membership match is
// authorized by the org's CURRENT chain, and this write is what just
// changed it.
func (a *App) ContextFabricOrgModelConfigPutHandler(store contextfabric.OrgModelConfigStore, invalidator contextfabric.ReuseInvalidator) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			a.handleRuntimeUnavailable(w, r)
			return
		}
		var request contractsv1.ContextFabricOrgModelConfigWriteRequest
		if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil {
			status := http.StatusBadRequest
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			writeError(w, r, status, "invalid_request", "Context Fabric model configuration request is invalid", false, nil)
			return
		}
		if err := request.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Context Fabric model configuration request is invalid", false, nil)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		config, err := store.UpsertOrgModelConfig(r.Context(), principal, request)
		if err != nil {
			a.writeContextFabricOrgModelConfigError(w, r, err)
			return
		}
		if invalidator != nil {
			// A failed invalidation must not be treated as a failed write --
			// the configuration write itself already succeeded and is not
			// rolled back. It is logged so an operator can see reuse was
			// left un-invalidated for this organization, but the request
			// still reports its own success to the caller.
			if err := invalidator.InvalidateOrganizationReuse(r.Context(), principal.OrgID); err != nil {
				a.logger.ErrorContext(r.Context(), "context fabric answer reuse invalidation failed after model config write", "request_id", RequestID(r.Context()), "failure_class", "context_fabric_reuse_invalidation")
			}
		}
		encoded, err := encodeBounded(config, int64(a.config.MaxSerializedBytes))
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric model configuration response exceeded service limits", false, nil)
			return
		}
		// Metadata never carries the credential or its masked form's
		// derivation input -- only the non-secret provider/model choice
		// (AC-3775-4, AC-3770-5: never in a log, a receipt, or telemetry).
		a.recordReadAudit(r.Context(), principal, "context_fabric_org_model_config_written", "context_fabric_org_model_config", principal.OrgID, "success", map[string]any{"provider": config.Provider, "model": config.Model})
		writeEncodedJSON(w, http.StatusOK, encoded)
	})
	// allowWebAssertions=false: this write carries a credential and must
	// stay bearer-only, the same restriction episode writes use ("episode
	// writes must remain bearer-only", TestHostedReadRoutesMatchOpenAPI).
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextAdmin, true, false, handler)
}

// ContextFabricOrgModelConfigDeleteHandler removes the organization's BYO
// LLM configuration; its next request falls through to the deployment
// default (AC-3775-3). Idempotent: deleting an organization with no
// configuration still returns 204.
//
// evictor may be nil (no per-organization runtime resolver composed --
// see (*App).orgModelRuntimeEvictor). When non-nil, it is called
// immediately after a successful delete so a cached runtime holding this
// organization's now-revoked decrypted credential does not stay resident
// in process memory (Codex round-1 finding F4) -- the Generation-keyed
// cache comparison already prevents it from ever being SERVED again after
// a delete-then-recreate, but eviction is what actually frees it.
//
// invalidator may be nil (see (*App).reuseInvalidator). When non-nil, it
// is called immediately after a successful delete for the same CHAOS-3786
// reason ContextFabricOrgModelConfigPutHandler calls it: deleting the
// organization's BYO configuration changes its effective chain back to
// the deployment default, and reuse must not keep matching candidates
// against the chain that just stopped applying.
func (a *App) ContextFabricOrgModelConfigDeleteHandler(store contextfabric.OrgModelConfigStore, evictor contextfabric.OrgModelRuntimeEvictor, invalidator contextfabric.ReuseInvalidator) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			a.handleRuntimeUnavailable(w, r)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		if err := store.DeleteOrgModelConfig(r.Context(), principal); err != nil {
			a.writeContextFabricOrgModelConfigError(w, r, err)
			return
		}
		if evictor != nil {
			evictor.EvictOrgModelRuntime(principal.OrgID)
		}
		if invalidator != nil {
			// See the matching comment in ContextFabricOrgModelConfigPutHandler:
			// a failed invalidation must not be treated as a failed delete.
			if err := invalidator.InvalidateOrganizationReuse(r.Context(), principal.OrgID); err != nil {
				a.logger.ErrorContext(r.Context(), "context fabric answer reuse invalidation failed after model config delete", "request_id", RequestID(r.Context()), "failure_class", "context_fabric_reuse_invalidation")
			}
		}
		a.recordReadAudit(r.Context(), principal, "context_fabric_org_model_config_deleted", "context_fabric_org_model_config", principal.OrgID, "success", nil)
		w.WriteHeader(http.StatusNoContent)
	})
	// allowWebAssertions=false: see ContextFabricOrgModelConfigPutHandler.
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextAdmin, true, false, handler)
}

func (a *App) writeContextFabricOrgModelConfigError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		writeError(w, r, http.StatusGatewayTimeout, "upstream_unavailable", "The Context Fabric model configuration request timed out", true, nil)
		return
	}
	if errors.Is(err, storage.ErrConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "Context Fabric model configuration write conflicted with a concurrent change; retry", true, nil)
		return
	}
	if errors.Is(err, storage.ErrUnavailable) {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Context Fabric model configuration storage is temporarily unavailable", true, nil)
		return
	}
	a.logger.ErrorContext(r.Context(), "context fabric model configuration request failed", "request_id", RequestID(r.Context()), "failure_class", "context_fabric_org_model_config")
	writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric model configuration request failed", false, nil)
}
