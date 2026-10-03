package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const interpretationSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func withInterpretationTool(boot *Bootstrap) *Bootstrap {
	boot.Capabilities.EnabledTools = append(slices.Clone(boot.Capabilities.EnabledTools), toolInvestigateWithInterpretation)
	return boot
}

func invokeInvestigateWithInterpretation(boot *Bootstrap, arguments []byte) (*mcpsdk.CallToolResult, error) {
	cfg, callerCtx := callerContextFor(context.Background(), boot)
	return handleInvestigateWithInterpretation(callerCtx, cfg, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: arguments}})
}

func interpretationArgs(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	args := map[string]any{
		"question":       "Which teams need attention?",
		"interpretation": json.RawMessage(`{"shape":"cohort"}`),
		"contract": map[string]any{
			"model_output_version": "out-v1",
			"prompt_version":       "prompt-v1",
			"system_sha256":        interpretationSHA,
		},
		"client_model": "vendor/model-1:latest",
	}
	if mutate != nil {
		mutate(args)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestInvestigateWithInterpretationForwardsTheSuppliedInterpretation(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := withInterpretationTool(answerFixtureBootstrap(t, parityResult(), &seen))

	result, err := invokeInvestigateWithInterpretation(boot, interpretationArgs(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", toolResultText(result))
	}
	supplied := seen.SuppliedInterpretation
	if supplied == nil {
		t.Fatal("the hosted request carried no supplied_interpretation")
	}
	want := contractsv1.ContextFabricSuppliedInterpretation{
		Output:             json.RawMessage(`{"shape":"cohort"}`),
		ModelOutputVersion: "out-v1",
		PromptVersion:      "prompt-v1",
		SystemSHA256:       interpretationSHA,
		ClientModel:        "vendor/model-1:latest",
	}
	if string(supplied.Output) != string(want.Output) {
		t.Errorf("output = %s, want %s", supplied.Output, want.Output)
	}
	supplied.Output, want.Output = nil, nil
	if !reflect.DeepEqual(*supplied, want) {
		t.Errorf("supplied interpretation = %#v, want %#v", *supplied, want)
	}
}

func TestInvestigateWithInterpretationOmitsTheOptionalClientModel(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := withInterpretationTool(answerFixtureBootstrap(t, parityResult(), &seen))
	args := interpretationArgs(t, func(m map[string]any) { delete(m, "client_model") })
	result, err := invokeInvestigateWithInterpretation(boot, args)
	if err != nil || result.IsError {
		t.Fatalf("err %v result %s", err, toolResultText(result))
	}
	if seen.SuppliedInterpretation == nil || seen.SuppliedInterpretation.SystemSHA256 != interpretationSHA || seen.SuppliedInterpretation.ClientModel != "" {
		t.Fatalf("supplied interpretation = %#v, want the whole contract and no client model", seen.SuppliedInterpretation)
	}
}

func fullInvestigationInput() contractsv1.MCPInvestigateQuestionRequest {
	allow := false
	return contractsv1.MCPInvestigateQuestionRequest{
		Question:       "How has delivery changed for the payments project?",
		ParentResultID: "res_4d1c9a72",
		PriorWindowReceipts: []contractsv1.ContextFabricBoundSubjectReceipt{
			{ResultID: "res_4d1c9a72", ReceiptID: "winr_a1b2c3d4"},
		},
		ExpectedKinds:          []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectProject},
		EvidenceWindow:         &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D},
		WindowConfirmationMode: contractsv1.ContextFabricWindowConfirmationHeadless,
		Scope:                  &contractsv1.MCPInvestigationScope{ProjectIDs: []string{"project_payments"}},
		Budget:                 &contractsv1.MCPInvestigationBudget{MaxDrivers: 3, MaxCohortMembers: 7, MaxEvidenceRefs: 11, MaxSerializedBytes: 32768},
		AllowClarification:     &allow,
		IncludeFullResult:      true,
	}
}

func TestInvestigateWithInterpretationForwardsEveryOtherFieldLikeInvestigateQuestion(t *testing.T) {
	input := fullInvestigationInput()
	inputType := reflect.TypeOf(input)
	inputValue := reflect.ValueOf(input)
	populated := map[string]bool{}
	for i := 0; i < inputType.NumField(); i++ {
		field := inputType.Field(i)
		populated[field.Name] = !inputValue.Field(i).IsZero()
	}
	for _, name := range []string{"Question", "ParentResultID", "PriorWindowReceipts", "ExpectedKinds", "EvidenceWindow", "WindowConfirmationMode", "Scope", "Budget", "AllowClarification", "IncludeFullResult"} {
		if !populated[name] {
			t.Fatalf("the fixture input leaves %s empty, so its forwarding is not exercised", name)
		}
	}

	var viaQuestion, viaInterpretation contractsv1.ContextFabricInvestigationRequest
	questionBoot := answerFixtureBootstrap(t, parityResult(), &viaQuestion)
	args, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	questionResult, err := invokeInvestigateQuestion(context.Background(), questionBoot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: args}})
	if err != nil || questionResult.IsError {
		t.Fatalf("investigate_question: err %v result %s", err, toolResultText(questionResult))
	}

	wire := map[string]any{}
	if err := json.Unmarshal(args, &wire); err != nil {
		t.Fatal(err)
	}
	wire["interpretation"] = json.RawMessage(`{"shape":"one_subject"}`)
	wire["contract"] = map[string]any{"model_output_version": "out-v1", "prompt_version": "prompt-v1", "system_sha256": interpretationSHA}
	interpretationArgs, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	interpretationBoot := withInterpretationTool(answerFixtureBootstrap(t, parityResult(), &viaInterpretation))
	interpretationResult, err := invokeInvestigateWithInterpretation(interpretationBoot, interpretationArgs)
	if err != nil || interpretationResult.IsError {
		t.Fatalf("investigate_with_interpretation: err %v result %s", err, toolResultText(interpretationResult))
	}

	if viaInterpretation.SuppliedInterpretation == nil {
		t.Fatal("supplied_interpretation was not forwarded")
	}
	if viaQuestion.SuppliedInterpretation != nil {
		t.Fatal("investigate_question forwarded a supplied_interpretation")
	}
	viaInterpretation.SuppliedInterpretation = nil
	viaQuestion.RequestID, viaInterpretation.RequestID = "", ""
	if !reflect.DeepEqual(viaQuestion, viaInterpretation) {
		t.Errorf("the other hosted request fields differ between the tools:\nquestion:       %#v\ninterpretation: %#v", viaQuestion, viaInterpretation)
	}
	if !reflect.DeepEqual(questionResult.StructuredContent, interpretationResult.StructuredContent) {
		t.Errorf("the tools rendered different answers for the same hosted result")
	}
	if toolResultText(questionResult) != toolResultText(interpretationResult) {
		t.Errorf("the tools rendered different markdown for the same hosted result")
	}
}

