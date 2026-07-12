package mcp

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ourSchemaVersions lists every MCP-facing schema this sidecar speaks.
// checkCompatibility requires the hosted API to support all of them.
var ourSchemaVersions = []string{
	contractsv1.MCPContextForTaskRequestSchema,
	contractsv1.MCPContextForTaskResponseSchema,
	contractsv1.MCPSourceEvidenceRequestSchema,
	contractsv1.MCPSourceEvidenceResponseSchema,
	contractsv1.ContextPacketRequestSchema,
	// The MCP wrapper schemas above are not the only hosted-side shapes
	// this sidecar depends on: context_for_task decodes and re-serves a
	// full ContextPacket (which nests ContextPacketItem), and
	// source_evidence decodes and re-serves an ExpandedEvidence (which
	// nests EvidenceRef). Every schema this sidecar actually parses from a
	// hosted response belongs in this negotiated set, not only the
	// request/response envelope schemas, so a hosted API that regressed
	// support for one of these consumed shapes fails this startup
	// compatibility gate instead of only failing later, opaquely, on the
	// first real tool call.
	contractsv1.ContextPacketSchema,
	contractsv1.ContextPacketItemSchema,
	contractsv1.EvidenceRefSchema,
	contractsv1.ExpandedEvidenceSchema,
}

// devVersionSentinel mirrors internal/sidecar's own unexported
// defaultSidecarVersion: both this package's compiled-in binary version
// (cmd/acr-mcp's "dev" default, overridden via -ldflags at release build
// time) and sidecar.Config.SidecarVersion (ACR_SIDECAR_VERSION, same
// default) use this exact literal for "no real version configured".
const devVersionSentinel = "dev"

// effectiveSidecarVersion picks the sidecar version identity
// checkCompatibility enforces against the hosted API's minimum_sidecar_
// version. An explicit, non-default ACR_SIDECAR_VERSION always wins (an
// operator override for canary/rollback testing). Otherwise, a real
// release binary's own compiled-in version is authoritative: this is the
// fail-closed fix over trusting cfgVersion alone, since cfgVersion
// silently defaults to "dev" -- which versionAtLeast always treats as
// compatible -- whenever an operator simply forgets to set the env var,
// even on a genuine stale release binary that should fail the check.
func effectiveSidecarVersion(cfgVersion, binaryVersion string) string {
	if cfgVersion != devVersionSentinel && cfgVersion != "" {
		return cfgVersion
	}
	if binaryVersion != devVersionSentinel && binaryVersion != "" {
		return binaryVersion
	}
	return cfgVersion
}

// checkCompatibility enforces that the hosted API's capability descriptor
// is compatible with this sidecar before any tool call is accepted:
// service identity, minimum sidecar version, required schema versions,
// both read tools enabled, and both read entitlement/permission bits set.
// Every returned error is a *compatError with a fixed, safe message.
func checkCompatibility(caps contractsv1.Capabilities, sidecarVersion string) error {
	const wantService = "dev-health-acr"
	if caps.Service != wantService {
		return &compatError{category: "version", detail: "hosted API service identity does not match this sidecar"}
	}
	if !versionAtLeast(sidecarVersion, caps.MinimumSidecarVersion) {
		return &compatError{category: "version", detail: "sidecar version is older than the hosted API's minimum supported version"}
	}
	for _, want := range ourSchemaVersions {
		if !slices.Contains(caps.SupportedSchemaVersions, want) {
			return &compatError{category: "version", detail: "hosted API does not support a schema version this sidecar requires"}
		}
	}
	for _, want := range []string{toolContextForTask, toolSourceEvidence} {
		if !slices.Contains(caps.EnabledTools, want) {
			return &compatError{category: "entitlement", detail: "hosted API has not enabled a tool this sidecar exposes"}
		}
	}
	if !caps.Entitlements.AgentContextRuntime {
		return &compatError{category: "entitlement", detail: "agent_context_runtime entitlement is not enabled for this credential's organization"}
	}
	if !caps.Permissions.ContextRead || !caps.Permissions.EvidenceRead {
		return &compatError{category: "entitlement", detail: "credential is missing context:read or evidence:read scope"}
	}
	return nil
}

// compatError is a fixed, safe-to-print startup compatibility failure. It
// never wraps or includes any error text derived from network responses,
// credentials, or file paths.
type compatError struct {
	category string
	detail   string
}

func (e *compatError) Error() string {
	return fmt.Sprintf("acr-mcp: %s incompatibility: %s", e.category, e.detail)
}

// versionAtLeast reports whether have >= want using dotted numeric
// component comparison (e.g. "1.4.2" >= "1.3.0"). The literal
// devVersionSentinel ("dev") on either side is a deliberate exemption --
// preserved exactly as before -- so a local, unreleased development build
// (have == "dev") is never blocked by this comparison, and a hosted API
// running in local/fixture dev mode (want == "dev") never blocks a real
// sidecar either. Any other unparseable value, on either side, fails
// closed (returns false) rather than being treated as automatically
// satisfied: a corrupted or unexpected minimum_sidecar_version from the
// hosted side (garbage that is not the "dev" sentinel) must not silently
// let every real release sidecar through, and a real release build's own
// compiled-in version is always a dotted numeric string, so a non-"dev",
// unparseable have here indicates a genuine configuration defect that
// should also fail closed rather than pass by default.
func versionAtLeast(have, want string) bool {
	if have == devVersionSentinel || want == devVersionSentinel {
		return true
	}
	haveParts, ok1 := parseDottedVersion(have)
	wantParts, ok2 := parseDottedVersion(want)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < len(haveParts) || i < len(wantParts); i++ {
		var h, w int
		if i < len(haveParts) {
			h = haveParts[i]
		}
		if i < len(wantParts) {
			w = wantParts[i]
		}
		if h != w {
			return h > w
		}
	}
	return true
}

func parseDottedVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	fields := strings.Split(v, ".")
	parts := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, len(parts) > 0
}
