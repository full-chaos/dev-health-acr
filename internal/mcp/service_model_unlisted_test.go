package mcp

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func serviceModelCaller(t *testing.T, withInvestigateQuestion bool) (*ProcessConfig, *CallerContext) {
	t.Helper()
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, toolInvestigateWithInterpretation, toolInvestigationResult, toolDataCatalog, toolFindSubjects, toolReadFacts)
	if withInvestigateQuestion {
		caps.EnabledTools = append(caps.EnabledTools, toolInvestigateQuestion)
	}
	return (&Bootstrap{Capabilities: caps}).split(io.Discard)
}

func TestCallerWithoutTheServiceModelNeverSeesInvestigateQuestion(t *testing.T) {
	cfg, caller := serviceModelCaller(t, false)
	session := connectedClientForCaller(t, cfg, caller)

	tools := listedToolNames(t, session)
	require.NotContains(t, tools, toolInvestigateQuestion)
	require.Contains(t, tools, toolInvestigateWithInterpretation)
	require.Contains(t, tools, toolInvestigationResult)

	require.Contains(t, promptNames(t, session), promptInterpretQuestion)
	require.Contains(t, promptNames(t, session), promptSynthesizeAnswer)
	uris := resourceURIs(t, session)
	require.Contains(t, uris, uriInterpretationOutput)
	require.Contains(t, uris, uriFactKinds)
	require.Contains(t, uris, uriSynthesisOutput)

	instructions := serverInstructions(cfg, caller)
	require.NotContains(t, instructions, "investigate_question")
	require.NotContains(t, instructions, "service_model_not_entitled")
	require.Contains(t, instructions, "use investigate_with_interpretation with synthesis \"client\".")
	require.Contains(t, instructions, "- investigate_with_interpretation:")
	require.Contains(t, instructions, "Question shapes that work:")
}

func TestCallerWithTheServiceModelStillSeesInvestigateQuestion(t *testing.T) {
	cfg, caller := serviceModelCaller(t, true)
	session := connectedClientForCaller(t, cfg, caller)
	require.Contains(t, listedToolNames(t, session), toolInvestigateQuestion)
	instructions := serverInstructions(cfg, caller)
	require.True(t, strings.Contains(instructions, "- investigate_question (callers with no model of their own)"))
	require.Contains(t, instructions, "instead of investigate_question.")
	require.Contains(t, instructions, "B. You want our engine's narrative answer")
}
