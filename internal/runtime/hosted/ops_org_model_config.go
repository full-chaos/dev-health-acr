package hosted

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelconfig"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// EnvOrgLLMConfigSource overrides where per-organization BYO LLM
// configuration comes from. The default (unset or "ops") is Dev Health's own
// settings store (the web AI Setup > BYO LLM page writes it), read through
// ops' internal llm-config route with the entitlement credential; acr then
// holds no BYO store and no settings encryption key. "off" turns the ops
// source off: acr's own store when its credential encryption keys are
// configured, else no per-organization source.
const EnvOrgLLMConfigSource = "ACR_CONTEXT_FABRIC_ORG_LLM_CONFIG_SOURCE"

const (
	orgLLMConfigSourceOps = "ops"
	orgLLMConfigSourceOff = "off"
)

type orgLLMConfigSource interface {
	OrgLLMConfig(ctx context.Context, orgID string) (entitlements.OrgLLMConfig, bool, error)
}

// buildOrgModelConfigResolver returns the per-organization config source, or
// a nil interface when there is none. One store only. Unset defaults to ops
// when the Dev Health entitlement client is wired and acr's own store is off;
// a deployment without that client, or one that still configures acr's store,
// keeps working on what it has. An explicit "ops" is strict and refuses to
// start when it cannot be honoured.
func buildOrgModelConfigResolver(acrStore *pgmodelconfig.Store, entitlement entitlementChecker, lookup func(string) (string, bool), logger *slog.Logger) (contextfabric.OrgModelConfigResolver, error) {
	value, _ := lookup(EnvOrgLLMConfigSource)
	source, hasOps := entitlement.(orgLLMConfigSource)
	switch strings.TrimSpace(value) {
	case "", orgLLMConfigSourceOps:
		explicit := strings.TrimSpace(value) != ""
		if acrStore != nil {
			if explicit {
				return nil, fmt.Errorf("%s=ops needs acr's own model config store off (one BYO store): unset the credential encryption keys", EnvOrgLLMConfigSource)
			}
			return acrStore, nil
		}
		if !hasOps {
			if explicit {
				return nil, fmt.Errorf("%s=ops needs the Dev Health entitlement client (ACR_DEV_HEALTH_ENTITLEMENT_URL and its token file)", EnvOrgLLMConfigSource)
			}
			return nil, nil
		}
		if logger != nil {
			logger.Info("context fabric org model config source", "source", contextfabric.SanitizeLogAttr(orgLLMConfigSourceOps))
		}
		return opsOrgModelConfigs{source: source, generations: &versionGenerations{}}, nil
	case orgLLMConfigSourceOff:
		if acrStore == nil {
			return nil, nil
		}
		return acrStore, nil
	default:
		return nil, fmt.Errorf("%s must be empty, %q or %q", EnvOrgLLMConfigSource, orgLLMConfigSourceOps, orgLLMConfigSourceOff)
	}
}

// opsOrgModelConfigs serves contextfabric.OrgModelConfigResolver from ops.
// No BYO config (404) is ok=false: the platform model answers. Anything else
// that is not a usable config is an error: modelruntimeresolver turns it
// into "unavailable for this organization" and never uses the platform
// model in its place.
type opsOrgModelConfigs struct {
	source      orgLLMConfigSource
	generations *versionGenerations
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
		return contextfabric.ResolvedOrgModelConfig{}, false, err
	}
	if !config.APIKey.Configured() && config.BaseURL == "" {
		return contextfabric.ResolvedOrgModelConfig{}, false, errors.Join(entitlements.ErrLLMConfigUnusable, errors.New("no credential for a hosted provider endpoint"))
	}
	return contextfabric.ResolvedOrgModelConfig{
		Provider: config.Provider, BaseURL: config.BaseURL, Model: config.Model, Credential: config.APIKey,
		// The cache key of the org's built runtime: it moves whenever ops'
		// version (a digest over the stored rows) changes.
		Generation: o.generations.generationFor(orgID, config.Version),
	}, true, nil
}
