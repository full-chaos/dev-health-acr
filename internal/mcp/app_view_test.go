package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Literal names from the MCP Apps extension (ext-apps specification
// 2026-01-26), kept as literals so the cells compile against any server.
const (
	appViewTestExtension = "io.modelcontextprotocol/ui"
	appViewTestMIME      = "text/html;profile=mcp-app"
	appViewTestURI       = "ui://dev-health/investigate-answer"
	appViewTestHTMLFile  = "appview/answer.html"
)

func appViewClientCaps(mimeTypes ...string) *mcpsdk.ClientCapabilities {
	caps := &mcpsdk.ClientCapabilities{}
	caps.AddExtension(appViewTestExtension, map[string]any{"mimeTypes": mimeTypes})
	return caps
}

func appViewSession(t *testing.T, boot *Bootstrap, caps *mcpsdk.ClientCapabilities) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := NewServer(boot, "test-version")
	var opts *mcpsdk.ClientOptions
	if caps != nil {
		opts = &mcpsdk.ClientOptions{Capabilities: caps}
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, opts)
	t1, t2 := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	return clientSession
}

func appViewListedTools(t *testing.T, session *mcpsdk.ClientSession, params *mcpsdk.ListToolsParams) map[string]*mcpsdk.Tool {
	t.Helper()
	listed, err := session.ListTools(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*mcpsdk.Tool{}
	for _, tool := range listed.Tools {
		out[tool.Name] = tool
	}
	if out[toolInvestigateQuestion] == nil {
		t.Fatalf("investigate_question not listed: %v", out)
	}
	return out
}

func appViewMetaURI(tool *mcpsdk.Tool) (nested, legacy string) {
	if tool.Meta == nil {
		return "", ""
	}
	if ui, ok := tool.Meta["ui"].(map[string]any); ok {
		nested, _ = ui["resourceUri"].(string)
	}
	legacy, _ = tool.Meta["ui/resourceUri"].(string)
	return nested, legacy
}

func assertAppViewMetaOnlyOnInvestigate(t *testing.T, label string, tools map[string]*mcpsdk.Tool) {
	t.Helper()
	for name, tool := range tools {
		nested, legacy := appViewMetaURI(tool)
		if name == toolInvestigateQuestion {
			if nested != appViewTestURI || legacy != appViewTestURI {
				t.Errorf("%s: investigate_question _meta ui = %q / %q, want %q", label, nested, legacy, appViewTestURI)
			}
			continue
		}
		if nested != "" || legacy != "" {
			t.Errorf("%s: tool %s carries a ui resource %q / %q", label, name, nested, legacy)
		}
	}
}

func TestAppViewToolsListMetaOnlyInvestigate(t *testing.T) {
	boot := answerFixtureBootstrap(t, parityResult(), nil)

	session := appViewSession(t, boot, appViewClientCaps(appViewTestMIME))
	assertAppViewMetaOnlyOnInvestigate(t, "initialize capabilities", appViewListedTools(t, session, nil))

	plain := appViewSession(t, boot, nil)
	params := &mcpsdk.ListToolsParams{}
	params.SetMeta(map[string]any{mcpsdk.MetaKeyClientCapabilities: map[string]any{
		"extensions": map[string]any{appViewTestExtension: map[string]any{"mimeTypes": []string{appViewTestMIME}}},
	}})
	assertAppViewMetaOnlyOnInvestigate(t, "request capabilities", appViewListedTools(t, plain, params))
}

func TestAppViewNoMetaWithoutAppsMIME(t *testing.T) {
	boot := answerFixtureBootstrap(t, parityResult(), nil)
	for label, caps := range map[string]*mcpsdk.ClientCapabilities{
		"no extension":          nil,
		"extension, other mime": appViewClientCaps("text/html"),
		"extension, no mime":    appViewClientCaps(),
	} {
		session := appViewSession(t, boot, caps)
		for name, tool := range appViewListedTools(t, session, nil) {
			if name == toolInvestigateQuestion && tool.Meta != nil {
				t.Errorf("%s: investigate_question carries _meta %v", label, tool.Meta)
			}
			if nested, legacy := appViewMetaURI(tool); nested != "" || legacy != "" {
				t.Errorf("%s: tool %s carries a ui resource", label, name)
			}
		}
		resources, err := session.ListResources(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, res := range resources.Resources {
			if strings.HasPrefix(res.URI, "ui://") {
				t.Errorf("%s: resources/list offers %s", label, res.URI)
			}
		}
	}
}

func TestAppViewResourceReadMIME(t *testing.T) {
	want, err := os.ReadFile(appViewTestHTMLFile)
	if err != nil {
		t.Fatalf("read %s: %v", appViewTestHTMLFile, err)
	}
	boot := answerFixtureBootstrap(t, parityResult(), nil)
	session := appViewSession(t, boot, appViewClientCaps(appViewTestMIME))

	resources, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, res := range resources.Resources {
		if res.URI == appViewTestURI {
			listed = true
			if res.MIMEType != appViewTestMIME {
				t.Errorf("listed MIME %q, want %q", res.MIMEType, appViewTestMIME)
			}
		}
	}
	if !listed {
		t.Errorf("resources/list does not offer %s", appViewTestURI)
	}

	read, err := session.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: appViewTestURI})
	if err != nil {
		t.Fatalf("read %s: %v", appViewTestURI, err)
	}
	if len(read.Contents) != 1 {
		t.Fatalf("read returned %d contents", len(read.Contents))
	}
	got := read.Contents[0]
	if got.URI != appViewTestURI || got.MIMEType != appViewTestMIME || got.Text != string(want) {
		t.Errorf("read contents: uri %q mime %q, text equal %v", got.URI, got.MIMEType, got.Text == string(want))
	}

	noAnswers := appViewSession(t, newFixtureBootstrap(t, newFixtureServer(t)), appViewClientCaps(appViewTestMIME))
	if _, err := noAnswers.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: appViewTestURI}); err == nil {
		t.Error("the view must not be served when investigate_question is not advertised")
	}
}

