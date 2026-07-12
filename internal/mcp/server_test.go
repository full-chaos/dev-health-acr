package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectedClient starts NewServer(boot, "test-version") over an in-memory
// transport and returns a connected *mcpsdk.ClientSession exercising the
// real initialize handshake, for wire-level tests (tools/list, tools/call)
// that a hand-built ToolHandler call cannot cover.
func connectedClient(t *testing.T, boot *Bootstrap) (*mcpsdk.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()
	server := NewServer(boot, "test-version")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)

	t1, t2 := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return clientSession, func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	}
}

func TestServerListsExactlyTwoReadOnlyTools(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	result, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("expected exactly 2 tools, got %d: %#v", len(result.Tools), result.Tools)
	}
	names := map[string]*mcpsdk.Tool{}
	for _, tool := range result.Tools {
		names[tool.Name] = tool
	}
	for _, name := range []string{toolContextForTask, toolSourceEvidence} {
		tool, ok := names[name]
		if !ok {
			t.Fatalf("expected tool %q to be listed", name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("expected tool %q to be read-only", name)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("expected tool %q to have destructiveHint=false", name)
		}
	}
	if _, ok := names["record_episode"]; ok {
		t.Fatal("record_episode must never be registered")
	}
}

func TestServerToolSchemasMatchCanonicalManifest(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	result, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcpsdk.Tool{}
	for _, tool := range result.Tools {
		byName[tool.Name] = tool
	}

	cases := []struct {
		name         string
		inputSchema  string
		outputSchema string
	}{
		{toolContextForTask, contextForTaskRequestSchemaFile, contextForTaskResponseSchemaFile},
		{toolSourceEvidence, sourceEvidenceRequestSchemaFile, sourceEvidenceResponseSchemaFile},
	}
	for _, tc := range cases {
		tool := byName[tc.name]
		assertSchemaEqual(t, tc.name+" input", tool.InputSchema, mustReadSchema(tc.inputSchema))
		assertSchemaEqual(t, tc.name+" output", tool.OutputSchema, mustReadSchema(tc.outputSchema))
	}
}

func assertSchemaEqual(t *testing.T, label string, got any, want json.RawMessage) {
	t.Helper()
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: marshal got schema: %v", label, err)
	}
	var gotNorm, wantNorm any
	if err := json.Unmarshal(gotBytes, &gotNorm); err != nil {
		t.Fatalf("%s: unmarshal got schema: %v", label, err)
	}
	if err := json.Unmarshal(want, &wantNorm); err != nil {
		t.Fatalf("%s: unmarshal want schema: %v", label, err)
	}
	gotCanon, _ := json.Marshal(gotNorm)
	wantCanon, _ := json.Marshal(wantNorm)
	if string(gotCanon) != string(wantCanon) {
		t.Fatalf("%s: schema mismatch\ngot:  %s\nwant: %s", label, gotCanon, wantCanon)
	}
}

func TestServerCallToolSuccessForBothTools(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	ctxResult, err := client.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: toolContextForTask,
		Arguments: map[string]any{
			"goal":       "investigate flaky checkout tests",
			"repository": map[string]any{"slug": "acme/widgets"},
			"scope":      map[string]any{"branch": "main"},
		},
	})
	if err != nil {
		t.Fatalf("context_for_task call failed at the protocol level: %v", err)
	}
	if ctxResult.IsError {
		t.Fatalf("expected context_for_task success, got: %#v", ctxResult.Content)
	}

	evResult, err := client.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      toolSourceEvidence,
		Arguments: map[string]any{"evidence_ref_id": "ev_abc123"},
	})
	if err != nil {
		t.Fatalf("source_evidence call failed at the protocol level: %v", err)
	}
	if evResult.IsError {
		t.Fatalf("expected source_evidence success, got: %#v", evResult.Content)
	}
}

// TestServerRejectsRecordEpisodeAsUnknownTool proves record_episode is
// unreachable: since NewServer never registers it, calling it produces a
// protocol-level "tool not found" error, not a CallToolResult.
func TestServerRejectsRecordEpisodeAsUnknownTool(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	_, err := client.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "record_episode",
		Arguments: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected record_episode to be rejected as an unknown tool")
	}
}
