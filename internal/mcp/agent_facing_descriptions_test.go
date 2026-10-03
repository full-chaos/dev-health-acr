package mcp

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var requestSchemaFiles = []string{
	contextForTaskRequestSchemaFile,
	sourceEvidenceRequestSchemaFile,
	investigateQuestionRequestSchemaFile,
	investigateWithInterpretationRequestSchemaFile,
	investigationResultRequestSchemaFile,
	dataCatalogRequestSchemaFile,
	findSubjectsRequestSchemaFile,
	runOperationRequestSchemaFile,
}

func readEmbeddedJSON(t *testing.T, file string) map[string]any {
	t.Helper()
	data, err := schemaFiles.ReadFile(file)
	if err != nil {
		t.Fatalf("read embedded %s: %v", file, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode %s: %v", file, err)
	}
	return doc
}

func hasDescription(node map[string]any) bool {
	text, _ := node["description"].(string)
	return strings.TrimSpace(text) != ""
}

// undescribedPaths returns the path of every property schema and every $defs
// entry under node that carries no description, and how many nodes it
// inspected, so a walk that finds nothing to inspect can fail loudly.
func undescribedPaths(node map[string]any, path string, inspected *int) []string {
	var missing []string
	if props, ok := node["properties"].(map[string]any); ok {
		for name, raw := range props {
			child, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			*inspected++
			childPath := path + ".properties." + name
			if !hasDescription(child) {
				missing = append(missing, childPath)
			}
			missing = append(missing, undescribedPaths(child, childPath, inspected)...)
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		missing = append(missing, undescribedPaths(items, path+".items", inspected)...)
	}
	if defs, ok := node["$defs"].(map[string]any); ok {
		for name, raw := range defs {
			child, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			*inspected++
			childPath := path + ".$defs." + name
			if !hasDescription(child) {
				missing = append(missing, childPath)
			}
			missing = append(missing, undescribedPaths(child, childPath, inspected)...)
		}
	}
	sort.Strings(missing)
	return missing
}

func collectDescriptions(node any, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		if text, ok := v["description"].(string); ok {
			*out = append(*out, text)
		}
		for _, child := range v {
			collectDescriptions(child, out)
		}
	case []any:
		for _, child := range v {
			collectDescriptions(child, out)
		}
	}
}

func TestRequestSchemasDescribeEveryProperty(t *testing.T) {
	total := 0
	for _, file := range requestSchemaFiles {
		doc := readEmbeddedJSON(t, file)
		if !hasDescription(doc) {
			t.Errorf("%s: root has no description", file)
		}
		inspected := 0
		missing := undescribedPaths(doc, "$", &inspected)
		if inspected == 0 {
			t.Fatalf("%s: walk inspected no properties; the measurement did not happen", file)
		}
		total += inspected
		for _, path := range missing {
			t.Errorf("%s: %s has no description", file, path)
		}
	}
	if total < 40 {
		t.Fatalf("inspected only %d schema nodes across the request schemas; expected the walk to reach the nested properties", total)
	}
}

func TestRequestSchemasCarryNoInternalProse(t *testing.T) {
	banned := []string{"CHAOS", "design brief", "DP12", "sol-max"}
	seen := 0
	for _, file := range append(slices.Clone(requestSchemaFiles), toolManifestFile) {
		var texts []string
		collectDescriptions(readEmbeddedJSON(t, file), &texts)
		seen += len(texts)
		for _, text := range texts {
			for _, word := range banned {
				if strings.Contains(text, word) {
					t.Errorf("%s: description contains internal wording %q: %.80s", file, word, text)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("collected no descriptions; the measurement did not happen")
	}
}

func TestRequestSchemaExamplesValidate(t *testing.T) {
	total := 0
	for _, file := range requestSchemaFiles {
		data, err := schemaFiles.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if len(schema.Examples) == 0 {
			t.Errorf("%s: no examples", file)
			continue
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatalf("%s: resolve: %v", file, err)
		}
		for i, example := range schema.Examples {
			total++
			if err := resolved.Validate(example); err != nil {
				t.Errorf("%s: example %d does not validate: %v", file, i, err)
			}
		}
	}
	if total == 0 {
		t.Fatal("validated no examples; the measurement did not happen")
	}
}

// TestToolDescriptionsNameTheirRequiredInputs pins a minimum specificity:
// a description must be more than a one-liner and must name every required
// input of the tool it describes, so an agent can call the tool from the
// description alone.
// dataToolsWithOptionalInputs are the tools whose inputs are all optional.
var dataToolsWithOptionalInputs = map[string]bool{toolDataCatalog: true, toolFindSubjects: true}

func TestToolDescriptionsNameTheirRequiredInputs(t *testing.T) {
	var manifest toolManifest
	data, err := schemaFiles.ReadFile(toolManifestFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tools) == 0 {
		t.Fatal("manifest lists no tools; the measurement did not happen")
	}
	checked := 0
	for _, tool := range manifest.Tools {
		if len(tool.Description) < 200 {
			t.Errorf("%s: description is %d bytes; state when to use it, its inputs and what it returns", tool.Name, len(tool.Description))
		}
		if tool.Name == toolRecordEpisode {
			// The writeback tool takes a large structured record, not a
			// short list of inputs a description can enumerate.
			continue
		}
		schemaFile := "schemas/" + tool.InputSchemaRef[strings.LastIndex(tool.InputSchemaRef, "/")+1:]
		schemaDoc := readEmbeddedJSON(t, schemaFile)
		required, _ := schemaDoc["required"].([]any)
		if len(required) == 0 && dataToolsWithOptionalInputs[tool.Name] {
			// CHAOS-7072: data_catalog and find_subjects have no field that
			// is always required (a top-level anyOf would be refused by
			// some model APIs), so the description must name every
			// property instead, and the schema must declare at least one.
			properties, _ := schemaDoc["properties"].(map[string]any)
			if len(properties) == 0 {
				t.Fatalf("%s: request schema declares no property; the measurement did not happen", tool.Name)
			}
			for name := range properties {
				checked++
				if !strings.Contains(tool.Description, name) {
					t.Errorf("%s: description does not name input %q", tool.Name, name)
				}
			}
			continue
		}
		if len(required) == 0 {
			t.Fatalf("%s: request schema declares no required input; the measurement did not happen", tool.Name)
		}
		for _, name := range required {
			checked++
			if !strings.Contains(tool.Description, name.(string)) {
				t.Errorf("%s: description does not name required input %q", tool.Name, name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("checked no required inputs")
	}
}

func TestToolDescriptionsPointToFollowUpTools(t *testing.T) {
	var manifest toolManifest
	data, _ := schemaFiles.ReadFile(toolManifestFile)
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, tool := range manifest.Tools {
		byName[tool.Name] = tool.Description
	}
	links := map[string][]string{
		"context_for_task":                {"source_evidence", "investigate_question"},
		"investigate_question":            {"investigation_result", "source_evidence", "context_for_task", "prior_", "result_id"},
		"investigate_with_interpretation": {"investigate_question", "interpret_question", "prompts/get", "interpretation", "contract", "_meta"},
		"investigation_result":            {"investigate_question"},
		"source_evidence":                 {"context_for_task", "investigate_question"},
	}
	for tool, wants := range links {
		description, ok := byName[tool]
		if !ok {
			t.Fatalf("manifest has no %s", tool)
		}
		for _, want := range wants {
			if !strings.Contains(description, want) {
				t.Errorf("%s: description does not mention %q", tool, want)
			}
		}
	}
}

func TestServerInstructionsAreAnAgentGuide(t *testing.T) {
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, toolInvestigateQuestion, toolInvestigationResult)
	instructions := serverInstructions(bootHandlerHalvesConfig(&Bootstrap{Capabilities: caps}))

	// The four things the guide must say, each pinned by its own phrase.
	for name, phrase := range map[string]string{
		"tool choice":     "Choosing a tool:",
		"question shapes": "Question shapes that work:",
		"receipt flow":    "prior_window_receipts",
		// CHAOS-6557: a stated period (evidence_window or a phrase in the
		// question) is used as given; only a period the caller did NOT state
		// is proposed and confirmed with a winr_ receipt.
		"stated period used as given": "A period you state is used as given and reported back.",
		"window confirmation":         "Without a period the first answer proposes one and asks you to confirm it: send the matching winr_ receipt back in prior_window_receipts",
		"clarification flow":          "clarification_required",
		"untrusted content":           "untrusted data",
		"live authorization":          "re-checked against your credential on every call",
		"opaque ids":                  "opaque",
	} {
		if !strings.Contains(instructions, phrase) {
			t.Errorf("instructions lack the %s phrase %q", name, phrase)
		}
	}
	if strings.Contains(instructions, "The first answer then asks you to confirm the window") {
		t.Error("instructions still say a stated period gets a confirmation turn")
	}
	for _, tool := range []string{toolContextForTask, toolSourceEvidence, toolInvestigateQuestion, toolInvestigationResult} {
		if !strings.Contains(instructions, tool) {
			t.Errorf("instructions do not name %s", tool)
		}
	}
	if lines := strings.Count(strings.TrimSpace(instructions), "\n") + 1; lines > 60 {
		t.Errorf("instructions run %d lines; they are sent on every discover, keep them under 60", lines)
	}
	if strings.Contains(instructions, toolRecordEpisode) {
		t.Errorf("read-only instructions must not name %s", toolRecordEpisode)
	}
}

func TestServerInstructionsNameOnlyRegisteredTools(t *testing.T) {
	instructions := serverInstructions(bootHandlerHalvesConfig(&Bootstrap{Capabilities: validCapabilitiesFixture()}))
	for _, absent := range []string{toolInvestigateQuestion, toolInvestigationResult, "prior_window_receipts"} {
		if strings.Contains(instructions, absent) {
			t.Errorf("instructions name %q although the hosted API did not advertise it", absent)
		}
	}
	if !strings.Contains(instructions, "untrusted data") || !strings.Contains(instructions, toolContextForTask) {
		t.Errorf("instructions lost the base guidance: %q", instructions)
	}
}

var everyToolName = []string{toolContextForTask, toolSourceEvidence, toolInvestigateQuestion, toolInvestigateWithInterpretation, toolInvestigationResult, toolRecordEpisode, toolDataCatalog, toolFindSubjects, toolRunOperation}

// TestWireMetadataNamesOnlyRegisteredTools drives the real MCP handshake for a
// reduced and a full capability set and holds what an agent actually reads
// (initialize instructions and tools/list descriptions) to what the server
// registered: an unregistered tool may appear in a tool description only
// inside a clause marked "if this server offers", and never in the
// instructions.
func TestWireMetadataNamesOnlyRegisteredTools(t *testing.T) {
	fx := newFixtureServer(t)
	cases := map[string]func() *Bootstrap{
		"reduced": func() *Bootstrap { return newFixtureBootstrap(t, fx) },
		"full": func() *Bootstrap {
			boot := newFixtureBootstrap(t, fx)
			boot.Capabilities.EnabledTools = append(boot.Capabilities.EnabledTools, toolInvestigateQuestion, toolInvestigateWithInterpretation, toolInvestigationResult)
			return boot
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			client, closeFn := connectedClient(t, build())
			defer closeFn()
			listed, err := client.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			registered := map[string]bool{}
			for _, tool := range listed.Tools {
				registered[tool.Name] = true
			}
			if len(registered) < 2 {
				t.Fatalf("tools/list returned %d tools; the measurement did not happen", len(registered))
			}
			if name == "reduced" && registered[toolInvestigateQuestion] {
				t.Fatal("reduced capability set registered investigate_question")
			}
			if name == "full" && !registered[toolInvestigateQuestion] {
				t.Fatal("full capability set did not register investigate_question")
			}
			instructions := client.InitializeResult().Instructions
			if strings.TrimSpace(instructions) == "" {
				t.Fatal("initialize returned no instructions")
			}
			for _, tool := range everyToolName {
				if registered[tool] {
					continue
				}
				if strings.Contains(instructions, tool) {
					t.Errorf("instructions name unregistered tool %s", tool)
				}
				for _, listedTool := range listed.Tools {
					if strings.Contains(listedTool.Description, tool) && !strings.Contains(listedTool.Description, "if this server offers") {
						t.Errorf("%s description names unregistered tool %s without the availability qualifier", listedTool.Name, tool)
					}
				}
			}
			if !registered[toolInvestigateQuestion] && strings.Contains(strings.ToLower(instructions), "investigation") {
				t.Errorf("instructions mention investigation although no investigation tool is registered: %q", instructions)
			}
		})
	}
}

// TestInvestigationResultDescriptionMatchesTheRealResponse calls the tool and
// holds its description to the shape it returns: the full canonical result
// sits in structured, and the fields the description lists are fields of that
// result.
func TestInvestigationResultDescriptionMatchesTheRealResponse(t *testing.T) {
	result := parityResult()
	boot := answerFixtureBootstrap(t, result, nil)
	args, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: result.ResultID})
	if err != nil {
		t.Fatal(err)
	}
	toolResult, err := invokeInvestigationResult(context.Background(), boot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: args}})
	if err != nil || toolResult.IsError {
		t.Fatalf("investigation_result failed: %v %s", err, toolResultText(toolResult))
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(toolResult.StructuredContent.(json.RawMessage), &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response["full_result"]; ok {
		t.Fatal("investigation_result carries a full_result field; the description says the result is in structured")
	}
	var structured map[string]json.RawMessage
	if err := json.Unmarshal(response["structured"], &structured); err != nil {
		t.Fatal(err)
	}
	if _, ok := structured["drivers"]; !ok {
		t.Fatal("structured has no drivers field")
	}

	description := manifestEntry(toolInvestigationResult).Description
	match := regexp.MustCompile(`fields such as ([^)]*)\)`).FindStringSubmatch(description)
	if match == nil {
		t.Fatal("description lists no fields of the structured result")
	}
	listed := 0
	for _, field := range regexp.MustCompile(`[a-z_]+`).FindAllString(match[1], -1) {
		if field == "and" {
			continue
		}
		listed++
		if _, ok := structured[field]; !ok && !resultHasSchemaField(t, field) {
			t.Errorf("description names %q, which is not a field of the returned result", field)
		}
	}
	if listed == 0 {
		t.Fatal("parsed no field names from the description; the measurement did not happen")
	}
}

func resultHasSchemaField(t *testing.T, field string) bool {
	t.Helper()
	doc := readEmbeddedJSON(t, investigationResultResponseSchemaFile)
	defs, _ := doc["$defs"].(map[string]any)
	result, _ := defs["context_fabric_investigation_result.v1"].(map[string]any)
	props, _ := result["properties"].(map[string]any)
	_, ok := props[field]
	return ok
}

// A supplied evidence_window is used as given (no confirmation turn): the
// embedded request schema an agent reads must say so, not that the service may
// ask for the window to be confirmed. Schema parity only proves the two copies
// match; this pins the meaning.
func TestEvidenceWindowSchemaSaysASuppliedWindowIsUsedAsGiven(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(mustReadSchema(investigateQuestionRequestSchemaFile), &schema); err != nil {
		t.Fatalf("decode request schema: %v", err)
	}
	description := schema.Properties["evidence_window"].Description
	if !strings.Contains(description, "used as given") {
		t.Errorf("evidence_window description = %q, want it to say the supplied window is used as given", description)
	}
	if strings.Contains(description, "ask you to confirm") {
		t.Errorf("evidence_window description = %q, still says a supplied window may need confirmation", description)
	}
}
