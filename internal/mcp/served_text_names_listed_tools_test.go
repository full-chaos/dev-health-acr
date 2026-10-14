package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
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
	"the server lists", "this server offers", "may be unavailable", "only when the hosted API enables",
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
				if inIdentifier(plain, at, from) {
					continue
				}
				lo, hi := max(0, at-toolQualifierBackReach), min(len(lower), at+len(name)+toolQualifierReach)
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
				}
			}
		}
	}
	return bad
}

const toolQualifierReach = 110

// toolQualifierBackReach is how far before a name a qualifier still counts.
// A qualifier normally follows the tool it qualifies; the short look-back
// covers "(if listed; section x for run_operation)" and "if this server offers
// them (investigate_question ...)" and stops one qualified tool from covering
// a later bare name in the same sentence.
const toolQualifierBackReach = 70

// inIdentifier reports whether plain[at:end] is only part of a longer
// identifier such as context_fabric_investigation_result, which names a
// schema and not a tool.
func inIdentifier(plain string, at, end int) bool {
	isIdent := func(b byte) bool {
		return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
	}
	return at > 0 && isIdent(plain[at-1]) || end < len(plain) && isIdent(plain[end])
}

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
	sibling := "read data_catalog (if this server offers it) for the operations and their variables, take ids from find_subjects, then call this"
	require.Equal(t, []string{"find_subjects in: " + sibling}, unqualifiedUnlistedMentions(sibling, []string{toolRunOperation}))
	require.Empty(t, unqualifiedUnlistedMentions("A result whose schema_version is context_fabric_investigation_result.v2", listed))
}

// schemaOwners returns the tools whose input or output schema is the given
// file. A schema may name its own tools freely; every other optional tool it
// names must carry the availability qualifier, because the schema text is the
// same for every caller.
func schemaOwners(t *testing.T, file string) []string {
	t.Helper()
	var manifest toolManifest
	data, err := schemaFiles.ReadFile(toolManifestFile)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &manifest))
	var owners []string
	for _, tool := range manifest.Tools {
		if path.Base(tool.InputSchemaRef) == path.Base(file) || path.Base(tool.OutputSchemaRef) == path.Base(file) {
			owners = append(owners, tool.Name)
		}
	}
	return owners
}

// schemaDescriptionProblems walks a decoded JSON document and returns one
// line per unqualified sibling-tool mention in any description string, naming
// the JSON path and the tool.
func schemaDescriptionProblems(node any, jsonPath string, exempt []string, inspected *int) []string {
	var bad []string
	switch v := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := v[key]
			if text, ok := child.(string); ok && key == "description" {
				*inspected++
				for _, b := range unqualifiedUnlistedMentions(text, exempt) {
					bad = append(bad, jsonPath+".description: "+b)
				}
				continue
			}
			bad = append(bad, schemaDescriptionProblems(child, jsonPath+"."+key, exempt, inspected)...)
		}
	case []any:
		for i, child := range v {
			bad = append(bad, schemaDescriptionProblems(child, fmt.Sprintf("%s[%d]", jsonPath, i), exempt, inspected)...)
		}
	}
	return bad
}

func decodeJSONAny(t *testing.T, raw any) any {
	t.Helper()
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	var doc any
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc
}

// TestServedSchemasNameSiblingToolsOnlyWithTheAvailabilityQualifier drives the
// real tools/list for a caller that can list every tool and holds the
// description, input schema and output schema each tool serves to the same
// rule as the instructions: a sibling tool the caller may not have is named
// only with the availability qualifier. The schema text is identical for every
// caller, so a tool is exempt only inside its own schemas. context_for_task and
// source_evidence are exempt everywhere because no caller can lack them.
func TestServedSchemasNameSiblingToolsOnlyWithTheAvailabilityQualifier(t *testing.T) {
	cfg, caller := servedTextCaller(t, toolDataCatalog, toolFindSubjects, toolReadFacts, toolReadRelationships, toolRunOperation, toolGraphQLQuery, toolInvestigateQuestion, toolInvestigateWithInterpretation, toolInvestigationResult)
	session := connectedClientForCaller(t, cfg, caller)
	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(tools.Tools), 11, "the measurement did not happen: tools/list returned too few tools")

	var bad []string
	inspected := 0
	for _, tool := range tools.Tools {
		entry := manifestEntry(tool.Name)
		inputOwners := schemaOwners(t, entry.InputSchemaRef)
		outputOwners := schemaOwners(t, entry.OutputSchemaRef)
		for _, b := range unqualifiedUnlistedMentions(tool.Description, []string{tool.Name}) {
			bad = append(bad, "tools/list "+tool.Name+" description: "+b)
		}
		for _, p := range schemaDescriptionProblems(decodeJSONAny(t, tool.InputSchema), "$", inputOwners, &inspected) {
			bad = append(bad, "tools/list "+tool.Name+" inputSchema "+p)
		}
		for _, p := range schemaDescriptionProblems(decodeJSONAny(t, tool.OutputSchema), "$", outputOwners, &inspected) {
			bad = append(bad, "tools/list "+tool.Name+" outputSchema "+p)
		}
	}
	require.Greater(t, inspected, 200, "the measurement did not happen: walked too few served schema descriptions")
	require.Empty(t, strings.Join(bad, "\n"), "%d unqualified sibling-tool mentions in served tool text", len(bad))
}

