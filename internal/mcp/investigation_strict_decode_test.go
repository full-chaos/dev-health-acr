package mcp

import (
	"encoding/json"
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
	if err := decodeInvestigationArguments([]byte(`{"question":"q"} {"question":"r"}`), &input); err == nil {
		t.Fatal("trailing JSON value accepted, want a refusal")
	}
	long := strings.Repeat("k", 200)
	err := decodeInvestigationArguments([]byte(`{"question":"q","`+long+`":1}`), &input)
	if err == nil {
		t.Fatal("unknown key accepted")
	}
	message := investigationArgumentsMessage(toolInvestigateQuestion, err)
	if strings.Contains(message, strings.Repeat("k", 65)) || !strings.Contains(message, strings.Repeat("k", 64)) {
		t.Errorf("message %q does not bound the named key to 64 characters", message)
	}
}

func TestDecodeInvestigationArgumentsRefusesAClosingDelimiterAfterTheValue(t *testing.T) {
	for _, tail := range []string{"]", "}", ")", ","} {
		var input contractsv1.MCPInvestigateQuestionRequest
		if err := decodeInvestigationArguments([]byte(`{"question":"q"}`+tail), &input); err == nil {
			t.Errorf("decode accepted %q after the value", tail)
		}
	}
	var input contractsv1.MCPInvestigateQuestionRequest
	if err := decodeInvestigationArguments([]byte(" {\"question\":\"q\"}\n"), &input); err != nil {
		t.Errorf("surrounding whitespace refused: %v", err)
	}
}

// Every property a request schema declares has a field on the Go request, and
// every field has a property, so the strict decode can neither refuse a key the
// schema allows nor allow one the schema forbids.
func TestInvestigationRequestSchemasMatchTheGoRequestFields(t *testing.T) {
	cases := map[string]any{
		investigateQuestionRequestSchemaFile:           contractsv1.MCPInvestigateQuestionRequest{},
		investigateWithInterpretationRequestSchemaFile: contractsv1.MCPInvestigateWithInterpretationRequest{},
	}
	for file, request := range cases {
		raw, err := schemaFiles.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil || len(schema.Properties) == 0 {
			t.Fatalf("%s: properties unreadable (%v)", file, err)
		}
		tags := jsonTagsOf(reflect.TypeOf(request))
		for name := range schema.Properties {
			if !tags[name] {
				t.Errorf("%s declares %q, which the Go request has no field for", file, name)
			}
		}
		for name := range tags {
			if _, ok := schema.Properties[name]; !ok {
				t.Errorf("%s: the Go request carries %q, which the schema does not declare", file, name)
			}
		}
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
	if err := decodeInvestigationArguments(expanded, &input); err != nil {
		t.Fatalf("expanded arguments refused: %v", err)
	}
	if len(input.PriorSubjectReceipts) != 1 || input.PriorSubjectReceipts[0].ReceiptID != "receipt_1" || input.PriorSubjectReceipts[0].ResultID != "result_1" {
		t.Fatalf("receipts = %+v", input.PriorSubjectReceipts)
	}
}
