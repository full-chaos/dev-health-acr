package sidecar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The two hosted codes the tasks callers meet decode as themselves, not as a
// non-conforming response.
func TestDecodeAPIErrorKnowsStreamUnavailableAndSubmissionInProgress(t *testing.T) {
	for _, tc := range []struct {
		status   int
		code     string
		sentinel error
	}{
		{http.StatusNotAcceptable, contractsv1.ErrorCodeStreamUnavailable, ErrStreamUnavailable},
		{http.StatusConflict, contractsv1.ErrorCodeSubmissionInProgress, ErrSubmissionInProgress},
	} {
		body := []byte(`{"schema_version":"error.v1","request_id":"req_00000000000000000000000000014307","error":{"code":"` + tc.code + `","message":"refused","http_status":` + itoa(tc.status) + `,"retryable":false}}`)
		err := decodeAPIError(tc.status, "req_00000000000000000000000000014307", "", body)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code != tc.code || !errors.Is(err, tc.sentinel) {
			t.Fatalf("%s: decoded %#v, want APIError code %s wrapping its sentinel", tc.code, err, tc.code)
		}
	}
}

func itoa(n int) string {
	return map[int]string{http.StatusNotAcceptable: "406", http.StatusConflict: "409"}[n]
}

// The synchronous create can meet neither code: it asks for JSON, names no
// submission and sends no Prefer.
func TestInvestigateSendsNeitherStreamNorPreferNorSubmissionHeaders(t *testing.T) {
	var mu sync.Mutex
	var seen http.Header
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		writeRaw(w, http.StatusInternalServerError, `{}`)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = client.InvestigateWithSynthesisInput(context.Background(), contractsv1.ContextFabricInvestigationRequest{
		Question:    "What is blocking the payments project?",
		TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		Options: contractsv1.ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10,
			MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
	})
	mu.Lock()
	defer mu.Unlock()
	if seen == nil {
		t.Fatal("no hosted call")
	}
	if got := seen.Values("Accept"); len(got) != 1 || got[0] != "application/json" {
		t.Fatalf("Accept %v, want only application/json", got)
	}
	if seen.Get(contractsv1.ContextFabricPreferHeader) != "" || seen.Get(contractsv1.ContextFabricSubmissionIDHeader) != "" {
		t.Fatalf("synchronous create sent Prefer %q / submission id %q", seen.Get(contractsv1.ContextFabricPreferHeader), seen.Get(contractsv1.ContextFabricSubmissionIDHeader))
	}
}
