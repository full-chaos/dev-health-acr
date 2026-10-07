package mcp

import (
	"context"
	"slices"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every tool the server registers must be a member of the request line's
// closed tool vocabulary; a tool outside it is logged as "other".
func TestToolsListedOverTheWireAreInTheRequestLineVocabulary(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newWritebackFixtureBootstrap(t, fx)
	boot.Capabilities.EnabledTools = append(boot.Capabilities.EnabledTools,
		toolInvestigateQuestion, toolInvestigateWithInterpretation, toolInvestigationResult,
		toolReadFacts, toolReadRelationships,
		toolDataCatalog, toolFindSubjects, toolRunOperation, toolGraphQLQuery)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()

	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	vocabulary := HTTPToolVocabulary()
	for _, want := range []string{toolDataCatalog, toolFindSubjects, toolRunOperation, toolGraphQLQuery} {
		if !slices.ContainsFunc(listed.Tools, func(tool *mcpsdk.Tool) bool { return tool.Name == want }) {
			t.Fatalf("tool %q not registered with every tool enabled", want)
		}
	}
	for _, tool := range listed.Tools {
		if got := bucket(tool.Name, vocabulary); got != tool.Name {
			t.Errorf("registered tool %q is logged as %q: not in eventspec.MCPHTTPToolVocabulary", tool.Name, got)
		}
	}
}
