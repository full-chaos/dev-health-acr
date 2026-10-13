package mcp_test

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func callText(t *testing.T, session *mcpsdk.ClientSession, name string) (*mcpsdk.CallToolResult, error, string) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: map[string]any{"question": "which teams need attention?"}})
	var text string
	if result != nil {
		for _, c := range result.Content {
			if tc, ok := c.(*mcpsdk.TextContent); ok {
				text += tc.Text
			}
		}
	}
	return result, err, text
}

func TestHTTPCallToAKnownButUnlistedToolIsAToolErrorThatNamesWhatIsListed(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	plain := hosted.issue(readScopes, []string{repoPlain}, nil)
	session := connectClient(t, e, &headerTransport{bearer: plain.token})

	result, err, text := callText(t, session, "investigate_question")
	if err != nil {
		t.Fatalf("a protocol error reached the client: %v", err)
	}
	if !result.IsError {
		t.Fatalf("not an error result: %q", text)
	}
	if !strings.Contains(text, "investigate_question") || !strings.Contains(text, "not listed for this credential") {
		t.Fatalf("text does not name the tool and the checked reason: %q", text)
	}
	if strings.Contains(text, "not enabled for this organization") || strings.Contains(text, "Listed for you") {
		t.Fatalf("text claims a reason or tools that were not checked: %q", text)
	}

	_, err = session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "no_such_tool"})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("a name the manifest does not declare must keep the protocol error: %v", err)
	}
}

func TestHTTPCallToAnUnlistedAnswerToolNamesTheListedAnswerTools(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	answers := hosted.issue(readScopes, []string{repoAnswers}, nil)
	session := connectClient(t, e, &headerTransport{bearer: answers.token})

	result, err, text := callText(t, session, "read_facts")
	if err != nil || !result.IsError {
		t.Fatalf("want a tool error, got err=%v result=%v", err, result)
	}
	if !strings.Contains(text, "Listed for you: investigate_with_interpretation") || strings.Contains(text, "data_catalog") {
		t.Fatalf("text must list exactly the tools in this caller's snapshot: %q", text)
	}
}

func TestHTTPHostedServerPromisesNoListChanged(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	plain := hosted.issue(readScopes, []string{repoPlain}, nil)
	session := connectClient(t, e, &headerTransport{bearer: plain.token})

	caps := session.InitializeResult().Capabilities
	if caps.Tools == nil || caps.Tools.ListChanged {
		t.Fatalf("tools.listChanged is promised: %+v", caps.Tools)
	}
	if caps.Prompts != nil && caps.Prompts.ListChanged {
		t.Fatalf("prompts.listChanged is promised: %+v", caps.Prompts)
	}
	if caps.Resources != nil && caps.Resources.ListChanged {
		t.Fatalf("resources.listChanged is promised: %+v", caps.Resources)
	}
}

func TestHTTPCallToAToolDisabledAtServerLevelKeepsTheUnknownToolError(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	plain := hosted.issue(readScopes, []string{repoPlain}, nil)
	session := connectClient(t, e, &headerTransport{bearer: plain.token})

	result, err, text := callText(t, session, "record_episode")
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("a server-level-disabled tool must stay an unknown tool: err=%v result=%v text=%q", err, result, text)
	}
}
