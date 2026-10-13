package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func dataFirstCaller(t *testing.T, withInvestigateQuestion bool) (*ProcessConfig, *CallerContext) {
	t.Helper()
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools,
		toolDataCatalog, toolFindSubjects, toolReadFacts, toolReadRelationships, toolRunOperation, toolGraphQLQuery,
		toolInvestigateWithInterpretation, toolInvestigationResult)
	if withInvestigateQuestion {
		caps.EnabledTools = append(caps.EnabledTools, toolInvestigateQuestion)
	}
	return (&Bootstrap{Capabilities: caps}).split(nil)
}

func TestServedToolsListNamesTheDataToolsBeforeInvestigateQuestion(t *testing.T) {
	cfg, caller := dataFirstCaller(t, true)
	session := connectedClientForCaller(t, cfg, caller)
	names := listedToolNames(t, session)
	last := slices.Index(names, toolInvestigateQuestion)
	require.GreaterOrEqual(t, last, 0)
	for _, data := range []string{toolDataCatalog, toolFindSubjects, toolReadFacts, toolReadRelationships, toolRunOperation, toolGraphQLQuery} {
		at := slices.Index(names, data)
		require.GreaterOrEqual(t, at, 0, data)
		require.Less(t, at, slices.Index(names, toolInvestigateWithInterpretation), data)
		require.Less(t, at, last, data)
	}
	require.Less(t, slices.Index(names, toolInvestigateWithInterpretation), last)
	require.Equal(t, len(names)-1, last, "investigate_question is the last listed tool: %v", names)
}

func TestServedInstructionsLeadWithTheDataToolsAndPutInvestigateQuestionLast(t *testing.T) {
	cfg, caller := dataFirstCaller(t, true)
	session := connectedClientForCaller(t, cfg, caller)
	text := session.InitializeResult().Instructions

	lead := strings.Index(text, "If you are a model: answer from stored facts first with ")
	ways := strings.Index(text, "Two ways to use this server.")
	choosing := strings.Index(text, "Choosing a tool:")
	shapes := strings.Index(text, "Question shapes that work (investigate tools only):")
	require.True(t, 0 <= lead && lead < ways && ways < choosing && choosing < shapes, "order lead=%d ways=%d choosing=%d shapes=%d", lead, ways, choosing, shapes)

	leadLine := text[lead : lead+strings.Index(text[lead:], "\n")]
	for _, name := range []string{"data_catalog", "find_subjects", "read_facts", "read_relationships", "run_operation"} {
		require.Contains(t, leadLine, name)
	}
	require.Contains(t, leadLine, "call no model and spend no model budget")
	require.Contains(t, text, "investigate_question spends the service's or your organization's model budget: use it only if you have no model of your own.")

	guide := text[choosing:shapes]
	require.Less(t, strings.Index(guide, "- read_facts:"), strings.Index(guide, "- investigate_with_interpretation:"))
	require.Less(t, strings.Index(guide, "- investigate_with_interpretation:"), strings.Index(guide, "- investigate_question "))
	require.Less(t, strings.Index(guide, "- source_evidence:"), strings.Index(guide, "- investigate_question "))
}

func TestServedDataToolDescriptionsSayTheyCallNoModel(t *testing.T) {
	cfg, caller := dataFirstCaller(t, true)
	session := connectedClientForCaller(t, cfg, caller)
	listed, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	seen := map[string]string{}
	for _, tool := range listed.Tools {
		seen[tool.Name] = tool.Description
	}
	for _, data := range []string{toolDataCatalog, toolFindSubjects, toolReadFacts, toolReadRelationships, toolRunOperation, toolGraphQLQuery} {
		require.True(t, strings.HasPrefix(seen[data], "Calls no model and spends no model budget. "), data)
	}
	require.Contains(t, seen[toolInvestigateQuestion], "spends the service's or your organization's model budget")
}

func TestServedInstructionsForACallerWithoutInvestigateQuestionNameItNowhere(t *testing.T) {
	cfg, caller := dataFirstCaller(t, false)
	session := connectedClientForCaller(t, cfg, caller)
	text := session.InitializeResult().Instructions
	require.NotContains(t, text, "investigate_question")
	require.Contains(t, text, "call no model and spend no model budget")
	require.NotContains(t, listedToolNames(t, session), toolInvestigateQuestion)
}
