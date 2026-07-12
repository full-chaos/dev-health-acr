package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerListsRecordEpisodeWhenAllWritebackGatesPass(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	boot := newWritebackFixtureBootstrap(t, fx)
	boot.Capabilities.Permissions.EpisodeWrite = true

	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	// When
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	for _, tool := range tools.Tools {
		if tool.Name == toolRecordEpisode {
			return
		}
	}
	t.Fatalf("expected %q when every writeback gate is enabled", toolRecordEpisode)
}

func TestServerOmitsRecordEpisodeWhenAnyWritebackGateFails(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Bootstrap)
	}{
		{"local opt-in disabled", func(boot *Bootstrap) { boot.Config.EnableWriteback = false }},
		{"entitlement absent", func(boot *Bootstrap) { boot.Capabilities.Entitlements.AgentContextRuntime = false }},
		{"permission absent", func(boot *Bootstrap) { boot.Capabilities.Permissions.EpisodeWrite = false }},
		{"hosted tool absent", func(boot *Bootstrap) {
			boot.Capabilities.EnabledTools = []string{toolContextForTask, toolSourceEvidence}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			fx := newFixtureServer(t)
			boot := newFixtureBootstrap(t, fx)
			boot.Config.EnableWriteback = true
			test.mutate(boot)
			client, closeFn := connectedClient(t, boot)
			defer closeFn()

			// When
			tools, err := client.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}

			// Then
			for _, tool := range tools.Tools {
				if tool.Name == toolRecordEpisode {
					t.Fatalf("%q must be absent when %s", toolRecordEpisode, test.name)
				}
			}
		})
	}
}

func TestRecordEpisodeReturnsSafeReceiptForCreateAndDuplicateReplay(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	var calls int
	fx.EpisodeHandler = func(w http.ResponseWriter, r *http.Request) {
		calls++
		var created contractsv1.AgentEpisodeCreate
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Fatal(err)
		}
		recorded := validAgentEpisodeFixture(created, calls == 2)
		recorded.SchemaVersion = contractsv1.AgentEpisodeSchema
		writeJSONFixture(t, w, http.StatusCreated, recorded)
	}
	boot := newWritebackFixtureBootstrap(t, fx)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	// When
	first := callRecordEpisode(t, client, recordEpisodeArguments("summary-secret", "transcript-secret", "default_90d"))
	second := callRecordEpisode(t, client, recordEpisodeArguments("summary-secret", "transcript-secret", "default_90d"))

	// Then
	if first.Status != "recorded" || first.Duplicate == nil || *first.Duplicate || first.Scope.Branch != "main" || first.TranscriptDisposition != "redacted" || second.Status != "recorded" || second.Duplicate == nil || !*second.Duplicate || calls != 2 {
		t.Fatalf("unexpected create/replay receipts: first=%#v second=%#v calls=%d", first, second, calls)
	}
	if rendered := string(receiptJSON(t, first)) + string(receiptJSON(t, second)); strings.Contains(rendered, "summary-secret") || strings.Contains(rendered, "transcript-secret") {
		t.Fatal("record_episode receipt leaked submitted text")
	}
}

func TestRecordEpisodeReturnsNoPersistReceipt(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	fx.EpisodeHandler = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	boot := newWritebackFixtureBootstrap(t, fx)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	// When
	receipt := callRecordEpisode(t, client, recordEpisodeArguments("safe-summary", "safe-transcript", "no_persist"))

	// Then
	if receipt.Status != "no_persist" || receipt.EpisodeID != "" || receipt.CreatedAt != nil || receipt.RedactionState != "" || receipt.Scope.Branch != "main" || receipt.TranscriptDisposition != "accepted" {
		t.Fatalf("unexpected no_persist receipt: %#v", receipt)
	}
}

