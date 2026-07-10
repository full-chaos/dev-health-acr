package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	newMux().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}

func TestCapabilitiesShape(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	response := httptest.NewRecorder()
	newMux().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	var capabilities contractsv1.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.SchemaVersion != contractsv1.CapabilitiesSchema {
		t.Fatalf("unexpected schema: %s", capabilities.SchemaVersion)
	}
}
