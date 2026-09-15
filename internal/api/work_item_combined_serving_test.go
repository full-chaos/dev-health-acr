package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Fresh terminal shape does not establish current authorization for a later
// stored read. Exercise both current grant states over the actual producer's
// immutable carrier, including whatever candidate identities it retained.
func assertFreshTerminalStoredAuthorization(t *testing.T, f *freshTupleProducerFixture, result contextfabric.InvestigationResult) {
	t.Helper()
	stored, err := f.store.Get(context.Background(), f.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	before := portableStoredCarrierBytes(t, stored)
	phases, resolves, discovers := len(f.client.phases), f.graph.resolve, f.graph.discover
	syntheses := 0
	f.model.observe = func(contextfabric.SynthesisInput) { syntheses++ }
	for _, universal := range []bool{false, true} {
		token := f.token
		if universal {
			token = storedServingCredential(t, f.app, []string{"*"})
		}
		request := investigationResultRequest(t, token, result.ResultID)
		recorder := httptest.NewRecorder()
		f.app.InstrumentedHandler(f.app.Handler()).ServeHTTP(recorder, request)
		called, body := callStoredServingMCP(t, f.app, token, result.ResultID)
		if !universal {
			if recorder.Code != http.StatusNotFound || !called.IsError {
				t.Errorf("unproven stored authorization: HTTP%d MCPerror=%t", recorder.Code, called.IsError)
			}
			for _, denied := range [][]byte{recorder.Body.Bytes(), body} {
				assertStoredServingContentAbsent(t, denied, result)
				for _, candidate := range result.SubjectResolution.Candidates {
					values := append([]string{candidate.Subject.Label, candidate.Subject.CanonicalID}, candidate.EvidenceRefIDs...)
					for _, value := range values {
						if value != "" && bytes.Contains(denied, []byte(value)) {
							t.Error("stored denial exposed candidate identity or evidence")
						}
					}
				}
			}
			continue
		}
		if recorder.Code != http.StatusOK || called.IsError {
			t.Fatalf("current universal grant failed: HTTP%d MCPerror=%t body=%s", recorder.Code, called.IsError, body)
		}
		var response v1.MCPInvestigationResultResponse
		if err := json.Unmarshal(mustRawJSON(t, called.StructuredContent), &response); err != nil {
			t.Fatal(err)
		}
		if err := response.Validate(); err != nil {
			t.Fatal(err)
		}
		var apiResult contextfabric.InvestigationResult
		if err := json.Unmarshal(recorder.Body.Bytes(), &apiResult); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(apiResult, response.Structured) {
			t.Error("authorized stored API/MCP results differ")
		}
		if response.Structured.Status != result.Status || !reflect.DeepEqual(response.Structured.SubjectResolution.Candidates, result.SubjectResolution.Candidates) || !reflect.DeepEqual(response.Structured.SubjectResolution.Committed, result.SubjectResolution.Committed) || !reflect.DeepEqual(response.Structured.ClaimedFacts, result.ClaimedFacts) || !reflect.DeepEqual(response.Structured.EvidenceRefIDs, result.EvidenceRefIDs) {
			t.Error("stored serving changed the producer's terminal content")
		}
		for _, detail := range response.Structured.Coverage.Details {
			if detail.Kind == v1.ContextFabricSubjectWorkItem && detail.Code == v1.ContextFabricCoverageDetailKindCensusTruncated {
				t.Error("pre-membership terminal gained a census floor")
			}
		}
	}
	after, err := f.store.Get(context.Background(), f.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	assertPortableStoredCarrierUnchanged(t, "terminal current authorization", before, after)
	if len(f.client.phases) != phases || f.graph.resolve != resolves || f.graph.discover != discovers || syntheses != 0 {
		t.Error("stored authorization started fresh investigation work")
	}
	assertResponseOwnerGateFree(t, f.gate)
}
