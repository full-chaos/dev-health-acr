//go:build sizeproof

package falkorgraph

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

// embedproviderEnvVars is a small, human-readable CROSS-CHECK list of every
// embedprovider.Env* configuration key expected to exist today -- NOT the
// closure guarantee itself (see TestBenchmarkLookupCoversEveryEmbedproviderEnvVar
// below, which is round-4-hardened to need no such list: it binds directly
// to discoverEmbedproviderEnvVars' parsed output, so a NEW constant is
// caught with zero human list-maintenance). This slice exists only so
// TestEmbedproviderEnvVarsListMatchesPackageSource can flag drift between
// what a reader of this file expects and what the source package actually
// declares, in either direction -- it plays no role in whether the closure
// test itself catches an omission.
var embedproviderEnvVars = []string{
	embedprovider.EnvProvider,
	embedprovider.EnvBaseURL,
	embedprovider.EnvModel,
	embedprovider.EnvDimension,
	embedprovider.EnvAPIKey,
	embedprovider.EnvSimilarityFloor,
	embedprovider.EnvTimeout,
	embedprovider.EnvBatchTimeout,
	embedprovider.EnvMaxBatch,
	embedprovider.EnvMaxTextRunes,
	embedprovider.EnvPrefixFamily,
	embedprovider.EnvExpectResponseModel,
	embedprovider.EnvMaxTransportRetries,
	embedprovider.EnvAllowInsecureBaseURL,
	embedprovider.EnvProviderLocality,
	embedprovider.EnvIncludeBodies,
	embedprovider.EnvAllowNoCredential,
}

// TestEmbedproviderEnvVarsListMatchesPackageSource is a CROSS-CHECK, not the
// closure guarantee itself (see TestBenchmarkLookupCoversEveryEmbedproviderEnvVar,
// which binds directly to discoverEmbedproviderEnvVars and needs no human
// list-maintenance to catch a new constant): it keeps the small,
// human-readable embedproviderEnvVars slice honest against the dynamically
// parsed set, failing in EITHER direction -- an entry in the slice that no
// longer exists in the package, or a constant in the package missing from
// the slice -- so the slice never quietly goes stale as documentation even
// though it is not what makes the closure test itself bite.
func TestEmbedproviderEnvVarsListMatchesPackageSource(t *testing.T) {
	discovered := discoverEmbedproviderEnvVars(t)
	discoveredSet := make(map[string]bool, len(discovered))
	for _, name := range discovered {
		discoveredSet[name] = true
	}
	listedSet := make(map[string]bool, len(embedproviderEnvVars))
	for _, name := range embedproviderEnvVars {
		listedSet[name] = true
	}
	for _, name := range discovered {
		if !listedSet[name] {
			t.Errorf("embedprovider defines %s but embedproviderEnvVars does not list it -- add it there (and to benchmarkLookupEnvVarFor / benchmarkLookup's switch if it needs a case)", name)
		}
	}
	for _, name := range embedproviderEnvVars {
		if !discoveredSet[name] {
			t.Errorf("embedproviderEnvVars lists %s but embedprovider no longer defines a matching exported Env*-prefixed string constant -- remove it", name)
		}
	}
}
