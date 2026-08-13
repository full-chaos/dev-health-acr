package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

func validCapabilities() contractsv1.Capabilities {
	return contractsv1.Capabilities{
		Service:                 "dev-health-acr",
		MinimumSidecarVersion:   "1.0.0",
		SupportedSchemaVersions: ourSchemaVersions,
		EnabledTools:            []string{toolContextForTask, toolSourceEvidence, "record_episode"},
		Entitlements:            contractsv1.CapabilityEntitlements{AgentContextRuntime: true},
		Permissions:             contractsv1.CapabilityPermissions{ContextRead: true, EvidenceRead: true},
	}
}

func TestCheckCompatibilitySucceeds(t *testing.T) {
	if err := checkCompatibility(validCapabilities(), "1.2.0", false); err != nil {
		t.Fatalf("expected compatible capabilities to pass, got: %v", err)
	}
}

func TestCheckCompatibilityAllowsPreWritebackHostWhenLocalWritebackDisabled(t *testing.T) {
	// Given
	caps := validCapabilities()

	// When
	err := checkCompatibility(caps, "1.2.0", false)

	// Then
	if err != nil {
		t.Fatalf("expected read-only compatibility with a pre-writeback host, got: %v", err)
	}
}

func TestCheckCompatibilityRequiresWritebackSchemasToolAndScopeWhenEnabled(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contractsv1.Capabilities)
	}{
		{"schemas", func(caps *contractsv1.Capabilities) { caps.SupportedSchemaVersions = ourSchemaVersions }},
		{"tool", func(caps *contractsv1.Capabilities) {
			caps.EnabledTools = []string{toolContextForTask, toolSourceEvidence}
		}},
		{"scope", func(caps *contractsv1.Capabilities) { caps.Permissions.EpisodeWrite = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			caps := validCapabilities()
			caps.SupportedSchemaVersions = append(caps.SupportedSchemaVersions, writebackSchemaVersions...)
			caps.EnabledTools = append(caps.EnabledTools, toolRecordEpisode)
			caps.Permissions.EpisodeWrite = true
			test.mutate(&caps)

			// When
			err := checkCompatibility(caps, "1.2.0", true)

			// Then
			if err == nil {
				t.Fatal("expected writeback compatibility to fail closed")
			}
		})
	}
}

func TestCheckCompatibilitySucceedsWhenWritebackRequirementsArePresent(t *testing.T) {
	// Given
	caps := validCapabilities()
	caps.SupportedSchemaVersions = append(caps.SupportedSchemaVersions, writebackSchemaVersions...)
	caps.EnabledTools = append(caps.EnabledTools, toolRecordEpisode)
	caps.Permissions.EpisodeWrite = true

	// When
	err := checkCompatibility(caps, "1.2.0", true)

	// Then
	if err != nil {
		t.Fatalf("expected writeback-compatible capabilities to pass, got: %v", err)
	}
}

func TestCheckCompatibilityRejectsWrongService(t *testing.T) {
	caps := validCapabilities()
	caps.Service = "some-other-service"
	err := checkCompatibility(caps, "1.2.0", false)
	assertCompatCategory(t, err, "version")
}

func TestCheckCompatibilityRejectsOlderSidecar(t *testing.T) {
	caps := validCapabilities()
	caps.MinimumSidecarVersion = "9.0.0"
	err := checkCompatibility(caps, "1.2.0", false)
	assertCompatCategory(t, err, "version")
}

func TestCheckCompatibilityRejectsDevelopmentVersion(t *testing.T) {
	caps := validCapabilities()
	caps.MinimumSidecarVersion = "dev"
	assertCompatCategory(t, checkCompatibility(caps, "dev", false), "version")
}

func TestCheckCompatibilityRejectsMissingSchemaVersion(t *testing.T) {
	caps := validCapabilities()
	caps.SupportedSchemaVersions = []string{contractsv1.MCPContextForTaskRequestSchema}
	err := checkCompatibility(caps, "1.2.0", false)
	assertCompatCategory(t, err, "version")
}

func TestCheckCompatibilityRejectsMissingTool(t *testing.T) {
	caps := validCapabilities()
	caps.EnabledTools = []string{toolContextForTask}
	err := checkCompatibility(caps, "1.2.0", false)
	assertCompatCategory(t, err, "entitlement")
}