func TestInvestigateWithInterpretationRefusesBadArgumentsBeforeTheHostedCall(t *testing.T) {
	big := `{"pad":"` + strings.Repeat("x", contractsv1.ContextFabricSuppliedInterpretationMaxBytes) + `"}`
	cases := map[string]func(map[string]any){
		"unknown argument":         func(m map[string]any) { m["surprise"] = true },
		"oversized interpretation": func(m map[string]any) { m["interpretation"] = json.RawMessage(big) },
		"array interpretation":     func(m map[string]any) { m["interpretation"] = json.RawMessage(`[1]`) },
		"string interpretation":    func(m map[string]any) { m["interpretation"] = "text" },
		"null interpretation":      func(m map[string]any) { m["interpretation"] = nil },
		"missing interpretation":   func(m map[string]any) { delete(m, "interpretation") },
		"missing contract":         func(m map[string]any) { delete(m, "contract") },
		"uppercase sha": func(m map[string]any) {
			m["contract"] = map[string]any{"model_output_version": "v", "prompt_version": "p", "system_sha256": strings.ToUpper(interpretationSHA)}
		},
		"short sha": func(m map[string]any) {
			m["contract"] = map[string]any{"model_output_version": "v", "prompt_version": "p", "system_sha256": "abc"}
		},
		"bad client model": func(m map[string]any) { m["client_model"] = "has space" },
		"empty version": func(m map[string]any) {
			m["contract"] = map[string]any{"model_output_version": "", "prompt_version": "p", "system_sha256": interpretationSHA}
		},
		"missing model output version": func(m map[string]any) {
			m["contract"] = map[string]any{"prompt_version": "p", "system_sha256": interpretationSHA}
		},
		"missing prompt version": func(m map[string]any) {
			m["contract"] = map[string]any{"model_output_version": "v", "system_sha256": interpretationSHA}
		},
		"missing sha": func(m map[string]any) {
			m["contract"] = map[string]any{"model_output_version": "v", "prompt_version": "p"}
		},
		"null sha": func(m map[string]any) {
			m["contract"] = map[string]any{"model_output_version": "v", "prompt_version": "p", "system_sha256": nil}
		},
		"empty sha": func(m map[string]any) {
			m["contract"] = map[string]any{"model_output_version": "v", "prompt_version": "p", "system_sha256": ""}
		},
		"unknown contract key": func(m map[string]any) {
			m["contract"] = map[string]any{"model_output_version": "v", "prompt_version": "p", "system_sha256": interpretationSHA, "extra": 1}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var seen contractsv1.ContextFabricInvestigationRequest
			boot := withInterpretationTool(answerFixtureBootstrap(t, parityResult(), &seen))
			result, err := invokeInvestigateWithInterpretation(boot, interpretationArgs(t, mutate))
			if err != nil {
				t.Fatalf("protocol error: %v", err)
			}
			if !result.IsError {
				t.Fatal("IsError = false, want a validation refusal")
			}
			if text := toolResultText(result); !strings.HasPrefix(text, "validation:") {
				t.Errorf("tool text %q is not a validation refusal", text)
			}
			if seen.Question != "" {
				t.Error("the hosted API was called for a request the tool must refuse")
			}
		})
	}
}

