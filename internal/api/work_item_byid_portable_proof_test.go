package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// portableStoredTupleFixture deliberately uses only the public stored-result
// carrier and a raw semantic-state document. It stays compilable at the b38
// pre-feature checkpoint, where the census/helper types do not exist.
func portableStoredTupleFixture(t *testing.T, mode string) contextfabric.StoredInvestigationResult {
	t.Helper()
	result := validContextFabricInvestigationResult()
	result.ResultID = "result_portable_tuple_01"
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilyScopedCohortStatus,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceModel,
		FamilyVersion: contextfabric.QuestionFamilyTableVersion,
		MemberKind:    contractsv1.ContextFabricSubjectWorkItem,
	}
	count := int64(7)
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{{
		ClaimID: "portable_count_01", Kind: contractsv1.ContextFabricFactCardinality,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectOrganization, CanonicalID: callerOrgID, Label: callerOrgID},
		Field:   "work_item_count", Value: contractsv1.ContextFabricScalarValue{Integer: &count},
	}}
	result.Limitations = []string{}
	if mode != "complete" {
		result.Coverage.Partial = true
	}
	if mode == "partial" {
		result.Coverage.DegradedReasons = []string{"repository source unavailable"}
	}
	if mode == "degraded" {
		result.Limitations = []string{"provider returned a stale status"}
	}
	declared, served := 2000, 1
	detail := contractsv1.ContextFabricCoverageDetail{
		DetailID: "cov-01", Source: "context-fabric:graph",
		Code:      contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
		Degrading: true, Kind: contractsv1.ContextFabricSubjectWorkItem,
		Declared: &declared, Served: &served,
		Raw: "kind_census_truncated:work_item:2000:1",
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	result.Coverage.Details = []contractsv1.ContextFabricCoverageDetail{detail}
	result.Coverage.DegradedReasons = append(result.Coverage.DegradedReasons, detail.Raw)

	// This is a deliberately partial raw document. The by-ID classifier reads
	// only its tuple discriminators; the persistence codec is not part of this
	// portable route proof.
	raw := `{"family":"scoped_cohort_status","frame_present":true,"frame":{"subject_expression":{"kind":"children_of_scope","scoped":{"member_kind":"work_item"}}},"scope_anchor":{"kind":"project","term":"project_ask_dev"}}`
	var state contextfabric.PersistedSemanticState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("decode raw semantic state: %v", err)
	}
	return contextfabric.StoredInvestigationResult{
		Result: result, SemanticState: &state,
		SemanticStateRead: contextfabric.SemanticStateReadAvailable,
	}
}

type portableStoredTupleStore struct {
	contextfabric.InvestigationResultStore
	stored contextfabric.StoredInvestigationResult
}

func (s *portableStoredTupleStore) Get(context.Context, storage.Principal, string) (contextfabric.StoredInvestigationResult, error) {
	return s.stored, nil
}

func portableWorkItemDetail(details []contractsv1.ContextFabricCoverageDetail) *contractsv1.ContextFabricCoverageDetail {
	for index := range details {
		if details[index].Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && details[index].Kind == contractsv1.ContextFabricSubjectWorkItem {
			return &details[index]
		}
	}
	return nil
}

func portableHasMembershipLimitation(limitations []string) bool {
	for _, limitation := range limitations {
		if limitation == contextfabric.WorkItemMembershipLimitation() {
			return true
		}
	}
	return false
}

func portableStoredCarrierBytes(t *testing.T, stored contextfabric.StoredInvestigationResult) []byte {
	t.Helper()
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("encode stored carrier snapshot: %v", err)
	}
	return encoded
}

func assertPortableStoredCarrierUnchanged(t *testing.T, surface string, before []byte, stored contextfabric.StoredInvestigationResult) {
	t.Helper()
	after := portableStoredCarrierBytes(t, stored)
	if string(before) != string(after) {
		t.Errorf("%s mutated the stored carrier bytes", surface)
	}
}

// TestPortableStoredWorkItemFallbackAndMCPParity is the baseline red/green
// proof for the by-ID fallback. Its API and MCP subtests run independently so
// a failure on one surface does not prevent measurement of the other. Each
// surface uses three stored coverage states so a complete, partial, and
// already-degraded row all disclose the same bounded limitation when the
// semantic reading has no usable census.
func TestPortableStoredWorkItemFallbackAndMCPParity(t *testing.T) {
	t.Run("api", testPortableStoredWorkItemFallbackAPI)
	t.Run("mcp", testPortableStoredWorkItemFallbackMCPParity)
}