func TestCheckCompatibilityRejectsMissingEntitlement(t *testing.T) {
	caps := validCapabilities()
	caps.Entitlements.AgentContextRuntime = false
	err := checkCompatibility(caps, "1.2.0", false)
	assertCompatCategory(t, err, "entitlement")
}

func TestCheckCompatibilityRejectsMissingScope(t *testing.T) {
	caps := validCapabilities()
	caps.Permissions.EvidenceRead = false
	err := checkCompatibility(caps, "1.2.0", false)
	assertCompatCategory(t, err, "entitlement")
}

func assertCompatCategory(t *testing.T, err error, wantCategory string) {
	t.Helper()
	var ce *compatError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *compatError, got: %v", err)
	}
	if ce.category != wantCategory {
		t.Fatalf("expected category %q, got %q (%v)", wantCategory, ce.category, ce)
	}
}

// TestEffectiveSidecarVersionPrefersExplicitConfigOverride locks the
// operator-override path: when ACR_SIDECAR_VERSION is explicitly set to a
// real value, it wins over the binary's own compiled-in version
// regardless of what that binary version is.
func TestEffectiveSidecarVersionPrefersExplicitConfigOverride(t *testing.T) {
	identity := version.Info{Version: "dev", Commit: "unknown", Date: "unknown"}
	if got := effectiveSidecarVersion("2.0.0", identity); got != "2.0.0" {
		t.Fatalf("expected explicit config override to win, got: %q", got)
	}
}

