package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// CHAOS-7073: the read_facts tool, driven through the real MCP server and the
// real sidecar client against a TLS fixture of the hosted route.

type readFactsHosted struct {
	mu      sync.Mutex
	calls   int
	body    string
	bearer  string
	status  int
	respond []byte
}

func newReadFactsBootstrap(t *testing.T, hosted *readFactsHosted, advertise bool) *Bootstrap {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/context-fabric/data/facts" {
			writeErrorFixture(t, w, http.StatusNotFound, "not_found", false)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		hosted.mu.Lock()
		hosted.calls++
		hosted.body = string(raw)
		hosted.bearer = r.Header.Get("Authorization")
		status, respond := hosted.status, hosted.respond
		hosted.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			writeErrorFixture(t, w, status, "invalid_request", false)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respond)
	}))
	t.Cleanup(server.Close)
	cfg := fixtureConfig(t, server)
	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xAB)))
	if err != nil {
		t.Fatal(err)
	}
	caps := validCapabilitiesFixture()
	if advertise {
		caps.EnabledTools = append(caps.EnabledTools, toolReadFacts)
	}
	return &Bootstrap{Config: cfg, Client: client, Capabilities: caps}
}

func readFactsExample(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "mcp_read_facts_response.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

const readFactsArgs = `{"kinds":["investment_distribution"],"subjects":[{"kind":"team","canonical_id":"team-payments"}],"window":{"mode":"trailing","days":30},"tables":"include","max_bytes":65536}`

func callReadFacts(t *testing.T, boot *Bootstrap, arguments string) *mcpsdk.CallToolResult {
	t.Helper()
	client, closeFn := connectedClient(t, boot)
	defer closeFn()
	result, err := client.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: toolReadFacts, Arguments: json.RawMessage(arguments)})
	if err != nil {
		// A protocol-level refusal (SDK input-schema validation) is also a
		// refusal to call the tool.
		return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: err.Error()}}}
	}
	return result
}

func TestReadFactsToolIsListedOnlyWhenAdvertisedAndIsReadOnly(t *testing.T) {
	hosted := &readFactsHosted{respond: readFactsExample(t)}
	for _, advertise := range []bool{true, false} {
		client, closeFn := connectedClient(t, newReadFactsBootstrap(t, hosted, advertise))
		listed, err := client.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var found *mcpsdk.Tool
		for _, tool := range listed.Tools {
			if tool.Name == toolReadFacts {
				found = tool
			}
		}
		closeFn()
		if !advertise {
			if found != nil {
				t.Fatal("read_facts offered without hosted support")
			}
			continue
		}
		if found == nil {
			t.Fatal("read_facts missing when hosted advertises it")
		}
		if found.Annotations == nil || !found.Annotations.ReadOnlyHint {
			t.Fatalf("read_facts must be read-only: %#v", found.Annotations)
		}
		for _, needle := range []string{"other tools", "no model", "attribution basis", "strings"} {
			if !strings.Contains(strings.ToLower(found.Description), needle) {
				t.Errorf("description does not say %q: %s", needle, found.Description)
			}
		}
	}
}

func TestReadFactsForwardsTheRequestAndReturnsTheHostedDocument(t *testing.T) {
	hosted := &readFactsHosted{respond: readFactsExample(t)}
	boot := newReadFactsBootstrap(t, hosted, true)
	result := callReadFacts(t, boot, readFactsArgs)
	if result.IsError {
		t.Fatalf("tool error: %s", toolResultText(result))
	}
	if hosted.calls != 1 {
		t.Fatalf("hosted calls = %d, want 1", hosted.calls)
	}
	if !strings.HasPrefix(hosted.bearer, "Bearer fcacr_") {
		t.Fatalf("the caller's own bearer must be forwarded, got %q", hosted.bearer)
	}
	var sent, want map[string]any
	if err := json.Unmarshal([]byte(hosted.body), &sent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(readFactsArgs), &want); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(sent, want) {
		t.Fatalf("request was rewritten:\nsent %s\nwant %s", hosted.body, readFactsArgs)
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got, expected map[string]any
	if err := json.Unmarshal(structured, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(hosted.respond, &expected); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(got, expected) {
		t.Fatalf("structured content differs from the hosted document:\n%s", structured)
	}
	var text map[string]any
	if err := json.Unmarshal([]byte(toolResultText(result)), &text); err != nil || !jsonEqual(text, expected) {
		t.Fatalf("text content must carry the same document: %v", err)
	}
	assertMatchesToolSchema(t, json.RawMessage(structured), readFactsResponseSchemaFile)
}

func TestReadFactsRefusesInvalidInputWithoutCallingHosted(t *testing.T) {
	cases := map[string]string{
		"no kinds":            `{"kinds":[],"subjects":[{"kind":"team","canonical_id":"t"}]}`,
		"no subjects":         `{"kinds":["health"],"subjects":[]}`,
		"nine kinds":          `{"kinds":["a","b","c","d","e","f","g","h","i"],"subjects":[{"kind":"team","canonical_id":"t"}]}`,
		"unknown field":       `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"t"}],"model":"x"}`,
		"max_bytes too small": `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"t"}],"max_bytes":100}`,
		"max_bytes too large": `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"t"}],"max_bytes":262145}`,
		"bad tables":          `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"t"}],"tables":"all"}`,
		"bad window mode":     `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"t"}],"window":{"mode":"sometime"}}`,
		"window days too big": `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"t"}],"window":{"mode":"trailing","days":61}}`,
		"subject no id":       `{"kinds":["health"],"subjects":[{"kind":"team"}]}`,
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			hosted := &readFactsHosted{respond: readFactsExample(t)}
			result := callReadFacts(t, newReadFactsBootstrap(t, hosted, true), arguments)
			if !result.IsError {
				t.Fatalf("invalid input was accepted: %s", toolResultText(result))
			}
			if hosted.calls != 0 {
				t.Fatalf("hosted was called %d times for an invalid request", hosted.calls)
			}
		})
	}
}

func TestReadFactsMapsAHostedRefusalToASafeToolError(t *testing.T) {
	hosted := &readFactsHosted{respond: readFactsExample(t), status: http.StatusBadRequest}
	result := callReadFacts(t, newReadFactsBootstrap(t, hosted, true), readFactsArgs)
	if !result.IsError {
		t.Fatal("a hosted 400 must be a tool error")
	}
	if strings.Contains(toolResultText(result), "fixture invalid_request") {
		t.Fatalf("raw hosted text leaked: %s", toolResultText(result))
	}
}

func TestReadFactsRefusesADocumentThatIsNotReadFacts(t *testing.T) {
	hosted := &readFactsHosted{respond: []byte(`{"tool":"something_else","contract_version":"acr-data.v1"}`)}
	result := callReadFacts(t, newReadFactsBootstrap(t, hosted, true), readFactsArgs)
	if !result.IsError {
		t.Fatal("a foreign document must not be relayed as read_facts output")
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
