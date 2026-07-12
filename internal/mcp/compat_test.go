package mcp

import (
	"errors"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	if err := checkCompatibility(validCapabilities(), "1.2.0"); err != nil {
		t.Fatalf("expected compatible capabilities to pass, got: %v", err)
	}
}

func TestCheckCompatibilityRejectsWrongService(t *testing.T) {
	caps := validCapabilities()
	caps.Service = "some-other-service"
	err := checkCompatibility(caps, "1.2.0")
	assertCompatCategory(t, err, "version")
}

func TestCheckCompatibilityRejectsOlderSidecar(t *testing.T) {
	caps := validCapabilities()
	caps.MinimumSidecarVersion = "9.0.0"
	err := checkCompatibility(caps, "1.2.0")
	assertCompatCategory(t, err, "version")
}

func TestCheckCompatibilityAllowsUnparseableDevVersions(t *testing.T) {
	caps := validCapabilities()
	caps.MinimumSidecarVersion = "dev"
	if err := checkCompatibility(caps, "dev"); err != nil {
		t.Fatalf("expected dev/dev version comparison to be permissive, got: %v", err)
	}
}

func TestCheckCompatibilityRejectsMissingSchemaVersion(t *testing.T) {
	caps := validCapabilities()
	caps.SupportedSchemaVersions = []string{contractsv1.MCPContextForTaskRequestSchema}
	err := checkCompatibility(caps, "1.2.0")
	assertCompatCategory(t, err, "version")
}

func TestCheckCompatibilityRejectsMissingTool(t *testing.T) {
	caps := validCapabilities()
	caps.EnabledTools = []string{toolContextForTask}
	err := checkCompatibility(caps, "1.2.0")
	assertCompatCategory(t, err, "entitlement")
}

func TestCheckCompatibilityRejectsMissingEntitlement(t *testing.T) {
	caps := validCapabilities()
	caps.Entitlements.AgentContextRuntime = false
	err := checkCompatibility(caps, "1.2.0")
	assertCompatCategory(t, err, "entitlement")
}

func TestCheckCompatibilityRejectsMissingScope(t *testing.T) {
	caps := validCapabilities()
	caps.Permissions.EvidenceRead = false
	err := checkCompatibility(caps, "1.2.0")
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

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		have, want string
		wantOK     bool
	}{
		{"1.2.0", "1.0.0", true},
		{"1.0.0", "1.2.0", false},
		{"1.2.0", "1.2.0", true},
		{"2.0.0", "1.9.9", true},
		{"v1.2.0", "v1.1.0", true},
		{"dev", "1.0.0", true},
		{"1.0.0", "dev", true},
		// A malformed (non-"dev", non-dotted-numeric) minimum from the hosted
		// side must fail closed, not silently pass every real release build.
		{"1.2.0", "not-a-version", false},
		{"1.2.0", "1.2.x", false},
		{"1.2.0", "", false},
		// A malformed, non-"dev" have (should not occur from a real compiled
		// binary, but defensively must not pass by default either).
		{"garbage", "1.0.0", false},
	}
	for _, tc := range cases {
		if got := versionAtLeast(tc.have, tc.want); got != tc.wantOK {
			t.Errorf("versionAtLeast(%q, %q) = %v, want %v", tc.have, tc.want, got, tc.wantOK)
		}
	}
}

// TestEffectiveSidecarVersionPrefersExplicitConfigOverride locks the
// operator-override path: when ACR_SIDECAR_VERSION is explicitly set to a
// real value, it wins over the binary's own compiled-in version
// regardless of what that binary version is.
func TestEffectiveSidecarVersionPrefersExplicitConfigOverride(t *testing.T) {
	if got := effectiveSidecarVersion("2.0.0", "1.5.0"); got != "2.0.0" {
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
func TestEffectiveSidecarVersionFallsBackToBinaryVersionWhenConfigIsDevDefault(t *testing.T) {
	if got := effectiveSidecarVersion("dev", "1.5.0"); got != "1.5.0" {
		t.Fatalf("expected fallback to the real binary version, got: %q", got)
	}
}

// TestEffectiveSidecarVersionStaysDevWhenBothAreDev locks that local,
// unreleased development builds (both sides "dev") remain permissive,
// exactly as versionAtLeast's own non-numeric handling already documents.
func TestEffectiveSidecarVersionStaysDevWhenBothAreDev(t *testing.T) {
	if got := effectiveSidecarVersion("dev", "dev"); got != "dev" {
		t.Fatalf("expected dev/dev to stay dev, got: %q", got)
	}
	if got := effectiveSidecarVersion("", ""); got != "" {
		t.Fatalf("expected empty/empty to stay empty, got: %q", got)
	}
}

// TestCheckCompatibilityRejectsMalformedMinimumSidecarVersion is the
// checkCompatibility-level lock for the versionAtLeast fail-closed fix: a
// hosted API reporting a corrupted, non-"dev" minimum_sidecar_version must
// not silently pass a real release sidecar's compatibility gate.
func TestCheckCompatibilityRejectsMalformedMinimumSidecarVersion(t *testing.T) {
	caps := validCapabilities()
	caps.MinimumSidecarVersion = "not-a-real-version"
	err := checkCompatibility(caps, "1.2.0")
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