// TestEmbeddedSchemaFilesNameSiblingToolsOnlyWithTheAvailabilityQualifier
// holds every embedded contract file, served or not, to the same rule, by file
// and JSON path.
func TestEmbeddedSchemaFilesNameSiblingToolsOnlyWithTheAvailabilityQualifier(t *testing.T) {
	entries, err := schemaFiles.ReadDir("schemas")
	require.NoError(t, err)
	var bad []string
	inspected, files := 0, 0
	for _, e := range entries {
		file := "schemas/" + e.Name()
		if file == toolManifestFile {
			var manifest toolManifest
			data, err := schemaFiles.ReadFile(file)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &manifest))
			for i, tool := range manifest.Tools {
				inspected++
				for _, b := range unqualifiedUnlistedMentions(tool.Description, []string{tool.Name}) {
					bad = append(bad, fmt.Sprintf("%s $.tools[%d].description (%s): %s", e.Name(), i, tool.Name, b))
				}
			}
			files++
			continue
		}
		files++
		for _, p := range schemaDescriptionProblems(readEmbeddedJSON(t, file), "$", schemaOwners(t, file), &inspected) {
			bad = append(bad, e.Name()+" "+p)
		}
	}
	require.Greater(t, files, 20, "the measurement did not happen: too few embedded files")
	require.Greater(t, inspected, 200, "the measurement did not happen: walked too few descriptions")
	require.Empty(t, strings.Join(bad, "\n"), "%d unqualified sibling-tool mentions in embedded contract files", len(bad))
}

// TestAlwaysRegisteredToolsAreNeverOmitted pins the exemption the two scans
// rely on: context_for_task and source_evidence are not in optionalToolNames
// because no caller can lack them. checkCompatibility refuses a capability
// snapshot without either (it runs for STDIO bootstrap and for every hosted
// caller), and a server for the lowest valid caller registers both.
func TestAlwaysRegisteredToolsAreNeverOmitted(t *testing.T) {
	for _, tool := range []string{toolContextForTask, toolSourceEvidence} {
		require.NotContains(t, optionalToolNames, tool)
		caps := validCapabilitiesFixture()
		require.NoError(t, checkCompatibility(caps, "1.2.0", false))
		caps.EnabledTools = slices.DeleteFunc(slices.Clone(caps.EnabledTools), func(name string) bool { return name == tool })
		err := checkCompatibility(caps, "1.2.0", false)
		require.Error(t, err, "a caller without %s must be refused", tool)
		assertCompatCategory(t, err, "entitlement")
	}
	cfg, caller := servedTextCaller(t)
	listed := listedToolNames(t, connectedClientForCaller(t, cfg, caller))
	require.Contains(t, listed, toolContextForTask)
	require.Contains(t, listed, toolSourceEvidence)
}

func TestSchemaScanFlagsAPlantedDescriptionByPath(t *testing.T) {
	doc := map[string]any{
		"description": "Input for the data_catalog tool.",
		"properties": map[string]any{
			"bare":      map[string]any{"description": "Take ids from find_subjects."},
			"qualified": map[string]any{"description": "Take ids from find_subjects (if listed)."},
			"loose":     map[string]any{"description": "Optional. Take ids from find_subjects."},
			"core":      map[string]any{"description": "Pass it to source_evidence or context_for_task."},
			"nested": map[string]any{"items": []any{
				map[string]any{"description": "Pass it to run_operation."},
			}},
		},
	}
	inspected := 0
	bad := schemaDescriptionProblems(doc, "$", []string{toolDataCatalog}, &inspected)
	require.Equal(t, 6, inspected)
	require.Len(t, bad, 3, strings.Join(bad, "\n"))
	require.Contains(t, bad[0], "$.properties.bare.description: find_subjects in:")
	require.Contains(t, bad[1], "$.properties.loose.description: find_subjects in:")
	require.Contains(t, bad[2], "$.properties.nested.items[0].description: run_operation in:")
}