func TestInvestigateWithInterpretationIsListedOnlyWhenAdvertised(t *testing.T) {
	for _, advertise := range []bool{true, false} {
		boot := answerFixtureBootstrap(t, parityResult(), nil)
		if advertise {
			boot = withInterpretationTool(boot)
		}
		client, closeFn := connectedClient(t, boot)
		listed, err := client.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var found *mcpsdk.Tool
		for _, tool := range listed.Tools {
			if tool.Name == toolInvestigateWithInterpretation {
				found = tool
			}
		}
		closeFn()
		if found != nil && !advertise {
			t.Fatal("investigate_with_interpretation offered without hosted support")
		}
		if found == nil && advertise {
			t.Fatal("investigate_with_interpretation missing when hosted advertises it")
		}
		if found != nil {
			if found.Annotations == nil || !found.Annotations.ReadOnlyHint {
				t.Fatalf("must be read-only: %#v", found.Annotations)
			}
			for _, needle := range []string{"interpret_question", "prompts/get", "interpretation", "contract", "_meta", "investigate_question"} {
				if !strings.Contains(found.Description, needle) {
					t.Errorf("description does not mention %q", needle)
				}
			}
		}
	}
}

func TestInvestigateWithInterpretationSchemaRefusesANonObjectInterpretation(t *testing.T) {
	boot := withInterpretationTool(answerFixtureBootstrap(t, parityResult(), nil))
	client, closeFn := connectedClient(t, boot)
	defer closeFn()
	for _, interpretation := range []any{"text", []int{1}, 7} {
		args := map[string]any{
			"question":       "q",
			"interpretation": interpretation,
			"contract":       map[string]any{"model_output_version": "v", "prompt_version": "p"},
		}
		result, err := client.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: toolInvestigateWithInterpretation, Arguments: args})
		if err == nil && (result == nil || !result.IsError) {
			t.Errorf("interpretation %v was accepted at the schema boundary", interpretation)
		}
	}
}

