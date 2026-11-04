package hosted

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// removedEnvOrgLLMConfigSource was the setting that chose where per-organization
// BYO LLM configuration comes from (CHAOS-12997, chris 2026-11-04: everything
// stays in ops). It no longer changes anything; a deployment that still sets it
// is told once at start-up.
const removedEnvOrgLLMConfigSource = "ACR_CONTEXT_FABRIC_ORG_LLM_CONFIG_SOURCE"

// removedEnvServiceModelDefault was the switch that treated every organization
// as holding acr_service_model and platform_model_fallback (CHAOS-12997, chris
// 2026-11-04: a model is BYO, or the feature flags grant the platform model).
// It is ignored, even in production where it used to be refused.
const removedEnvServiceModelDefault = "ACR_MCP_SERVICE_MODEL_DEFAULT"

type orgLLMConfigSource interface {
	OrgLLMConfig(ctx context.Context, orgID string) (entitlements.OrgLLMConfig, bool, error)
}

// buildOrgModelConfigResolver returns the per-organization config source: the
// settings store of Dev Health (the web AI Setup > BYO LLM page writes it), read
// through ops' internal llm-config route with the entitlement credential. acr
// holds no BYO store and no settings encryption key. With no Dev Health
// entitlement client (local/dev) there is no per-organization source and it
// returns a nil interface: the platform model answers every organization.
func buildOrgModelConfigResolver(entitlement entitlementChecker, lookup func(string) (string, bool), logger *slog.Logger) contextfabric.OrgModelConfigResolver {
	if value, set := lookup(removedEnvOrgLLMConfigSource); set && logger != nil {
		logger.Warn("context fabric org model config source: the setting is removed and ignored; per-organization BYO configuration is read from ops whenever a Dev Health entitlement client is configured", "setting", removedEnvOrgLLMConfigSource, "value_set", strings.TrimSpace(value) != "")
	}
	if value, set := lookup(removedEnvServiceModelDefault); set && logger != nil {
		logger.Warn("context fabric service model: the setting is removed and ignored; an organization without its own model draws the platform model only with acr_service_model and platform_model_fallback", "setting", removedEnvServiceModelDefault, "value_set", strings.TrimSpace(value) != "")
	}
	source, hasOps := entitlement.(orgLLMConfigSource)
	if !hasOps {
		if logger != nil {
			logger.Info("context fabric org model config source: none (no Dev Health entitlement client configured); the platform model answers every organization")
		}
		return nil
	}
	if logger != nil {
		logger.Info("context fabric org model config source", "source", "ops")
	}
	return opsOrgModelConfigs{source: source, generations: &versionGenerations{}, logger: logger}
}

// opsOrgModelConfigs serves contextfabric.OrgModelConfigResolver from ops.
// No BYO config (404) is ok=false: the service model gate then decides
// whether the platform model may answer. Anything else that is not a usable
// config (a closed byo_llm gate included) is an error: modelruntimeresolver
// turns it into "unavailable for this organization" and never uses the
// platform model in its place.
type opsOrgModelConfigs struct {
	source      orgLLMConfigSource
	generations *versionGenerations
	logger      *slog.Logger
}

// versionGenerations turns ops' opaque version (a digest over the org's
// stored rows) into the per-organization number modelruntimeresolver keys
// its built runtimes on: the same version keeps its number, and a version
// that differs from the last one seen for the org gets a higher one.
type versionGenerations struct {
	mu   sync.Mutex
	last map[string]generationEntry
	next int64
}

type generationEntry struct {
	version    string
	generation int64
}

func (v *versionGenerations) generationFor(orgID, version string) int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.last == nil {
		v.last = map[string]generationEntry{}
	}
	if entry, ok := v.last[orgID]; ok && entry.version == version {
		return entry.generation
	}
	v.next++
	v.last[orgID] = generationEntry{version: version, generation: v.next}
	return v.next
}

func (o opsOrgModelConfigs) ResolveOrgModelConfig(ctx context.Context, orgID string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	config, found, err := o.source.OrgLLMConfig(ctx, orgID)
	if err != nil || !found {
		var refused *entitlements.BaseURLRefusedError
		if errors.As(err, &refused) && o.logger != nil {
			o.logger.WarnContext(ctx, "context fabric org model config refused", "org_id", contextfabric.SanitizeLogAttr(orgID), "reason", contextfabric.SanitizeLogAttr(refused.Reason))
		}
		return contextfabric.ResolvedOrgModelConfig{}, false, err
	}
	if !config.APIKey.Configured() && config.BaseURL == "" {
		return contextfabric.ResolvedOrgModelConfig{}, false, errors.Join(entitlements.ErrLLMConfigUnusable, errors.New("no credential for a hosted provider endpoint"))
	}
	return contextfabric.ResolvedOrgModelConfig{
		Provider: config.Provider, BaseURL: config.BaseURL, Model: config.Model, Credential: config.APIKey, PromptCache: config.PromptCache,
		// The cache key of the org's built runtime: it moves whenever ops'
		// version (a digest over the stored rows) changes.
		Generation: o.generations.generationFor(orgID, config.Version),
	}, true, nil
}
