package sidecar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func synthesisInputForClientTest() *contractsv1.ContextFabricSynthesisInput {
	input := json.RawMessage(`{"facts":[]}`)
	sum := sha256.Sum256(input)
	return &contractsv1.ContextFabricSynthesisInput{
		Contract: contractsv1.ContextFabricSynthesisContract{
			ModelOutputVersion: "context-fabric-model-output.v8", PromptVersion: "context-fabric-synthesis.v3", SystemSHA256: strings.Repeat("b", 64),
		},
		Input: input, InputSHA256: hex.EncodeToString(sum[:]), Rules: []string{"Use only the facts in this input."},
	}
}

func investigationRequestForClientTest() contractsv1.ContextFabricInvestigationRequest {
	return contractsv1.ContextFabricInvestigationRequest{
		Question:    "What is blocking the payments project?",
		TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		Options: contractsv1.ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10,
			MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
	}
}

func clientForResponse(t *testing.T, body any) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Error(err)
		}
		writeRaw(w, http.StatusOK, string(encoded))
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestInvestigateWithSynthesisInputHandsBackTheValidatedInput(t *testing.T) {
	bundle := synthesisInputForClientTest()
	client := clientForResponse(t, contractsv1.ContextFabricInvestigationResponse{ContextFabricInvestigationResult: validSidecarInvestigationResult(), SynthesisInput: bundle})
	response, requestID, err := client.InvestigateWithSynthesisInput(context.Background(), investigationRequestForClientTest())
	if err != nil {
		t.Fatal(err)
	}
	if requestID == "" || response.SynthesisInput == nil || string(response.SynthesisInput.Input) != string(bundle.Input) {
		t.Fatalf("response carries synthesis input %#v, want the hosted one", response.SynthesisInput)
	}
	result, _, err := client.InvestigateWithRequestID(context.Background(), investigationRequestForClientTest())
	if err != nil || result.ResultID != response.ResultID {
		t.Fatalf("the result-only method = %q, %v; want the same result", result.ResultID, err)
	}
}

func TestInvestigateWithSynthesisInputRefusesAnInputThatFailsValidation(t *testing.T) {
	bundle := synthesisInputForClientTest()
	bundle.InputSHA256 = strings.Repeat("0", 64)
	client := clientForResponse(t, contractsv1.ContextFabricInvestigationResponse{ContextFabricInvestigationResult: validSidecarInvestigationResult(), SynthesisInput: bundle})
	if _, _, err := client.InvestigateWithSynthesisInput(context.Background(), investigationRequestForClientTest()); err == nil {
		t.Fatal("a synthesis input with a wrong input_sha256 was handed to the caller")
	}
}

func TestInvestigateWithSynthesisInputReturnsNoInputForAPlainResult(t *testing.T) {
	client := clientForResponse(t, validSidecarInvestigationResult())
	response, _, err := client.InvestigateWithSynthesisInput(context.Background(), investigationRequestForClientTest())
	if err != nil {
		t.Fatal(err)
	}
	if response.SynthesisInput != nil {
		t.Fatal("a plain result produced a synthesis input")
	}
}
