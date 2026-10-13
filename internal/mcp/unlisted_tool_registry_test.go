package mcp

import (
	"context"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEveryManifestToolNotDisabledByDefaultIsRegisteredForAFullCaller(t *testing.T) {
	caps := validCapabilitiesFixture()
	for _, name := range manifestToolNames() {
		if !contains(caps.EnabledTools, name) {
			caps.EnabledTools = append(caps.EnabledTools, name)
		}
	}
	cfg, caller := (&Bootstrap{Capabilities: caps}).split(io.Discard)
	session := connectedClientForCaller(t, cfg, caller)
	listed := listedToolNames(t, session)
	for _, name := range manifestToolNames() {
		if entry, _ := lookupManifestEntry(name); entry.DisabledByDefault {
			continue
		}
		require.Contains(t, listed, name, "a tool in the manifest is not registered by the server; the unlisted-call path would misjudge it")
	}
}

func contains(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}
func TestUnlistedDecisionEqualsTheServersRealToolsListForEveryCaller(t *testing.T) {
	snapshots := [][]string{
		nil,
		{toolDataCatalog, toolFindSubjects, toolReadFacts},
		{toolInvestigateWithInterpretation, toolInvestigationResult, toolReadRelationships},
		{toolInvestigateQuestion, toolRunOperation, toolGraphQLQuery, toolReadFacts},
	}
	for _, extra := range snapshots {
		caps := validCapabilitiesFixture()
		caps.EnabledTools = append(caps.EnabledTools, extra...)
		cfg, caller := (&Bootstrap{Capabilities: caps}).split(io.Discard)
		session := connectedClientForCaller(t, cfg, caller)
		listed := listedToolNames(t, session)
		for _, name := range manifestToolNames() {
			entry, _ := lookupManifestEntry(name)
			if entry.DisabledByDefault {
				continue
			}
			require.Equal(t, contains(listed, name), hostedToolEnabled(caller, name), "tool %s with extra %v: the middleware's listed decision differs from tools/list", name, extra)
		}
	}
}

func TestStdioServerKeepsTheProtocolErrorForAnUnlistedTool(t *testing.T) {
	cfg, caller := serviceModelCaller(t, false)
	session := connectedClientForCaller(t, cfg, caller)
	_, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: toolInvestigateQuestion, Arguments: map[string]any{"question": "q"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}
