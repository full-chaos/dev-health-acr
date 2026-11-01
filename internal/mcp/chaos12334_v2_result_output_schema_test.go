package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12334: the investigate_question and investigation_result tools hand a
// stored result to the client unchanged, and a stored result can be
// context_fabric_investigation_result.v2. The output schemas a client gets
// from tools/list must admit that result as it is, never as a v1 result.
//
// These tests drive the real tools over a real MCP client session: the result
// comes from the hosted fixture through the sidecar client, the handler builds
// the response, and the client reads both the response and the output schema
// from the server. The response is validated against that served schema.

// readResultExample decodes a published investigation result example.
func readResultExample(t *testing.T, name string) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	if err := contractsv1.ValidateStoredResult(result); err != nil {
		t.Fatalf("premise: %s is not a valid stored result: %v", name, err)
	}
	return result
}

// servedOutputSchemaDocuments returns the output schema of each listed tool
// as the server serves it in tools/list, decoded.
func servedOutputSchemaDocuments(t *testing.T, session *mcpsdk.ClientSession, tools ...string) map[string]map[string]any {
	t.Helper()
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, tool := range listed.Tools {
		encoded, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(encoded, &document); err != nil {
			t.Fatalf("%s: decode the served output schema: %v", tool.Name, err)
		}
		out[tool.Name] = document
	}
	for _, tool := range tools {
		if out[tool] == nil {
			t.Fatalf("%s is not listed; the measurement did not happen", tool)
		}
	}
	return out
}

// compileSchemaDocument resolves a decoded JSON Schema document.
func compileSchemaDocument(t *testing.T, label string, document map[string]any) *jsonschema.Resolved {
	t.Helper()
	var schema jsonschema.Schema
	if err := json.Unmarshal(remarshal(t, document), &schema); err != nil {
		t.Fatalf("%s: decode the schema: %v", label, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: resolve the schema: %v", label, err)
	}
	return resolved
}

// servedOutputSchemas compiles the output schema of each listed tool as the
// server serves it in tools/list.
func servedOutputSchemas(t *testing.T, session *mcpsdk.ClientSession, tools ...string) map[string]*jsonschema.Resolved {
	t.Helper()
	out := map[string]*jsonschema.Resolved{}
	for name, document := range servedOutputSchemaDocuments(t, session, tools...) {
		out[name] = compileSchemaDocument(t, name, document)
	}
	return out
}

// callServedTool calls one tool over the client session and returns its
// structured content as the client received it.
func callServedTool(t *testing.T, session *mcpsdk.ClientSession, tool string, arguments map[string]any) (map[string]any, []byte) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	if result.IsError {
		t.Fatalf("%s reported an error: %s", tool, toolResultText(result))
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded, encoded
}

func schemaVersionAt(document map[string]any, property string) string {
	member, _ := document[property].(map[string]any)
	version, _ := member["schema_version"].(string)
	return version
}

func remarshal(t *testing.T, document map[string]any) []byte {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// cloneDocument deep-copies a decoded JSON document.
func cloneDocument(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	var copied map[string]any
	if err := json.Unmarshal(remarshal(t, document), &copied); err != nil {
		t.Fatal(err)
	}
	return copied
}

// (a) and (b): a v2 result, and a v1 result, reach the client through both
// tools and validate against the output schema the server serves. The
// responses are also held to the offline producer check, which refuses a
// served key no schema branch declares.
func TestCHAOS12334StoredResultsValidateAgainstTheServedOutputSchemas(t *testing.T) {
	cases := []struct {
		name    string
		example string
		version string
	}{
		{"v2 result", "context_fabric_investigation_result.v2.json", contractsv1.ContextFabricInvestigationResultSchemaV2},
		{"v1 result", "context_fabric_investigation_result.v1.json", contractsv1.ContextFabricInvestigationResultSchema},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := readResultExample(t, tc.example)
			if result.SchemaVersion != tc.version {
				t.Fatalf("premise: %s carries schema_version %q, want %q", tc.example, result.SchemaVersion, tc.version)
			}
			session, closeFn := connectedClient(t, answerFixtureBootstrap(t, result, nil))
			defer closeFn()
			served := servedOutputSchemas(t, session, toolInvestigateQuestion, toolInvestigationResult)

			t.Run("investigate_question with the full result", func(t *testing.T) {
				document, encoded := callServedTool(t, session, toolInvestigateQuestion, map[string]any{"question": result.Question, "include_full_result": true})
				if got := schemaVersionAt(document, "full_result"); got != tc.version {
					t.Fatalf("full_result.schema_version = %q, want %q; the full result was not attached unchanged", got, tc.version)
				}
				if err := served[toolInvestigateQuestion].Validate(document); err != nil {
					t.Errorf("the served investigate_question output schema refuses the response: %v", err)
				}
				if err := contractcheck.ValidateSerializedStrict("", "mcp_investigate_question_response.v1.schema.json", encoded); err != nil {
					t.Errorf("the producer check refuses the response: %v", err)
				}
			})

			// The answer projection is the structured member of
			// investigate_question. It is built from the stored result by
			// answerprojection.Project, which this tool calls.
			t.Run("investigate_question projection only", func(t *testing.T) {
				document, encoded := callServedTool(t, session, toolInvestigateQuestion, map[string]any{"question": result.Question})
				if document["full_result"] != nil {
					t.Fatal("premise: a full result was attached although include_full_result was not set")
				}
				if got := schemaVersionAt(document, "structured"); got != contractsv1.ContextFabricAnswerProjectionSchema {
					t.Fatalf("structured.schema_version = %q, want the answer projection", got)
				}
				if err := served[toolInvestigateQuestion].Validate(document); err != nil {
					t.Errorf("the served investigate_question output schema refuses the projection of a %s: %v", tc.name, err)
				}
				if err := contractcheck.ValidateSerializedStrict("", "mcp_investigate_question_response.v1.schema.json", encoded); err != nil {
					t.Errorf("the producer check refuses the projection of a %s: %v", tc.name, err)
				}
			})

			t.Run("investigation_result", func(t *testing.T) {
				document, encoded := callServedTool(t, session, toolInvestigationResult, map[string]any{"result_id": result.ResultID})
				if got := schemaVersionAt(document, "structured"); got != tc.version {
					t.Fatalf("structured.schema_version = %q, want %q; the result was not returned unchanged", got, tc.version)
				}
				if err := served[toolInvestigationResult].Validate(document); err != nil {
					t.Errorf("the served investigation_result output schema refuses the response: %v", err)
				}
				if err := contractcheck.ValidateSerializedStrict("", "mcp_investigation_result_response.v1.schema.json", encoded); err != nil {
					t.Errorf("the producer check refuses the response: %v", err)
				}
			})
		})
	}
}

