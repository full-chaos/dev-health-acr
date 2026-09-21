package mcp

import (
	"slices"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The hosted MCP transport depends on the linked SDK speaking the 2026-07-28
// revision while still negotiating every older revision the STDIO sidecar
// serves today.
func TestLinkedSDKSupportsProtocolRevisions(t *testing.T) {
	supported := mcpsdk.SupportedProtocolVersions()
	for _, want := range []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"} {
		if !slices.Contains(supported, want) {
			t.Errorf("linked SDK does not support protocol revision %s; supported=%v", want, supported)
		}
	}
}

// The in-memory STDIO-equivalent handshake must keep negotiating a revision the
// linked SDK lists as supported.
func TestInitializeNegotiatesSupportedRevision(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	result := client.InitializeResult()
	if result == nil {
		t.Fatal("no initialize result")
	}
	if !slices.Contains(mcpsdk.SupportedProtocolVersions(), result.ProtocolVersion) {
		t.Fatalf("negotiated %q, not in supported %v", result.ProtocolVersion, mcpsdk.SupportedProtocolVersions())
	}
}
