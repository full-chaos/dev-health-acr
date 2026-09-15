package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// storedWorkItemTupleRouteFixture constructs the canonical package-api tuple
// carrier used by route and MCP tests. It keeps the result payload and the
// semantic reading separate, as the production store does, and binds the
// persisted census to the principal's requested repository scope.
func storedWorkItemTupleRouteFixture(t *testing.T, principal storage.Principal) contextfabric.StoredInvestigationResult {
	t.Helper()
	result := validContextFabricInvestigationResult()
	result.ResultID = "result_work_item_tuple_01"
	project := result.SubjectResolution.Committed[0]
	result.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{{
		ReceiptID: "receipt_project_01", Subject: project, State: contractsv1.ContextFabricResolutionCommitted,
		MatchReasons: []string{"exact project match"}, Confidence: 1,
	}}
	memberID, omitted, err := identity.Derive(identity.KindWorkItem, []string{"example-org/widget-service", "WI-1"}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive() = %q, omitted=%v, err=%v", memberID, omitted, err)
	}
	member := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: memberID, Label: "WI-1"}
	evidenceRef := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "example-org/widget-service:WI-1")
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilyScopedCohortStatus,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceModel,
		FamilyVersion: contextfabric.QuestionFamilyTableVersion,
		MemberKind:    contractsv1.ContextFabricSubjectWorkItem,
	}
	result.Cohort = &contractsv1.ContextFabricCohort{
		Kind: contractsv1.ContextFabricSubjectWorkItem,
		Members: []contractsv1.ContextFabricCohortMember{{
			Subject: member, Rank: 1, InclusionReasons: []string{"within project scope"}, EvidenceRefIDs: []string{evidenceRef},
		}},
		Rationale: "work items in the project scope", Complete: false, Truncated: true,
	}
	status := "open"
	title := "Implement the tuple"
	count := int64(1)
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{
		{ClaimID: "claim_status_01", Kind: contractsv1.ContextFabricFactStatus, Subject: member, Field: "status", Value: contractsv1.ContextFabricScalarValue{String: &status}},
		{ClaimID: "claim_work_01", Kind: contractsv1.ContextFabricFactWork, Subject: member, Field: "title", Value: contractsv1.ContextFabricScalarValue{String: &title}},
		{ClaimID: "claim_count_01", Kind: contractsv1.ContextFabricFactCardinality, Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectOrganization, CanonicalID: principal.OrgID, Label: principal.OrgID}, Field: "work_item_count", Value: contractsv1.ContextFabricScalarValue{Integer: &count}},
	}
	result.EvidenceRefIDs = []string{evidenceRef}
	result.EvidenceRefLabels = map[string]string{evidenceRef: "WI-1"}
	result.Coverage.Partial = true
	result.Coverage.Details = []contractsv1.ContextFabricCoverageDetail{storedWorkItemTupleRouteCensusDetail(2000, 1)}
	result.Coverage.DegradedReasons = []string{storedWorkItemTupleRouteCensusReason(2000, 1)}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	digest, err := contextfabric.WorkItemAuthorizationDigest(principal, principal.RepositoryScopes)
	if err != nil {
		t.Fatalf("WorkItemAuthorizationDigest() error = %v", err)
	}
	state := &contextfabric.PersistedSemanticState{
		Family: contextfabric.QuestionFamilyScopedCohortStatus, FramePresent: true,
		Frame: &contextfabric.QuestionFrame{SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"project"}, MemberKind: contextfabric.SubjectWorkItem},
		}},
		ScopeAnchor: contextfabric.SemanticScopeAnchor{Kind: contextfabric.SubjectProject, Term: "project"},
		WorkItemCensus: &contextfabric.WorkItemTupleCensus{
			Version: contextfabric.WorkItemTupleCensusVersion, State: contextfabric.WorkItemMembershipCensusFloor,
			Value: contextfabric.WorkItemMembershipCensusLimit, Retained: 1,
			RequestedRepositoryScope: append([]string(nil), principal.RepositoryScopes...), AuthorizationDigest: digest,
		},
	}
	return contextfabric.StoredInvestigationResult{
		Result: result, SemanticState: state, SemanticStateRead: contextfabric.SemanticStateReadAvailable,
	}
}

type storedWorkItemTupleRouteStore struct {
	contextfabric.InvestigationResultStore
	stored contextfabric.StoredInvestigationResult
}

func (s *storedWorkItemTupleRouteStore) Get(context.Context, storage.Principal, string) (contextfabric.StoredInvestigationResult, error) {
	return s.stored, nil
}