// (c): the v2 alternative is the real v2 result schema, not an open object. A
// v2 body whose structure_needs has no anchor_options (or no structure_needs
// at all, or an empty anchor_options) is refused by the served schemas. The
// control relabels the same broken body v1 and is accepted: the refusal comes
// from the v2 alternative's own rule, not from some other part of the body.
func TestCHAOS12334TheServedSchemasRefuseAV2ResultWithoutAnchorOptions(t *testing.T) {
	result := readResultExample(t, "context_fabric_investigation_result.v2.json")
	session, closeFn := connectedClient(t, answerFixtureBootstrap(t, result, nil))
	defer closeFn()
	served := servedOutputSchemas(t, session, toolInvestigateQuestion, toolInvestigationResult)

	iq, _ := callServedTool(t, session, toolInvestigateQuestion, map[string]any{"question": result.Question, "include_full_result": true})
	ir, _ := callServedTool(t, session, toolInvestigationResult, map[string]any{"result_id": result.ResultID})
	surfaces := []struct {
		tool       string
		schemaFile string
		document   map[string]any
		property   string
	}{
		{toolInvestigateQuestion, "mcp_investigate_question_response.v1.schema.json", iq, "full_result"},
		{toolInvestigationResult, "mcp_investigation_result_response.v1.schema.json", ir, "structured"},
	}
	breaks := []struct {
		name  string
		apply func(map[string]any)
	}{
		{"no anchor_options", func(body map[string]any) {
			delete(body["structure_needs"].(map[string]any), "anchor_options")
		}},
		{"empty anchor_options", func(body map[string]any) {
			body["structure_needs"].(map[string]any)["anchor_options"] = []any{}
		}},
		{"no structure_needs", func(body map[string]any) { delete(body, "structure_needs") }},
	}
	for _, surface := range surfaces {
		if err := served[surface.tool].Validate(surface.document); err != nil {
			t.Fatalf("%s: premise: the unbroken v2 response is refused: %v", surface.tool, err)
		}
		for _, broken := range breaks {
			t.Run(surface.tool+"/"+broken.name, func(t *testing.T) {
				document := cloneDocument(t, surface.document)
				body := document[surface.property].(map[string]any)
				broken.apply(body)
				if served[surface.tool].Validate(document) == nil {
					t.Errorf("the served %s output schema admits a v2 %s with %s", surface.tool, surface.property, broken.name)
				}
				if contractcheck.ValidateSerialized("", surface.schemaFile, remarshal(t, document)) == nil {
					t.Errorf("%s admits a v2 %s with %s", surface.schemaFile, surface.property, broken.name)
				}

				body["schema_version"] = contractsv1.ContextFabricInvestigationResultSchema
				if err := served[surface.tool].Validate(document); err != nil {
					t.Errorf("control: the served %s output schema refuses the same body labelled v1: %v", surface.tool, err)
				}
			})
		}
	}
}

