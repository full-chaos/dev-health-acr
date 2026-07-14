package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type readinessDocument struct {
	Status string `json:"status"`
	Checks []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"checks"`
}

func (r readinessDocument) checkStatus(name string) string {
	for _, check := range r.Checks {
		if check.Name == name {
			return check.Status
		}
	}
	return ""
}

type hostedAPI struct {
	client  *http.Client
	baseURL string
	token   string
}

type hostedAPIRequest struct {
	method       string
	path         string
	requestBody  any
	responseBody any
	statuses     []int
}

func (a hostedAPI) readiness(t *testing.T) readinessDocument {
	t.Helper()
	var response readinessDocument
	a.requestJSON(t, hostedAPIRequest{method: http.MethodGet, path: "/readyz", responseBody: &response, statuses: []int{http.StatusOK, http.StatusServiceUnavailable}})
	return response
}

func (a hostedAPI) capabilities(t *testing.T) contractsv1.Capabilities {
	t.Helper()
	var response contractsv1.Capabilities
	a.requestJSON(t, hostedAPIRequest{method: http.MethodGet, path: "/api/v1/agent-context/capabilities", responseBody: &response, statuses: []int{http.StatusOK}})
	return response
}

func (a hostedAPI) contextPacket(t *testing.T) contractsv1.ContextPacket {
	t.Helper()
	request := contractsv1.ContextPacketRequest{
		SchemaVersion: contractsv1.ContextPacketRequestSchema, RequestID: "caller-request-id", Goal: "Investigate seeded CI failure",
		Repository: contractsv1.RepositoryRef{Slug: hostedIntegrationRepository}, Scope: contractsv1.RequestedScope{Branch: "main"},
		Options: contractsv1.PacketOptions{MaxItems: 10, MaxOutputTokens: 500, MaxSerializedBytes: 8192},
		Client:  contractsv1.ClientInfo{Name: "integration", Version: "1.0.0", SidecarVersion: "0.1.0"},
	}
	var response contractsv1.ContextPacket
	a.requestJSON(t, hostedAPIRequest{method: http.MethodPost, path: "/api/v1/agent-context/context-packets", requestBody: request, responseBody: &response, statuses: []int{http.StatusOK}})
	return response
}

func (a hostedAPI) evidence(t *testing.T, evidenceID string) contractsv1.ExpandedEvidence {
	t.Helper()
	var response contractsv1.ExpandedEvidence
	a.requestJSON(t, hostedAPIRequest{method: http.MethodGet, path: "/api/v1/agent-context/evidence/" + evidenceID, responseBody: &response, statuses: []int{http.StatusOK}})
	return response
}

func (a hostedAPI) requestJSON(t *testing.T, spec hostedAPIRequest) {
	t.Helper()
	var body io.Reader
	if spec.requestBody != nil {
		encoded, err := json.Marshal(spec.requestBody)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(spec.method, a.baseURL+spec.path, body)
	if err != nil {
		t.Fatal(err)
	}
	if a.token != "" && spec.path != "/readyz" {
		request.Header.Set("Authorization", "Bearer "+a.token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if slices.Contains(spec.statuses, response.StatusCode) {
		if err := json.NewDecoder(response.Body).Decode(spec.responseBody); err != nil {
			t.Fatal(err)
		}
		return
	}
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("%s %s status = %d, want %v: %s", spec.method, spec.path, response.StatusCode, spec.statuses, contents)
}