func assertPortableStoredFallback(t *testing.T, surface string, got contractsv1.ContextFabricInvestigationResult) {
	t.Helper()
	if portableWorkItemDetail(got.Coverage.Details) != nil {
		t.Errorf("%s fallback carried a work-item census detail: %#v", surface, got.Coverage.Details)
	}
	if !portableHasMembershipLimitation(got.Limitations) {
		t.Errorf("%s fallback omitted membership limitation: %#v", surface, got.Limitations)
	}
	if got.SemanticReading != nil {
		t.Errorf("%s fallback invented semantic_reading: %#v", surface, got.SemanticReading)
	}
	if len(got.ClaimedFacts) != 1 || got.ClaimedFacts[0].Value.Integer == nil || *got.ClaimedFacts[0].Value.Integer != 7 {
		t.Errorf("%s fallback changed cardinality claim: %#v", surface, got.ClaimedFacts)
	}
}

func testPortableStoredWorkItemFallbackAPI(t *testing.T) {
	for _, mode := range []string{"complete", "partial", "degraded"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			stored := portableStoredTupleFixture(t, mode)
			store := &portableStoredTupleStore{stored: stored}
			before := portableStoredCarrierBytes(t, store.stored)
			app, _ := newContextFabricTestAppWithResults(t, nil, store)
			token := storedServingCredential(t, app, []string{"*"})

			canonical := httptest.NewRecorder()
			app.Handler().ServeHTTP(canonical, portableWorkItemDetailRequest(t, token, stored.Result.ResultID))
			if canonical.Code != http.StatusOK {
				t.Fatalf("canonical status = %d, want 200 (body %s)", canonical.Code, canonical.Body.String())
			}
			var got contractsv1.ContextFabricInvestigationResult
			if err := json.Unmarshal(canonical.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode canonical result: %v", err)
			}
			assertPortableStoredFallback(t, "canonical API", got)
			assertPortableStoredCarrierUnchanged(t, "canonical API", before, store.stored)

			projectionRequest := portableWorkItemDetailRequest(t, token, stored.Result.ResultID)
			projectionRequest.URL.RawQuery = "view=projection&max_drivers=3&max_cohort_members=3&max_evidence_refs=3"
			projection := httptest.NewRecorder()
			app.Handler().ServeHTTP(projection, projectionRequest)
			if projection.Code != http.StatusOK {
				t.Fatalf("projection status = %d, want 200 (body %s)", projection.Code, projection.Body.String())
			}
			assertPortableStoredCarrierUnchanged(t, "projection API", before, store.stored)

		})
	}
}

func testPortableStoredWorkItemFallbackMCPParity(t *testing.T) {
	for _, mode := range []string{"complete", "partial", "degraded"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			stored := portableStoredTupleFixture(t, mode)
			store := &portableStoredTupleStore{stored: stored}
			before := portableStoredCarrierBytes(t, store.stored)
			hostedApp, _ := newParityHostedApp(t, nil, store)
			hostedToken := storedServingCredential(t, hostedApp, []string{"*"})
			server := httptest.NewTLSServer(hostedApp.Handler())
			t.Cleanup(server.Close)
			configureSidecarEnvironment(t, server, hostedToken)
			boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
			if err != nil {
				t.Fatalf("MCP bootstrap: %v", err)
			}
			apiResult := getRealAPIResult(t, server, hostedToken, stored.Result.ResultID)
			assertPortableStoredCarrierUnchanged(t, "hosted API", before, store.stored)
			mcpResult := callRealMCPInvestigationResult(t, boot, stored.Result.ResultID)
			assertPortableStoredCarrierUnchanged(t, "MCP", before, store.stored)
			if err := apiResult.Validate(); err != nil {
				t.Errorf("API fallback result failed validation: %v", err)
			}
			if err := mcpResult.Validate(); err != nil {
				t.Errorf("MCP fallback result failed validation: %v", err)
			}
			assertPortableStoredFallback(t, "hosted API", apiResult)
			assertPortableStoredFallback(t, "MCP", mcpResult)
			apiJSON, err := json.Marshal(apiResult)
			if err != nil {
				t.Fatal(err)
			}
			mcpJSON, err := json.Marshal(mcpResult)
			if err != nil {
				t.Fatal(err)
			}
			if string(apiJSON) != string(mcpJSON) {
				t.Errorf("API/MCP fallback diverged:\nAPI=%s\nMCP=%s", apiJSON, mcpJSON)
			}
		})
	}
}

func portableWorkItemDetailRequest(t *testing.T, token, resultID string) *http.Request {
	t.Helper()
	request := investigationResultRequest(t, token, resultID)
	request.Header.Set("X-ACR-Client-Version", "1.2.5")
	return request
}