func TestRecordEpisodeRejectsInvalidInputWithoutCallingHostedAPI(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	calls := 0
	fx.EpisodeHandler = func(w http.ResponseWriter, _ *http.Request) { calls++ }
	boot := newWritebackFixtureBootstrap(t, fx)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()
	arguments := recordEpisodeArguments("safe-summary", "safe-transcript", "default_90d")
	arguments["idempotency_key"] = "short"

	// When
	result, err := client.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: toolRecordEpisode, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if !result.IsError || calls != 0 {
		t.Fatalf("invalid input result=%#v calls=%d", result, calls)
	}
}

func TestRecordEpisodeRejectsTranscriptWhenLocalCaptureIsDisabled(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	calls := 0
	fx.EpisodeHandler = func(w http.ResponseWriter, _ *http.Request) { calls++ }
	boot := newFixtureBootstrap(t, fx)
	boot.Config.EnableWriteback = true
	boot.Capabilities.Permissions.EpisodeWrite = true
	request := callToolRequest(t, recordEpisodeArguments("safe-summary", "transcript-secret", "default_90d"))

	// When
	result, err := handleRecordEpisode(context.Background(), boot, request)

	// Then
	if err != nil || !result.IsError || calls != 0 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, calls)
	}
}

func TestTranscriptDispositionDistinguishesSubmissionAndRedaction(t *testing.T) {
	tests := []struct {
		name         string
		inputMode    string
		responseMode string
		redaction    string
		want         string
	}{
		{"not submitted", "none", "none", "active", "not_submitted"},
		{"accepted", "opaque_ref", "opaque_ref", "active", "accepted"},
		{"redacted transcript", "opaque_ref", "redacted_summary", "active", "redacted"},
		{"redacted episode", "opaque_ref", "opaque_ref", "redacted", "redacted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			input := contractsv1.MCPRecordEpisodeRequest{Transcript: contractsv1.TranscriptRef{Mode: test.inputMode}}
			episode := &contractsv1.AgentEpisode{AgentEpisodeCreate: contractsv1.AgentEpisodeCreate{Transcript: contractsv1.TranscriptRef{Mode: test.responseMode}}, RedactionState: test.redaction}

			// When
			got := transcriptDisposition(input, episode)

			// Then
			if got != test.want {
				t.Fatalf("transcript disposition = %q, want %q", got, test.want)
			}
		})
	}
}

func recordEpisodeArguments(summary, transcript, retentionClass string) map[string]any {
	return map[string]any{
		"client_episode_id": "client_ep_01J0ACR001",
		"idempotency_key":   "idem_01J0ACR001",
		"context_packet_id": "packet_01J0ACR001",
		"goal":              "Record MCP writeback coverage",
		"repository":        map[string]any{"slug": "acme/widgets"},
		"scope":             map[string]any{"branch": "main"},
		"started_at":        time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"ended_at":          time.Date(2026, time.July, 11, 12, 1, 0, 0, time.UTC).Format(time.RFC3339),
		"outcome":           "succeeded",
		"summary":           summary,
		"artifacts":         map[string]any{"files_touched": []string{}, "artifact_uris": []string{}, "tests_run": []string{}},
		"transcript":        map[string]any{"mode": "redacted_summary", "redacted_summary": transcript},
		"retention_class":   retentionClass,
	}
}

func callRecordEpisode(t *testing.T, client *mcpsdk.ClientSession, arguments map[string]any) contractsv1.MCPRecordEpisodeResponse {
	t.Helper()
	result, err := client.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: toolRecordEpisode, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("record_episode returned tool error: %s", toolResultText(result))
	}
	if text := toolResultText(result); !strings.Contains(text, "append-only evidence") || !strings.Contains(text, "not durable memory or promoted truth") {
		t.Fatalf("record_episode success content is not safety truthful: %q", text)
	}
	var receipt contractsv1.MCPRecordEpisodeResponse
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(structured, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func toolResultText(result *mcpsdk.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	text, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		return "unexpected tool content"
	}
	return text.Text
}

func receiptJSON(t *testing.T, receipt contractsv1.MCPRecordEpisodeResponse) []byte {
	t.Helper()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