// TestEffectiveSidecarVersionFallsBackToBinaryVersionWhenConfigIsDevDefault
// is the fail-closed fix this test locks: an operator who never set
// ACR_SIDECAR_VERSION (cfg.SidecarVersion defaults to "dev") must not get
// a free pass on the minimum-sidecar-version compatibility check just
// because they forgot the env var -- a real release binary's own
// compiled-in version is used instead, so a stale installed binary still
// fails a real minimum-version gate with no configuration required.
func TestEffectiveSidecarVersionKeepsReleaseIdentityAuthoritative(t *testing.T) {
	identity := version.Info{Version: "1.5.0", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-07-12T15:04:05Z"}
	if got := effectiveSidecarVersion("2.0.0", identity); got != "1.5.0" {
		t.Fatalf("expected fallback to the real binary version, got: %q", got)
	}
}

// TestEffectiveSidecarVersionStaysDevWhenBothAreDev locks that local,
// unreleased development builds (both sides "dev") remain permissive,
// exactly as versionAtLeast's own non-numeric handling already documents.
func TestEffectiveSidecarVersionStaysDevWhenBothAreDev(t *testing.T) {
	identity := version.Info{Version: "dev", Commit: "unknown", Date: "unknown"}
	if got := effectiveSidecarVersion("dev", identity); got != "dev" {
		t.Fatalf("expected dev/dev to stay dev, got: %q", got)
	}
	if got := effectiveSidecarVersion("latest", identity); got != "dev" {
		t.Fatalf("expected malformed fixture override to stay dev, got: %q", got)
	}
}

// TestCheckCompatibilityRejectsMalformedMinimumSidecarVersion is the
// checkCompatibility-level lock for the versionAtLeast fail-closed fix: a
// hosted API reporting a corrupted, non-"dev" minimum_sidecar_version must
// not silently pass a real release sidecar's compatibility gate.
func TestCheckCompatibilityRejectsMalformedMinimumSidecarVersion(t *testing.T) {
	caps := validCapabilities()
	caps.MinimumSidecarVersion = "not-a-real-version"
	err := checkCompatibility(caps, "1.2.0", false)
	assertCompatCategory(t, err, "version")
}

// TestOurSchemaVersionsAreSubsetOfCanonicalSchemaVersions locks the
// canonical-schema-version-source fix: ourSchemaVersions (every MCP-facing
// schema this sidecar requires) must always be a subset of
// contractsv1.AllSchemaVersions (the single canonical list the hosted API's
// capabilities handshake advertises from). This is the regression test for
// the review finding where the hosted API's hand-typed advertised list
// silently omitted every MCP-only schema version: a future PR that adds a
// new MCP schema requirement without registering it in AllSchemaVersions
// fails here instead of only failing at hosted-API runtime.
func TestOurSchemaVersionsAreSubsetOfCanonicalSchemaVersions(t *testing.T) {
	canonical := make(map[string]bool, len(contractsv1.AllSchemaVersions))
	for _, version := range contractsv1.AllSchemaVersions {
		canonical[version] = true
	}
	for _, want := range ourSchemaVersions {
		if !canonical[want] {
			t.Fatalf("ourSchemaVersions requires %q, which is absent from contractsv1.AllSchemaVersions", want)
		}
	}
	for _, want := range writebackSchemaVersions {
		if !canonical[want] {
			t.Fatalf("writebackSchemaVersions requires %q, which is absent from contractsv1.AllSchemaVersions", want)
		}
	}
}

// TestOurSchemaVersionsIncludesConsumedHostedResponseSchemas locks the
// CHAOS-2908 rereview finding: ourSchemaVersions previously negotiated
// only the MCP wrapper request/response schemas and
// ContextPacketRequestSchema, silently omitting the hosted response
// schemas this sidecar actually decodes and re-serves inside a
// context_for_task response (ContextPacketSchema, ContextPacketItemSchema
// nested within it, EvidenceRefSchema and ExpandedEvidenceSchema returned
// by source_evidence). A hosted API that advertised every MCP-wrapper
// schema but had silently regressed support for one of these consumed
// response shapes would previously pass this sidecar's startup
// compatibility gate and only fail later, opaquely, on the first real
// tool call.
func TestOurSchemaVersionsIncludesConsumedHostedResponseSchemas(t *testing.T) {
	consumed := []string{
		contractsv1.ContextPacketSchema,
		contractsv1.ContextPacketItemSchema,
		contractsv1.EvidenceRefSchema,
		contractsv1.ExpandedEvidenceSchema,
	}
	for _, want := range consumed {
		if !slices.Contains(ourSchemaVersions, want) {
			t.Fatalf("ourSchemaVersions is missing consumed hosted response schema %q", want)
		}
	}
}

// TestGoldenCapabilitiesExamplePassesTheRealStartupGate is the codex
// round-2 F6 regression.
//
// The published capabilities example advertised the answer tools while
// omitting the schema versions checkCompatibility requires, so the document
// the repository holds up as canonical would have been REJECTED by the real
// sidecar at boot. Validating it against its schema did not catch that:
// schema validity and startup compatibility are different properties, and
// only the second one decides whether a deployment works.
//
// This runs the ACTUAL gate against the golden document rather than a
// hand-built fixture, so an example that could not boot a sidecar fails
// here instead of in someone's terminal.
func TestGoldenCapabilitiesExamplePassesTheRealStartupGate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "capabilities.v1.json"))
	if err != nil {
		t.Fatalf("read golden capabilities example: %v", err)
	}
	var capabilities contractsv1.Capabilities
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		t.Fatalf("decode golden capabilities example: %v", err)
	}
	if err := capabilities.Validate(); err != nil {
		t.Fatalf("golden capabilities example is not contract-valid: %v", err)
	}

	// The example must be a document a real sidecar accepts in its default
	// read-only mode.
	if err := checkCompatibility(capabilities, "1.0.0", false); err != nil {
		t.Fatalf("the golden capabilities example fails the real startup gate: %v", err)
	}

	// And it must actually advertise the answer surface, or this test
	// would pass for a document that simply never claimed it.
	for _, tool := range []string{toolInvestigateQuestion, toolInvestigationResult} {
		if !slices.Contains(capabilities.EnabledTools, tool) {
			t.Errorf("golden capabilities example does not advertise %q", tool)
		}
	}
	// Every schema the sidecar requires must be advertised, including the
	// answer contracts it decodes.
	for _, required := range ourSchemaVersions {
		if !slices.Contains(capabilities.SupportedSchemaVersions, required) {
			t.Errorf("golden capabilities example omits required schema %q", required)
		}
	}

	// SET EQUALITY against the canonical list, both directions (codex
	// round-3 P2-5). A subset check only catches an omission; it cannot
	// catch a stale extra entry, and it lets the golden document drift
	// quietly behind AllSchemaVersions as new contracts land -- which is
	// exactly how it came to advertise the answer tools without the
	// schemas they need.
	advertised := append([]string(nil), capabilities.SupportedSchemaVersions...)
	canonical := append([]string(nil), contractsv1.AllSchemaVersions...)
	sort.Strings(advertised)
	sort.Strings(canonical)
	if !slices.Equal(advertised, canonical) {
		t.Errorf("golden capabilities example advertises a different schema set than AllSchemaVersions.\n advertised: %v\n canonical:  %v", advertised, canonical)
	}
}