func TestContextFabricInvestigationResultRouteServesStoredWorkItemCensus(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	stored := storedWorkItemTupleRouteFixture(t, principal)
	store := &storedWorkItemTupleRouteStore{stored: stored}
	app, token := newContextFabricTestAppWithResults(t, nil, store)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.Result.ResultID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	detail := findWorkItemTupleRouteCensusDetail(got.Coverage.Details)
	if detail == nil || detail.Declared == nil || *detail.Declared != contextfabric.WorkItemMembershipCensusLimit || detail.Served == nil || *detail.Served != 1 {
		t.Fatalf("served coverage detail = %#v, want declared floor and one retained member", detail)
	}
	if containsWorkItemTupleRouteLimitation(got.Limitations) {
		t.Fatalf("available floor census unexpectedly disclosed stored fallback limitation: %#v", got.Limitations)
	}
}

func TestContextFabricInvestigationResultRouteStoresFallbackWithoutCensus(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	stored := storedWorkItemTupleRouteFixture(t, principal)
	stored.SemanticState.WorkItemCensus = nil
	original := stored
	store := &storedWorkItemTupleRouteStore{stored: stored}
	app, _ := newContextFabricTestAppWithResults(t, nil, store)
	token := storedServingCredential(t, app, []string{"*"})

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.Result.ResultID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if findWorkItemTupleRouteCensusDetail(got.Coverage.Details) != nil {
		t.Fatalf("fallback response carried a census detail: %#v", got.Coverage.Details)
	}
	if !containsWorkItemTupleRouteLimitation(got.Limitations) {
		t.Fatalf("fallback response omitted the existing membership limitation: %#v", got.Limitations)
	}
	if got.SemanticReading != nil {
		t.Fatalf("fallback response invented semantic_reading: %#v", got.SemanticReading)
	}
	if !reflect.DeepEqual(store.stored, original) {
		t.Fatal("fallback serving mutated the stored carrier")
	}
}

func TestContextFabricInvestigationResultRouteHidesWorkItemDigestMismatch(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	stored := storedWorkItemTupleRouteFixture(t, principal)
	other := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{"other-org/other-repository"}}
	digest, err := contextfabric.WorkItemAuthorizationDigest(other, stored.SemanticState.WorkItemCensus.RequestedRepositoryScope)
	if err != nil {
		t.Fatal(err)
	}
	stored.SemanticState.WorkItemCensus.AuthorizationDigest = digest
	store := &storedWorkItemTupleRouteStore{stored: stored}
	app, token := newContextFabricTestAppWithResults(t, nil, store)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.Result.ResultID))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a digest mismatch (body %s)", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), stored.Result.ResultID) {
		t.Fatalf("digest mismatch response disclosed the stored result id: %s", recorder.Body.String())
	}
}

func TestContextFabricInvestigationResultMCPMatchesByIDForStoredWorkItemCensus(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	stored := storedWorkItemTupleRouteFixture(t, principal)
	store := &storedWorkItemTupleRouteStore{stored: stored}
	app, token := newParityHostedApp(t, nil, store)
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)
	boot, err := newMCPBootstrapForStoredTuple(t)
	if err != nil {
		t.Fatalf("sidecar bootstrap: %v", err)
	}
	apiResult := getRealAPIResult(t, server, token, stored.Result.ResultID)
	mcpResult := callRealMCPInvestigationResult(t, boot, stored.Result.ResultID)
	apiJSON, err := json.Marshal(apiResult)
	if err != nil {
		t.Fatal(err)
	}
	mcpJSON, err := json.Marshal(mcpResult)
	if err != nil {
		t.Fatal(err)
	}
	if string(apiJSON) != string(mcpJSON) {
		t.Fatalf("real API and MCP result-by-id surfaces diverged:\nAPI=%s\nMCP=%s", apiJSON, mcpJSON)
	}
}

func newMCPBootstrapForStoredTuple(t *testing.T) (*acrmcp.Bootstrap, error) {
	t.Helper()
	return acrmcp.NewBootstrap(context.Background(), "1.2.5")
}

func storedWorkItemTupleRouteCensusDetail(declared, served int) contractsv1.ContextFabricCoverageDetail {
	detail := contractsv1.ContextFabricCoverageDetail{
		DetailID: "cov-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
		Degrading: true, Kind: contractsv1.ContextFabricSubjectWorkItem, Declared: &declared, Served: &served,
		Raw: storedWorkItemTupleRouteCensusReason(declared, served),
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	return detail
}

func storedWorkItemTupleRouteCensusReason(declared, served int) string {
	return "kind_census_truncated:work_item:" + strconv.Itoa(declared) + ":" + strconv.Itoa(served)
}

func findWorkItemTupleRouteCensusDetail(details []contractsv1.ContextFabricCoverageDetail) *contractsv1.ContextFabricCoverageDetail {
	for index := range details {
		if details[index].Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && details[index].Kind == contractsv1.ContextFabricSubjectWorkItem {
			return &details[index]
		}
	}
	return nil
}

func containsWorkItemTupleRouteLimitation(limitations []string) bool {
	for _, limitation := range limitations {
		if limitation == contractsv1.ContextFabricFactScopeUnexpandedLimitation {
			return true
		}
	}
	return false
}
