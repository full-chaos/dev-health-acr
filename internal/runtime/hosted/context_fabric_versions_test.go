package hosted

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// CHAOS-3810 codex round-1 P1. The composed version identity was half-filled:
// Backend and ProjectionVersion were supplied, QueryVersion and
// CanonicalServiceVersion were not, so every result stamped "unwired" for two
// fields whose authorities exist in this repository -- and a terminal
// clarification_required/no_match result, which reads no fact bundle to
// recover CanonicalServiceVersion from, was permanently unattributable across
// a rebuild.
//
// Asserted field by field against the AUTHORITY, not against a copied
// literal: a test that repeated the strings would keep passing if an
// authority moved.
func TestContextFabricSynthesizerOptionsCarryEveryStaticVersion(t *testing.T) {
	t.Parallel()
	options := contextFabricSynthesizerOptions("acr-test-1.2.3")

	if options.ServiceVersion != "acr-test-1.2.3" {
		t.Fatalf("ServiceVersion = %q, want the composition's own service version", options.ServiceVersion)
	}
	if options.Backend != "graph" {
		t.Fatalf("Backend = %q, want the vendor-neutral capability class", options.Backend)
	}
	if options.ProjectionVersion != contextFabricProjectionVersion {
		t.Fatalf("ProjectionVersion = %q, want %q", options.ProjectionVersion, contextFabricProjectionVersion)
	}
	if options.QueryVersion != devhealthfacts.QueryVersion {
		t.Fatalf("QueryVersion = %q, want devhealthfacts.QueryVersion (%q)", options.QueryVersion, devhealthfacts.QueryVersion)
	}
	if options.CanonicalServiceVersion != contextfabric.CanonicalFactRegistryVersion {
		t.Fatalf("CanonicalServiceVersion = %q, want contextfabric.CanonicalFactRegistryVersion (%q)", options.CanonicalServiceVersion, contextfabric.CanonicalFactRegistryVersion)
	}
	// The whole point of the finding: nothing here may fall through to the
	// placeholder. StaticResultVersions is what a terminal result reads.
	static := contextfabric.RuntimeAnswerSynthesizer{Options: options}.StaticResultVersions()
	for name, value := range map[string]string{
		"service_version":           static.ServiceVersion,
		"backend":                   static.Backend,
		"projection_version":        static.ProjectionVersion,
		"query_version":             static.QueryVersion,
		"canonical_service_version": static.CanonicalServiceVersion,
	} {
		if strings.TrimSpace(value) == "" || value == "unwired" {
			t.Fatalf("%s = %q, want a real version: composition has an authority for it", name, value)
		}
	}
}

// TestContextFabricProjectionVersionComposesEverySourceVersion pins the
// CHAOS-3833 P1-2 fix: the projection version reuse compares against must
// compose ALL of devhealthsource's producer version constants. The
// pre-fix form omitted TeamsProjectsSourceVersion (behind a comment that
// went stale when CHAOS-3802 turned the stub into a real producer), so a
// teams/projects-only producer change would not have moved the reuse key.
// Asserted against the authorities, not a repeated literal, for the same
// reason as the test above; strings.Contains rather than an exact
// composition so a future FOURTH source version fails this test only if
// it is forgotten, not merely reordered.
func TestContextFabricProjectionVersionComposesEverySourceVersion(t *testing.T) {
	t.Parallel()
	for _, sourceVersion := range []string{
		devhealthsource.ClickHouseSourceVersion,
		devhealthsource.EpisodesSourceVersion,
		devhealthsource.TeamsProjectsSourceVersion,
	} {
		if !strings.Contains(contextFabricProjectionVersion, sourceVersion) {
			t.Fatalf("contextFabricProjectionVersion = %q, missing source version %q", contextFabricProjectionVersion, sourceVersion)
		}
	}
}
