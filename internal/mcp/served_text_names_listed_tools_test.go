package mcp

import (
	"context"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
	"github.com/stretchr/testify/require"
)

var optionalToolNames = []string{
	toolInvestigateQuestion, toolInvestigateWithInterpretation, toolInvestigationResult,
	toolDataCatalog, toolFindSubjects, toolReadFacts, toolReadRelationships, toolRunOperation, toolGraphQLQuery,
}

var toolQualifiers = []string{
	"if this server offers", "if listed", "when listed", "when tools/list shows", "that tools/list shows",
	"the server lists", "this server offers", "may be unavailable", "optional", "only when the hosted API enables",
	"the ones this server offers", "if the server lists", "when it is listed", "appears in tools/list", "tools/list offers",
}

var sentenceSplit = regexp.MustCompile(`\n|\. `)

func unqualifiedUnlistedMentions(text string, listed []string) []string {
	var bad []string
	for _, sentence := range sentenceSplit.Split(text, -1) {
		lower := strings.ReplaceAll(strings.ToLower(sentence), "`", "")
		plain := strings.ReplaceAll(sentence, "`", "")
		for _, name := range optionalToolNames {
			if slices.Contains(listed, name) {
				continue
			}
			from := 0
			for {
				at := strings.Index(plain[from:], name)
				if at < 0 {
					break
				}
				at += from
				from = at + len(name)
				lo, hi := max(0, at-toolQualifierReach), min(len(lower), at+len(name)+toolQualifierReach)
				window := lower[lo:hi]
				qualified := false
				for _, q := range toolQualifiers {
					if strings.Contains(window, q) {
						qualified = true
						break
					}
				}
				if !qualified {
					bad = append(bad, name+" in: "+strings.TrimSpace(sentence))
					break
				}
			}
		}
	}
	return bad
}

const toolQualifierReach = 110

func servedTextCaller(t *testing.T, tools ...string) (*ProcessConfig, *CallerContext) {
	t.Helper()
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, tools...)
	return (&Bootstrap{Capabilities: caps}).split(io.Discard)
}

func TestServedTextNamesOnlyToolsTheCallerCanList(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []string
	}{
		{"question without interpretation", []string{toolDataCatalog, toolFindSubjects, toolInvestigateQuestion}},
		{"no find_subjects", []string{toolDataCatalog, toolReadFacts, toolReadRelationships, toolInvestigateWithInterpretation}},
		{"data tools only", []string{toolDataCatalog, toolFindSubjects, toolReadFacts, toolReadRelationships, toolRunOperation}},
		{"interpretation without question", []string{toolDataCatalog, toolFindSubjects, toolReadFacts, toolInvestigateWithInterpretation}},
		{"no data catalog", []string{toolFindSubjects, toolReadFacts, toolInvestigateWithInterpretation, toolInvestigationResult}},
		{"everything", []string{toolDataCatalog, toolFindSubjects, toolReadFacts, toolReadRelationships, toolRunOperation, toolGraphQLQuery, toolInvestigateQuestion, toolInvestigateWithInterpretation, toolInvestigationResult}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, caller := servedTextCaller(t, tc.tools...)
			session := connectedClientForCaller(t, cfg, caller)
			listed := listedToolNames(t, session)

			var bad []string
			for _, b := range unqualifiedUnlistedMentions(session.InitializeResult().Instructions, listed) {
				bad = append(bad, "instructions: "+b)
			}
			tools, err := session.ListTools(context.Background(), nil)
			require.NoError(t, err)
			for _, tool := range tools.Tools {
				for _, b := range unqualifiedUnlistedMentions(tool.Description, listed) {
					bad = append(bad, "tool "+tool.Name+": "+b)
				}
			}
			for _, res := range guide.Resources() {
				read, err := readResource(t, session, res.URI)
				require.NoError(t, err, res.URI)
				for _, b := range unqualifiedUnlistedMentions(res.Description, listed) {
					bad = append(bad, res.URI+" description: "+b)
				}
				for _, c := range read.Contents {
					for _, b := range unqualifiedUnlistedMentions(c.Text, listed) {
						bad = append(bad, res.URI+": "+b)
					}
				}
			}
			require.Empty(t, strings.Join(bad, "\n"))
		})
	}
}

func TestServedInstructionsListTheModelFreeReadToolsInTheMainFlow(t *testing.T) {
	cfg, caller := servedTextCaller(t, toolDataCatalog, toolFindSubjects, toolReadFacts, toolReadRelationships, toolRunOperation)
	session := connectedClientForCaller(t, cfg, caller)
	text := session.InitializeResult().Instructions
	start := strings.Index(text, "A. You plan the reads yourself")
	end := strings.Index(text, "Rules for A:")
	require.True(t, start >= 0 && end > start)
	section := text[start:end]
	require.Contains(t, section, "- read_facts:")
	require.Contains(t, section, "- read_relationships:")
}

func TestDataGuideMainFlowListsReadFactsWithRunOperation(t *testing.T) {
	for _, serviceModel := range []bool{true, false} {
		text, err := guide.TextFor(guide.URIData, serviceModel)
		require.NoError(t, err)
		require.NotContains(t, text, "3b.")
		flow := text[strings.Index(text, "## The flow"):]
		flow = flow[:strings.Index(flow, "Read each answer in this order")]
		require.Contains(t, flow, "`read_facts`")
		require.Contains(t, flow, "`read_relationships`")
	}
}

func TestEveryGuideThatNamesAToolCarriesTheAvailabilityNote(t *testing.T) {
	for _, serviceModel := range []bool{true, false} {
		for _, res := range guide.Resources() {
			text, err := guide.TextFor(res.URI, serviceModel)
			require.NoError(t, err)
			named := false
			for _, name := range optionalToolNames {
				named = named || strings.Contains(text, name)
			}
			if named {
				require.Contains(t, text, guide.ToolAvailabilityNote, res.URI)
			}
		}
	}
}

func TestUnqualifiedMentionDetectorFlagsAPlantedSentence(t *testing.T) {
	listed := []string{toolDataCatalog}
	require.NotEmpty(t, unqualifiedUnlistedMentions("Plan the reads with `run_operation`. Then rank.", listed))
	require.NotEmpty(t, unqualifiedUnlistedMentions("Line one.\n- `investigate_with_interpretation` answers it", listed))
	require.Empty(t, unqualifiedUnlistedMentions("Plan the reads with `run_operation` (if listed). Use data_catalog", listed))
	far := "Use run_operation for the big reads, and plan them well before you call it, with the ids you hold and the windows you chose, then rank the rows (if listed)"
	require.NotEmpty(t, unqualifiedUnlistedMentions(far, listed))
	require.Empty(t, unqualifiedUnlistedMentions("Use `read_facts` when `tools/list` shows it", listed))
}
