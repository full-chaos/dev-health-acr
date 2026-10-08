package mcp

import (
	"context"
	"encoding/json"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A budget field sent beside the question instead of inside budget is refused
// by name; it is not dropped so the default budget is served.
func TestInvestigateQuestionRefusesAMisplacedBudgetField(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, parityResult(), &seen)
	result := callInvestigateQuestionRaw(t, boot, map[string]any{"question": "Which teams need attention?", "max_cohort_members": 5})
	if !result.IsError {
		t.Fatal("IsError = false, want a validation refusal for the misplaced field")
	}
	text := toolResultText(result)
	if !strings.HasPrefix(text, "validation:") || !strings.Contains(text, `"max_cohort_members"`) {
		t.Errorf("tool text %q does not name the misplaced field", text)
	}
	if seen.Question != "" {
		t.Error("the hosted API was called for a request the tool must refuse")
	}
}

func TestInvestigateQuestionRefusesAnUnknownNestedField(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"budget": {"question": "q", "budget": map[string]any{"max_members": 5}},
		"scope":  {"question": "q", "scope": map[string]any{"repos": []string{"a/b"}}},
	} {
		t.Run(name, func(t *testing.T) {
			var seen contractsv1.ContextFabricInvestigationRequest
			boot := answerFixtureBootstrap(t, parityResult(), &seen)
			result := callInvestigateQuestionRaw(t, boot, args)
			if !result.IsError || !strings.HasPrefix(toolResultText(result), "validation:") {
				t.Fatalf("result = %q (IsError %v), want a validation refusal", toolResultText(result), result.IsError)
			}
			if seen.Question != "" {
				t.Error("the hosted API was called for a request the tool must refuse")
			}
		})
	}
}

// The budget inside budget still narrows the list.
func TestInvestigateQuestionHonoursABudgetInsideBudget(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, parityResult(), &seen)
	result := callInvestigateQuestionRaw(t, boot, map[string]any{"question": "Which teams need attention?", "budget": map[string]any{"max_cohort_members": 5}})
	if result.IsError {
		t.Fatalf("refused: %s", toolResultText(result))
	}
	if seen.Options.MaxCohortMembers != 5 {
		t.Errorf("max_cohort_members = %d, want 5", seen.Options.MaxCohortMembers)
	}
}

func TestInvestigateWithInterpretationNamesAMisplacedBudgetField(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := withInterpretationTool(answerFixtureBootstrap(t, parityResult(), &seen))
	result, err := invokeInvestigateWithInterpretation(boot, interpretationArgs(t, func(m map[string]any) { m["max_cohort_members"] = 5 }))
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	text := toolResultText(result)
	if !result.IsError || !strings.Contains(text, `"max_cohort_members"`) {
		t.Errorf("tool text %q does not name the misplaced field", text)
	}
	if seen.Question != "" {
		t.Error("the hosted API was called for a request the tool must refuse")
	}
}

func TestDecodeInvestigationArgumentsRefusesTrailingDataAndBoundsTheNamedKey(t *testing.T) {
	var input contractsv1.MCPInvestigateQuestionRequest
	if err := decodeToolArguments([]byte(`{"question":"q"} {"question":"r"}`), &input, false); err == nil {
		t.Fatal("trailing JSON value accepted, want a refusal")
	}
	long := strings.Repeat("k", 200)
	err := decodeToolArguments([]byte(`{"question":"q","`+long+`":1}`), &input, false)
	if err == nil {
		t.Fatal("unknown key accepted")
	}
	message := toolArgumentsMessage(toolInvestigateQuestion, err)
	if strings.Contains(message, strings.Repeat("k", 65)) || !strings.Contains(message, strings.Repeat("k", 64)) {
		t.Errorf("message %q does not bound the named key to 64 characters", message)
	}
}

func TestDecodeInvestigationArgumentsRefusesAClosingDelimiterAfterTheValue(t *testing.T) {
	for _, tail := range []string{"]", "}", ")", ","} {
		var input contractsv1.MCPInvestigateQuestionRequest
		if err := decodeToolArguments([]byte(`{"question":"q"}`+tail), &input, false); err == nil {
			t.Errorf("decode accepted %q after the value", tail)
		}
	}
	var input contractsv1.MCPInvestigateQuestionRequest
	if err := decodeToolArguments([]byte(" {\"question\":\"q\"}\n"), &input, false); err != nil {
		t.Errorf("surrounding whitespace refused: %v", err)
	}
}

type toolRequestCase struct {
	tool    string
	schema  string
	example string
	request func() any
	handle  func(context.Context, *ProcessConfig, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error)
}