func TestInvestigateWithInterpretationIsInTheRequestLineVocabulary(t *testing.T) {
	if !slices.Contains(eventspec.MCPHTTPToolVocabulary(), toolInvestigateWithInterpretation) {
		t.Fatalf("%s is not in eventspec.MCPHTTPToolVocabulary", toolInvestigateWithInterpretation)
	}
	if bucket(toolInvestigateWithInterpretation, HTTPToolVocabulary()) != toolInvestigateWithInterpretation {
		t.Fatal("the tool is logged as other")
	}
}

func contractRefusalBootstrap(t *testing.T, details map[string]any) *Bootstrap {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/context-fabric/investigations" {
			writeJSONFixture(t, w, http.StatusConflict, contractsv1.ErrorEnvelope{
				SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_0123456789abcdef",
				Error: contractsv1.ErrorDetail{
					Code: "invalid_request", Message: "hosted prose that must never be shown",
					HTTPStatus: http.StatusConflict, Details: details,
				},
			})
			return
		}
		writeErrorFixture(t, w, http.StatusNotFound, "not_found", false)
	}))
	t.Cleanup(server.Close)
	cfg := fixtureConfig(t, server)
	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xAB)))
	if err != nil {
		t.Fatal(err)
	}
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, toolInvestigateWithInterpretation)
	return &Bootstrap{Config: cfg, Client: client, Capabilities: caps}
}

func TestInvestigateWithInterpretationShowsAContractRefusalWithTheCurrentValues(t *testing.T) {
	boot := contractRefusalBootstrap(t, map[string]any{
		contractsv1.ContextFabricInterpretationContractDetailsKey: map[string]any{
			"mismatch": []string{"prompt_version", "system_sha256"},
			"current":  map[string]any{"model_output_version": "out-v2", "prompt_version": "prompt-v2", "system_sha256": interpretationSHA},
		},
	})
	result, err := invokeInvestigateWithInterpretation(boot, interpretationArgs(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("IsError = false, want the contract refusal")
	}
	text := toolResultText(result)
	for _, fragment := range []string{"validation:", "prompt_version, system_sha256", `"out-v2"`, `"prompt-v2"`, interpretationSHA, "interpret_question", "prompts/get"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("tool text %q lacks %q", text, fragment)
		}
	}
	if strings.Contains(text, "hosted prose") {
		t.Errorf("tool text %q carries hosted prose", text)
	}
}

func TestInvestigateWithInterpretationIgnoresAnInvalidContractRefusal(t *testing.T) {
	boot := contractRefusalBootstrap(t, map[string]any{
		contractsv1.ContextFabricInterpretationContractDetailsKey: map[string]any{
			"mismatch": []string{"prompt_version", "not_a_field"},
			"current":  map[string]any{"model_output_version": "out-v2", "prompt_version": "prompt-v2", "system_sha256": interpretationSHA},
		},
	})
	result, err := invokeInvestigateWithInterpretation(boot, interpretationArgs(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	text := toolResultText(result)
	if !result.IsError || !strings.Contains(text, "status=409") {
		t.Fatalf("want the generic 409 invalid_request error, got %q", text)
	}
	for _, fragment := range []string{"out-v2", "prompt-v2", "not_a_field", "prompts/get"} {
		if strings.Contains(text, fragment) {
			t.Errorf("tool text %q shows %q from an invalid refusal", text, fragment)
		}
	}
}
