package panelharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func validRequest() contractsv1.ContextFabricInvestigationRequest {
	return contractsv1.ContextFabricInvestigationRequest{
		Question:    "Was Ask Dev ready to ship?",
		TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		Options: contractsv1.ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144,
			AllowClarification: true,
		},
	}
}

func TestClient_InvestigateStampsHonestConsumerSurfaceAndBearerHeader(t *testing.T) {
	var gotAuth string
	var gotBody contractsv1.ContextFabricInvestigationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(minimalValidResult("result_test0001", gotBody.RequestID))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testBearerToken(1), time.Second*5)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.Investigate(context.Background(), "request_test0001", validRequest())
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}

	if want := "Bearer " + testBearerToken(1); gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
	if gotBody.Consumer.Surface != contextFabricConsumerSurface {
		t.Errorf("Consumer.Surface = %q, want the honest panel-harness surface %q (never a caller-supplied or spoofed value)", gotBody.Consumer.Surface, contextFabricConsumerSurface)
	}
	if gotBody.Consumer.Name != consumerName {
		t.Errorf("Consumer.Name = %q, want %q", gotBody.Consumer.Name, consumerName)
	}
	if gotBody.SchemaVersion != contractsv1.ContextFabricInvestigationRequestSchema {
		t.Errorf("SchemaVersion = %q, want %q", gotBody.SchemaVersion, contractsv1.ContextFabricInvestigationRequestSchema)
	}
	if result.ResultID != "result_test0001" {
		t.Errorf("ResultID = %q, want %q", result.ResultID, "result_test0001")
	}
}

func TestClient_InvestigateSurfacesNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"invalid_token"}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testBearerToken(2), time.Second*5)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Investigate(context.Background(), "request_test0001", validRequest())
	if err == nil {
		t.Fatal("expected an error for a 401 response, got nil")
	}
}

func TestClient_InvestigateRejectsAMalformedSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testBearerToken(3), time.Second*5)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Investigate(context.Background(), "request_test0001", validRequest()); err == nil {
		t.Fatal("expected a well-formed-but-empty 200 {} response to be rejected by ValidateStored, got nil error")
	}
}

func TestNewClient_RequiresHTTPSForNonLoopbackHosts(t *testing.T) {
	if _, err := NewClient("http://acr.example.com", testBearerToken(4), time.Second); err == nil {
		t.Error("expected plain HTTP to a non-loopback host to be rejected")
	}
	if _, err := NewClient("http://127.0.0.1:8080", testBearerToken(5), time.Second); err != nil {
		t.Errorf("expected plain HTTP to a loopback host to be accepted, got error: %v", err)
	}
	if _, err := NewClient("https://acr.example.com", testBearerToken(6), time.Second); err != nil {
		t.Errorf("expected https to a non-loopback host to be accepted, got error: %v", err)
	}
}

func TestNewClient_RejectsBlankBearerToken(t *testing.T) {
	if _, err := NewClient("https://acr.example.com", "   ", time.Second); err == nil {
		t.Error("expected a blank bearer token to be rejected")
	}
}

func TestNewClient_RejectsTokenWithoutTheExpectedShape(t *testing.T) {
	tests := []string{"plain-secret-value", "license_1234567890", "fcacr_not-valid-base64!!!"}
	for _, token := range tests {
		if _, err := NewClient("https://acr.example.com", token, time.Second); err == nil {
			t.Errorf("expected token %q (not a real fcacr_ credential shape) to be rejected", token)
		}
	}
}

func TestNewClient_RejectsBaseURLWithPathQueryFragmentOrUserinfo(t *testing.T) {
	tests := []string{
		"https://acr.example.com/some/path",
		"https://acr.example.com?leaked=secret",
		"https://acr.example.com#fragment",
		"https://user:pass@acr.example.com",
	}
	for _, baseURL := range tests {
		if _, err := NewClient(baseURL, testBearerToken(7), time.Second); err == nil {
			t.Errorf("expected base URL %q (not a bare origin) to be rejected", baseURL)
		}
	}
}