// toolRequestCases lists every MCP tool whose request goes through the one
// strict decode. record_episode keeps its own decoder, which is strict, refuses
// trailing data and duplicate keys, and is pinned by its own tests.
func toolRequestCases() []toolRequestCase {
	return []toolRequestCase{
		{toolInvestigateQuestion, investigateQuestionRequestSchemaFile, "mcp_investigate_question_request.v1.json", func() any { return &contractsv1.MCPInvestigateQuestionRequest{} }, handleInvestigateQuestion},
		{toolInvestigateWithInterpretation, investigateWithInterpretationRequestSchemaFile, "mcp_investigate_with_interpretation_request.v1.json", func() any { return &contractsv1.MCPInvestigateWithInterpretationRequest{} }, handleInvestigateWithInterpretation},
		{toolInvestigationResult, investigationResultRequestSchemaFile, "mcp_investigation_result_request.v1.json", func() any { return &contractsv1.MCPInvestigationResultRequest{} }, handleInvestigationResult},
		{toolFindSubjects, findSubjectsRequestSchemaFile, "mcp_find_subjects_request.v1.json", func() any { return &contractsv1.MCPFindSubjectsRequest{} }, handleFindSubjects},
		{toolDataCatalog, dataCatalogRequestSchemaFile, "mcp_data_catalog_request.v1.json", func() any { return &contractsv1.MCPDataCatalogRequest{} }, handleDataCatalog},
		{toolGraphQLQuery, graphqlQueryRequestSchemaFile, "mcp_graphql_query_request.v1.json", func() any { return &contractsv1.MCPGraphQLQueryRequest{} }, handleGraphQLQuery},
		{toolRunOperation, runOperationRequestSchemaFile, "mcp_run_operation_request.v1.json", func() any { return &contractsv1.MCPRunOperationRequest{} }, handleRunOperation},
		{toolReadFacts, readFactsRequestSchemaFile, "mcp_read_facts_request.v1.json", func() any { return &readFactsInput{} }, handleReadFacts},
		{toolReadRelationships, readRelationshipsRequestSchemaFile, "mcp_read_relationships_request.v1.json", func() any { return &readRelationshipsInput{} }, handleReadRelationships},
		{toolSourceEvidence, sourceEvidenceRequestSchemaFile, "mcp_source_evidence_request.v1.json", func() any { return &contractsv1.MCPSourceEvidenceRequest{} }, handleSourceEvidence},
		{toolContextForTask, contextForTaskRequestSchemaFile, "mcp_context_for_task_request.v1.json", func() any { return &contractsv1.MCPContextForTaskRequest{} }, handleContextForTask},
	}
}

func readExample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Every tool refuses an undeclared top-level key by name before any hosted
// call, and its own published example request still decodes.
func TestEveryToolRequestRefusesAnUnknownKeyAndAcceptsItsExample(t *testing.T) {
	for _, tc := range toolRequestCases() {
		t.Run(tc.tool, func(t *testing.T) {
			example := readExample(t, tc.example)
			if err := decodeToolArguments(example, tc.request(), true); err != nil {
				t.Fatalf("published example refused: %v", err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(example, &object); err != nil {
				t.Fatal(err)
			}
			object["surprise_key"] = json.RawMessage(`1`)
			args, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			var seen contractsv1.ContextFabricInvestigationRequest
			boot := withInterpretationTool(answerFixtureBootstrap(t, parityResult(), &seen))
			cfg, callerCtx := callerContextFor(context.Background(), boot)
			result, err := tc.handle(callerCtx, cfg, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: args}})
			if err != nil {
				t.Fatalf("protocol error: %v", err)
			}
			text := toolResultText(result)
			if !result.IsError || !strings.HasPrefix(text, "validation:") || !strings.Contains(text, `"surprise_key"`) {
				t.Fatalf("result = %q (IsError %v), want a validation refusal naming the key", text, result.IsError)
			}
		})
	}
}

// Every property a request schema declares has a field on the Go request, and
// every field has a property, so the strict decode can neither refuse a key the
// schema allows nor allow one the schema forbids.
func TestEveryToolRequestSchemaMatchesItsGoRequestFields(t *testing.T) {
	for _, tc := range toolRequestCases() {
		t.Run(tc.tool, func(t *testing.T) {
			raw, err := schemaFiles.ReadFile(tc.schema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil || len(schema.Properties) == 0 {
				t.Fatalf("%s: properties unreadable (%v)", tc.schema, err)
			}
			tags := jsonTagsOf(reflect.TypeOf(tc.request()).Elem())
			for name := range schema.Properties {
				if !tags[name] {
					t.Errorf("the schema declares %q, which the Go request has no field for", name)
				}
			}
			for name := range tags {
				if _, ok := schema.Properties[name]; !ok {
					t.Errorf("the Go request carries %q, which the schema does not declare", name)
				}
			}
		})
	}
}

func jsonTagsOf(t reflect.Type) map[string]bool {
	tags := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous {
			for name := range jsonTagsOf(field.Type) {
				tags[name] = true
			}
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			tags[name] = true
		}
	}
	return tags
}

// A bare receipt id bound to parent_result_id is expanded into the object the
// strict decode accepts.
func TestBareReceiptExpansionOutputPassesTheStrictDecode(t *testing.T) {
	raw := []byte(`{"question":"q","parent_result_id":"result_1","prior_subject_receipts":["receipt_1"]}`)
	expanded, summary, err := expandBareReceiptIDs(raw, toolInvestigateQuestion)
	if err != nil || summary.Bare != 1 {
		t.Fatalf("expandBareReceiptIDs: summary %+v, err %v", summary, err)
	}
	var input contractsv1.MCPInvestigateQuestionRequest
	if err := decodeToolArguments(expanded, &input, false); err != nil {
		t.Fatalf("expanded arguments refused: %v", err)
	}
	if len(input.PriorSubjectReceipts) != 1 || input.PriorSubjectReceipts[0].ReceiptID != "receipt_1" || input.PriorSubjectReceipts[0].ResultID != "result_1" {
		t.Fatalf("receipts = %+v", input.PriorSubjectReceipts)
	}
}
