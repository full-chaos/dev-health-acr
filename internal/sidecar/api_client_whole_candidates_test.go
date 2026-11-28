package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func wholeCandidatesClientTestResult() contractsv1.ContextFabricInvestigationResult {
	result := validSidecarInvestigationResult()
	result.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{
		{ReceiptID: "subr_whole_alpha", Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_alpha", Label: "Alpha"}, State: contractsv1.ContextFabricResolutionAmbiguous, Confidence: 0.8, MatchReasons: []string{"label match"}},
		{ReceiptID: "subr_whole_beta", Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_beta", Label: "Beta"}, State: contractsv1.ContextFabricResolutionAmbiguous, Confidence: 0.8, MatchReasons: []string{"label match"}},
	}
	return result
}

func clientForResponseWithHeader(t *testing.T, body any, header, value string) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Error(err)
		}
		if header != "" {
			w.Header().Set(header, value)
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

func TestInvestigateReadsTheSubjectCandidatesCompleteHeader(t *testing.T) {
	result := wholeCandidatesClientTestResult()
	want := []contractsv1.ContextFabricSubjectRef{result.SubjectResolution.Candidates[0].Subject, result.SubjectResolution.Candidates[1].Subject}
	cases := []struct {
		name, header, value string
		whole               bool
	}{
		{"header true", contractsv1.ContextFabricSubjectCandidatesCompleteHeader, contractsv1.ContextFabricSubjectCandidatesCompleteValue, true},
		{"no header", "", "", false},
		{"header with another value", contractsv1.ContextFabricSubjectCandidatesCompleteHeader, "1", false},
		{"header in another case", contractsv1.ContextFabricSubjectCandidatesCompleteHeader, "TRUE", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := clientForResponseWithHeader(t, result, tc.header, tc.value)
			response, _, err := client.InvestigateWithSynthesisInput(context.Background(), investigationRequestForClientTest())
			if err != nil {
				t.Fatal(err)
			}
			got := response.SubjectResolution.WholeCandidates
			if !tc.whole {
				if got != nil {
					t.Fatalf("WholeCandidates = %#v, want nil", got)
				}
				return
			}
			if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
				t.Fatalf("WholeCandidates = %#v, want %#v", got, want)
			}
		})
	}
}

func TestInvestigationResultByIDNeverReadsTheSubjectCandidatesCompleteHeader(t *testing.T) {
	client := clientForResponseWithHeader(t, wholeCandidatesClientTestResult(), contractsv1.ContextFabricSubjectCandidatesCompleteHeader, contractsv1.ContextFabricSubjectCandidatesCompleteValue)
	result, err := client.InvestigationResult(context.Background(), "result_12345678")
	if err != nil {
		t.Fatal(err)
	}
	if result.SubjectResolution.WholeCandidates != nil {
		t.Fatalf("a stored result read by id carries WholeCandidates %#v, want nil", result.SubjectResolution.WholeCandidates)
	}
}
