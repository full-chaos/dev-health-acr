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

// CHAOS-7074: the read_relationships tool, driven through the real MCP server and the
// real sidecar client against a TLS fixture of the hosted route.

type readRelationshipsHosted struct {
	mu      sync.Mutex
	calls   int
	body    string
	bearer  string
	status  int
	respond []byte
}

func newReadRelationshipsBootstrap(t *testing.T, hosted *readRelationshipsHosted, advertise bool) *Bootstrap {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/context-fabric/data/relationships" {
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
		caps.EnabledTools = append(caps.EnabledTools, toolReadRelationships)
	}
	return &Bootstrap{Config: cfg, Client: client, Capabilities: caps}
}

func readRelationshipsExample(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "mcp_read_relationships_response.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

const readRelationshipsArgs = `{"subject":{"kind":"repository","canonical_id":"repo-payments-api"},"types":["BELONGS_TO_PROJECT","OWNED_BY_TEAM"],"direction":"both","depth":1,"limit":50}`

func callReadRelationships(t *testing.T, boot *Bootstrap, arguments string) *mcpsdk.CallToolResult {
	t.Helper()
	client, closeFn := connectedClient(t, boot)
	defer closeFn()
	result, err := client.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: toolReadRelationships, Arguments: json.RawMessage(arguments)})
	if err != nil {
		// A protocol-level refusal (SDK input-schema validation) is also a
		// refusal to call the tool.
		return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: err.Error()}}}
	}
	return result
}

func TestReadRelationshipsToolIsListedOnlyWhenAdvertisedAndIsReadOnly(t *testing.T) {
	hosted := &readRelationshipsHosted{respond: readRelationshipsExample(t)}
	for _, advertise := range []bool{true, false} {
		client, closeFn := connectedClient(t, newReadRelationshipsBootstrap(t, hosted, advertise))
		listed, err := client.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var found *mcpsdk.Tool
		for _, tool := range listed.Tools {
			if tool.Name == toolReadRelationships {
				found = tool
			}
		}
		closeFn()
		if !advertise {
			if found != nil {
				t.Fatal("read_relationships offered without hosted support")
			}
			continue
		}
		if found == nil {
			t.Fatal("read_relationships missing when hosted advertises it")
		}
		if found.Annotations == nil || !found.Annotations.ReadOnlyHint {
			t.Fatalf("read_relationships must be read-only: %#v", found.Annotations)
		}
		for _, needle := range []string{"find_subjects", "model-free", "next_cursor", "edges_not_visible", "an edge shows a relation. it does not show a cause."} {
			if !strings.Contains(strings.ToLower(found.Description), needle) {
				t.Errorf("description does not say %q: %s", needle, found.Description)
			}
		}
	}
}

func TestReadRelationshipsForwardsTheRequestAndReturnsTheHostedDocument(t *testing.T) {
	hosted := &readRelationshipsHosted{respond: readRelationshipsExample(t)}
	boot := newReadRelationshipsBootstrap(t, hosted, true)
	result := callReadRelationships(t, boot, readRelationshipsArgs)
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
	if err := json.Unmarshal([]byte(readRelationshipsArgs), &want); err != nil {
		t.Fatal(err)
	}
	if !relationshipsJSONEqual(sent, want) {
		t.Fatalf("request was rewritten:\nsent %s\nwant %s", hosted.body, readRelationshipsArgs)
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
	if !relationshipsJSONEqual(got, expected) {
		t.Fatalf("structured content differs from the hosted document:\n%s", structured)
	}
	var text map[string]any
	if err := json.Unmarshal([]byte(toolResultText(result)), &text); err != nil || !relationshipsJSONEqual(text, expected) {
		t.Fatalf("text content must carry the same document: %v", err)
	}
	assertMatchesToolSchema(t, json.RawMessage(structured), readRelationshipsResponseSchemaFile)
}

func TestReadRelationshipsRefusesInvalidInputWithoutCallingHosted(t *testing.T) {
	const subject = `"subject":{"kind":"repository","canonical_id":"r"}`
	cases := map[string]string{
		"no subject":        `{"depth":1}`,
		"subject no id":     `{"subject":{"kind":"repository"}}`,
		"unknown field":     `{` + subject + `,"model":"x"}`,
		"unknown type":      `{` + subject + `,"types":["CAUSES"]}`,
		"bad direction":     `{` + subject + `,"direction":"sideways"}`,
		"depth three":       `{` + subject + `,"depth":3}`,
		"limit too large":   `{` + subject + `,"limit":101}`,
		"as_of not instant": `{` + subject + `,"as_of":"yesterday"}`,
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			hosted := &readRelationshipsHosted{respond: readRelationshipsExample(t)}
			result := callReadRelationships(t, newReadRelationshipsBootstrap(t, hosted, true), arguments)
			if !result.IsError {
				t.Fatalf("invalid input was accepted: %s", toolResultText(result))
			}
			if hosted.calls != 0 {
				t.Fatalf("hosted was called %d times for an invalid request", hosted.calls)
			}
		})
	}
}

func TestReadRelationshipsMapsAHostedRefusalToASafeToolError(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			hosted := &readRelationshipsHosted{respond: readRelationshipsExample(t), status: status}
			result := callReadRelationships(t, newReadRelationshipsBootstrap(t, hosted, true), readRelationshipsArgs)
			if !result.IsError {
				t.Fatalf("a hosted %d must be a tool error", status)
			}
			if hosted.calls != 1 {
				t.Fatalf("hosted calls = %d, want 1", hosted.calls)
			}
			if strings.Contains(toolResultText(result), "fixture invalid_request") {
				t.Fatalf("raw hosted text leaked: %s", toolResultText(result))
			}
		})
	}
}

func TestReadRelationshipsRefusesADocumentThatIsNotAcrData(t *testing.T) {
	for name, body := range map[string]string{
		"no status":     `{"tool":"read_facts","contract_version":"acr-data.v1"}`,
		"other version": `{"contract_version":"acr-data.v2","status":"complete"}`,
	} {
		t.Run(name, func(t *testing.T) {
			hosted := &readRelationshipsHosted{respond: []byte(body)}
			result := callReadRelationships(t, newReadRelationshipsBootstrap(t, hosted, true), readRelationshipsArgs)
			if !result.IsError {
				t.Fatal("a foreign document must not be relayed as read_relationships output")
			}
		})
	}
}

func relationshipsJSONEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// r1 P3: the tool refuses a repeated type before calling the hosted API.
func TestReadRelationshipsRefusesARepeatedType(t *testing.T) {
	input := readRelationshipsInput{Subject: readRelationshipsSubjectInput{Kind: "repository", CanonicalID: "repository:a"}, Types: []string{"BLOCKS", "BLOCKS"}}
	if err := input.validate(); err == nil {
		t.Fatal("a repeated type was accepted")
	}
	input.Types = []string{"BLOCKS", "PART_OF"}
	if err := input.validate(); err != nil {
		t.Fatalf("distinct types refused: %v", err)
	}
}