// The served schemas put the v1 and v2 results in an anyOf, and say a body
// matches exactly one alternative. This holds that claim: a served v1 result
// and a served v2 result each match exactly the alternative of their own
// version, under both tools.
func TestCHAOS12334AServedResultMatchesExactlyOneAlternative(t *testing.T) {
	surfaces := []struct {
		tool     string
		property string
	}{
		{toolInvestigateQuestion, "full_result"},
		{toolInvestigationResult, "structured"},
	}
	for _, example := range []string{"context_fabric_investigation_result.v1.json", "context_fabric_investigation_result.v2.json"} {
		result := readResultExample(t, example)
		session, closeFn := connectedClient(t, answerFixtureBootstrap(t, result, nil))
		documents := servedOutputSchemaDocuments(t, session, toolInvestigateQuestion, toolInvestigationResult)
		iq, _ := callServedTool(t, session, toolInvestigateQuestion, map[string]any{"question": result.Question, "include_full_result": true})
		ir, _ := callServedTool(t, session, toolInvestigationResult, map[string]any{"result_id": result.ResultID})
		closeFn()
		served := map[string]map[string]any{toolInvestigateQuestion: iq, toolInvestigationResult: ir}
		for _, surface := range surfaces {
			body, ok := served[surface.tool][surface.property].(map[string]any)
			if !ok {
				t.Fatalf("%s %s: the response carries no %s", example, surface.tool, surface.property)
			}
			document := documents[surface.tool]
			property, _ := document["properties"].(map[string]any)[surface.property].(map[string]any)
			alternatives, _ := property["anyOf"].([]any)
			if len(alternatives) != 2 {
				t.Fatalf("%s: properties.%s is not an anyOf of two alternatives: %v", surface.tool, surface.property, property)
			}
			var matched []string
			for _, raw := range alternatives {
				ref, _ := raw.(map[string]any)["$ref"].(string)
				alone := map[string]any{"$defs": document["$defs"], "$ref": ref}
				if compileSchemaDocument(t, ref, alone).Validate(body) == nil {
					matched = append(matched, ref)
				}
			}
			want := "#/$defs/" + result.SchemaVersion
			if len(matched) != 1 || matched[0] != want {
				t.Errorf("%s %s: a %s %s matches the alternatives %v, want exactly [%s]", example, surface.tool, result.SchemaVersion, surface.property, matched, want)
			}
		}
	}
}

// The two v2 tool examples are the real handlers' output for the published v2
// result example, indented. To regenerate after a deliberate change:
//
//	WRITE_MCP_V2_RESULT_GOLDENS=1 go test ./internal/mcp -run TestCHAOS12334V2ToolExamplesAreTheHandlerOutput
//
// and review the diff. Without the variable the test fails on any difference.
// The variable has no ACR_ prefix because TestMain removes every ACR_ variable.
func TestCHAOS12334V2ToolExamplesAreTheHandlerOutput(t *testing.T) {
	result := readResultExample(t, "context_fabric_investigation_result.v2.json")
	boot := answerFixtureBootstrap(t, result, nil)
	cases := []struct {
		golden    string
		arguments any
		invoke    func(context.Context, *Bootstrap, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error)
	}{
		{"mcp_investigate_question_response_full_result_v2.v1.json", contractsv1.MCPInvestigateQuestionRequest{Question: result.Question, IncludeFullResult: true}, invokeInvestigateQuestion},
		{"mcp_investigation_result_response_result_v2.v1.json", contractsv1.MCPInvestigationResultRequest{ResultID: result.ResultID}, invokeInvestigationResult},
	}
	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			arguments, err := json.Marshal(tc.arguments)
			if err != nil {
				t.Fatal(err)
			}
			toolResult, err := tc.invoke(context.Background(), boot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: arguments}})
			if err != nil {
				t.Fatalf("protocol error: %v", err)
			}
			if toolResult.IsError {
				t.Fatalf("tool reported an error: %s", toolResultText(toolResult))
			}
			raw, ok := toolResult.StructuredContent.(json.RawMessage)
			if !ok {
				t.Fatalf("structured content is %T, want the encoded response", toolResult.StructuredContent)
			}
			var indented bytes.Buffer
			if err := json.Indent(&indented, raw, "", "  "); err != nil {
				t.Fatal(err)
			}
			indented.WriteByte('\n')
			path := filepath.Join("..", "..", "contracts", "examples", "v1", tc.golden)
			if os.Getenv("WRITE_MCP_V2_RESULT_GOLDENS") == "1" {
				if err := os.WriteFile(path, indented.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s (%d bytes)", tc.golden, indented.Len())
				return
			}
			onDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v (regenerate with WRITE_MCP_V2_RESULT_GOLDENS=1)", tc.golden, err)
			}
			if !bytes.Equal(onDisk, indented.Bytes()) {
				t.Fatalf("%s differs from the handler output for the published v2 result (%d bytes on disk, %d emitted); regenerate with WRITE_MCP_V2_RESULT_GOLDENS=1 and review the diff", tc.golden, len(onDisk), indented.Len())
			}
		})
	}
}
