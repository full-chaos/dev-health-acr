package mcp

import (
	"context"
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

// The in-memory STDIO-equivalent handshake negotiates the newest revision the
// STDIO server lists.
func TestInitializeNegotiatesNewestStdioRevision(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	result := client.InitializeResult()
	if result == nil {
		t.Fatal("no initialize result")
	}
	if want := stdioProtocolVersions()[0]; result.ProtocolVersion != want {
		t.Fatalf("negotiated %q, want the newest STDIO revision %q", result.ProtocolVersion, want)
	}
}

// The STDIO server negotiates a revision on which roots/list is still allowed,
// even when the client asks for a newer one, so context_for_task keeps
// resolving its workspace from client roots.
func TestStdioServerNegotiatesRootsCapableRevision(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	ctx := context.Background()
	server := NewServer(boot, "test-version")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	t1, t2 := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, t2, &mcpsdk.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	if got := clientSession.InitializeResult().ProtocolVersion; got != "2025-11-25" {
		t.Fatalf("negotiated %q, want 2025-11-25", got)
	}
	if _, err := serverSession.ListRoots(ctx, nil); err != nil {
		t.Fatalf("roots/list refused on the negotiated revision: %v", err)
	}
}
