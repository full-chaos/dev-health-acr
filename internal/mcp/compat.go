package mcp

import (
	"fmt"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

// ourSchemaVersions are required for every sidecar, including the default
// read-only mode.
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

var writebackSchemaVersions = []string{
	contractsv1.MCPRecordEpisodeRequestSchema,
	contractsv1.MCPRecordEpisodeResponseSchema,
	contractsv1.AgentEpisodeCreateSchema,
	contractsv1.AgentEpisodeSchema,
}

func effectiveSidecarVersion(cfgVersion string, identity version.Info) string {
	return version.EffectiveVersion(identity, cfgVersion)
}

// checkCompatibility enforces that the hosted API's capability descriptor
// is compatible with this sidecar before any tool call is accepted:
// service identity, minimum sidecar version, required schema versions,
// both read tools enabled, and both read entitlement/permission bits set.
// Writeback requirements are enforced only when local writeback is enabled.
// Every returned error is a *compatError with a fixed, safe message.
func checkCompatibility(caps contractsv1.Capabilities, sidecarVersion string, writebackEnabled bool) error {
	const wantService = "dev-health-acr"
	if caps.Service != wantService {
		return &compatError{category: "version", detail: "hosted API service identity does not match this sidecar"}
	}
	if !version.AtLeast(sidecarVersion, caps.MinimumSidecarVersion) {
		return &compatError{category: "version", detail: "sidecar version is older than the hosted API's minimum supported version"}
	}
	for _, want := range ourSchemaVersions {
		if !slices.Contains(caps.SupportedSchemaVersions, want) {
			return &compatError{category: "version", detail: "hosted API does not support a schema version this sidecar requires"}
		}
	}
	if writebackEnabled {
		for _, want := range writebackSchemaVersions {
			if !slices.Contains(caps.SupportedSchemaVersions, want) {
				return &compatError{category: "version", detail: "hosted API does not support a writeback schema version this sidecar requires"}
			}
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
	if writebackEnabled && (!slices.Contains(caps.EnabledTools, toolRecordEpisode) || !caps.Permissions.EpisodeWrite) {
		return &compatError{category: "entitlement", detail: "credential is missing episode:write scope or hosted record_episode availability"}
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
