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

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	message string
	details map[string]any
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
		status, respond, message, details := hosted.status, hosted.respond, hosted.message, hosted.details
		hosted.mu.Unlock()
		if status != 0 && status != http.StatusOK && message != "" {
			writeJSONFixture(t, w, status, contractsv1.ErrorEnvelope{
				SchemaVersion: contractsv1.ErrorSchema,
				RequestID:     "req_fixture",
				Error: contractsv1.ErrorDetail{
					Code: "invalid_request", Message: message, HTTPStatus: status,
					Details: details,
				},
			})
			return
		}
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
		"window days too big": `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"t"}],"window":{"mode":"trailing","days":366}}`,
		"subject no id":       `{"kinds":["health"],"subjects":[{"kind":"team"}]}`,
		"units too large":     `{"kinds":["investment"],"subjects":[{"kind":"team","canonical_id":"t"}],"units":{"max_units":151}}`,
		"units negative":      `{"kinds":["investment"],"subjects":[{"kind":"team","canonical_id":"t"}],"units":{"max_units":-1}}`,
		"units cursor huge":   `{"kinds":["investment"],"subjects":[{"kind":"team","canonical_id":"t"}],"units":{"cursor":"` + strings.Repeat("a", 1025) + `"}}`,
		"units unknown field": `{"kinds":["investment"],"subjects":[{"kind":"team","canonical_id":"t"}],"units":{"page":2}}`,
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

func TestReadFactsForwardsTheUnitsPageRequestUnchanged(t *testing.T) {
	arguments := `{"kinds":["investment"],"subjects":[{"kind":"team","canonical_id":"team:t"}],"units":{"cursor":"abc","max_units":150}}`
	hosted := &readFactsHosted{respond: readFactsExample(t)}
	result := callReadFacts(t, newReadFactsBootstrap(t, hosted, true), arguments)
	if result.IsError {
		t.Fatalf("tool error: %s", toolResultText(result))
	}
	var sent, want map[string]any
	if err := json.Unmarshal([]byte(hosted.body), &sent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(arguments), &want); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(sent, want) {
		t.Fatalf("units request was rewritten:\nsent %s\nwant %s", hosted.body, arguments)
	}
}

func TestReadFactsCarriesTheWindowRefusalAdviceToTheClient(t *testing.T) {
	const advice = "read a longer period as several windows"
	hosted := &readFactsHosted{
		respond: readFactsExample(t),
		status:  http.StatusBadRequest,
		message: "The read_facts request is invalid: a window spans at most 60 days for the kinds asked; " + advice,
		details: map[string]any{"reason": "window_beyond_kind_max", "max_days": 60},
	}
	args := `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"team-payments"}],"window":{"mode":"range","start":"2026-01-01T00:00:00Z","end":"2026-04-01T00:00:00Z"}}`
	result := callReadFacts(t, newReadFactsBootstrap(t, hosted, true), args)
	if !result.IsError {
		t.Fatal("a hosted window refusal must be a tool error")
	}
	text := toolResultText(result)
	if !strings.Contains(text, "at most 60 days") || !strings.Contains(text, advice) {
		t.Fatalf("the refusal advice did not reach the client: %s", text)
	}
}

func TestReadFactsRefusesAWindowBeyondEveryKindWithAdviceLocally(t *testing.T) {
	hosted := &readFactsHosted{respond: readFactsExample(t)}
	args := `{"kinds":["investment"],"subjects":[{"kind":"team","canonical_id":"team-payments"}],"window":{"mode":"trailing","days":366}}`
	result := callReadFacts(t, newReadFactsBootstrap(t, hosted, true), args)
	text := toolResultText(result)
	if !result.IsError || !strings.Contains(text, "do not add shorter windows") || !strings.Contains(text, "at most 365 days") {
		t.Fatalf("366 d refusal lacks the advice: %v %s", result.IsError, text)
	}
	if hosted.calls != 0 {
		t.Fatal("a locally refused window must not call hosted")
	}
}