func TestAppViewFallbackGoldenUnchanged(t *testing.T) {
	boot := answerFixtureBootstrap(t, parityResult(), nil)
	input, err := json.Marshal(contractsv1.MCPInvestigateQuestionRequest{Question: "Which teams need attention on Ask Dev, and why?"})
	if err != nil {
		t.Fatal(err)
	}
	encode := func(result *mcpsdk.CallToolResult) []byte {
		t.Helper()
		if result.IsError {
			t.Fatalf("tool error: %s", toolResultText(result))
		}
		encoded, err := json.Marshal(map[string]any{"content": result.Content, "structuredContent": result.StructuredContent})
		if err != nil {
			t.Fatal(err)
		}
		var canonical any
		if err := json.Unmarshal(encoded, &canonical); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(canonical)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	golden, err := invokeInvestigateQuestion(context.Background(), boot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: input}})
	if err != nil {
		t.Fatal(err)
	}
	want := encode(golden)
	for label, caps := range map[string]*mcpsdk.ClientCapabilities{
		"plain client": nil,
		"apps client":  appViewClientCaps(appViewTestMIME),
	} {
		session := appViewSession(t, boot, caps)
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: toolInvestigateQuestion, Arguments: json.RawMessage(input)})
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got := encode(result); !bytes.Equal(got, want) {
			t.Errorf("%s: result differs from the handler result (%d vs %d bytes)", label, len(got), len(want))
		}
		if result.Meta != nil {
			t.Errorf("%s: result carries _meta %v", label, result.Meta)
		}
	}
}

func TestAppViewHTMLStaticSafety(t *testing.T) {
	raw, err := os.ReadFile(appViewTestHTMLFile)
	if err != nil {
		t.Fatalf("read %s: %v", appViewTestHTMLFile, err)
	}
	html := string(raw)
	if !strings.HasPrefix(strings.ToLower(html), "<!doctype html>") {
		t.Error("the view is not an HTML5 document")
	}
	for _, banned := range []string{
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function",
		"callServerTool", "tools/call", "fetch(", "XMLHttpRequest", "WebSocket", "EventSource", "sendBeacon", "import(",
		"data-px-scale", "mock-src", "review-bar", "STRUCTURED",
	} {
		if strings.Contains(html, banned) {
			t.Errorf("the view contains %q", banned)
		}
	}
	if regexp.MustCompile(`(?i)<script[^>]*\bsrc\s*=`).MatchString(html) {
		t.Error("the view loads a script by src")
	}
	if regexp.MustCompile(`(?i)<link\b`).MatchString(html) {
		t.Error("the view links an external resource")
	}
	for _, url := range regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.\-]+[^\s"'<>)]*`).FindAllString(html, -1) {
		if url != "http://www.w3.org/2000/svg" {
			t.Errorf("the view names a URL other than the SVG namespace: %q", url)
		}
	}
}
