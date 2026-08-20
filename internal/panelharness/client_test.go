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
		_ = json.NewEncoder(w).Encode(contractsv1.ContextFabricInvestigationResult{
			SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
			ResultID:      "result_test0001", RequestID: gotBody.RequestID, Status: contractsv1.ContextFabricInvestigationComplete,
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-bearer-token", time.Second*5)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.Investigate(context.Background(), "request_test0001", validRequest())
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}

	if want := "Bearer test-bearer-token"; gotAuth != want {
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

	client, err := NewClient(server.URL, "test-bearer-token", time.Second*5)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Investigate(context.Background(), "request_test0001", validRequest())
	if err == nil {
		t.Fatal("expected an error for a 401 response, got nil")
	}
}

func TestNewClient_RequiresHTTPSForNonLoopbackHosts(t *testing.T) {
	if _, err := NewClient("http://acr.example.com", "token", time.Second); err == nil {
		t.Error("expected plain HTTP to a non-loopback host to be rejected")
	}
	if _, err := NewClient("http://127.0.0.1:8080", "token", time.Second); err != nil {
		t.Errorf("expected plain HTTP to a loopback host to be accepted, got error: %v", err)
	}
	if _, err := NewClient("https://acr.example.com", "token", time.Second); err != nil {
		t.Errorf("expected https to a non-loopback host to be accepted, got error: %v", err)
	}
}

func TestNewClient_RejectsBlankBearerToken(t *testing.T) {
	if _, err := NewClient("https://acr.example.com", "   ", time.Second); err == nil {
		t.Error("expected a blank bearer token to be rejected")
	}
}
