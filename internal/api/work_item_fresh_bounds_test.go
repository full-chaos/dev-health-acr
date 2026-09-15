package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

func roundTripFreshTupleRequest(t *testing.T, f *freshTupleProducerFixture, body contextfabric.InvestigationRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "req_00000000000000000000000000000001")
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	recorder := httptest.NewRecorder()
	f.app.InstrumentedHandler(f.app.Handler()).ServeHTTP(recorder, request)
	return recorder
}

func serveFreshTupleRequest(t *testing.T, f *freshTupleProducerFixture, body contextfabric.InvestigationRequest) contextfabric.InvestigationResult {
	t.Helper()
	recorder := roundTripFreshTupleRequest(t, f, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s; phases=%v engine=%v", recorder.Code, recorder.Body.String(), f.client.phases, f.engineErr)
	}
	var result contextfabric.InvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	assertResponseOwnerGateFree(t, f.gate)
	return result
}

func TestWorkItemFreshPartialRowsDoNotBecomeContent(t *testing.T) {
	for _, phase := range []string{"status", "work"} {
		t.Run(phase, func(t *testing.T) {
			f := newFreshTupleProducerFixture(t, phase+"_partial")
			body := investigationRequestBody()
			body.Question = "What is the state and count of this project's work items?"
			body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
			result := serveFreshTupleRequest(t, f, body)
			if f.client.scanned[phase] != 1 {
				t.Fatalf("partial error did not follow a real scanned row: %v", f.client.scanned)
			}
			if !reflect.DeepEqual(f.client.phases, []string{"s1", "status", "work"}) {
				t.Fatalf("phases=%v", f.client.phases)
			}
			counts := map[contextfabric.FactKind]int{}
			for _, claim := range result.ClaimedFacts {
				counts[claim.Kind]++
			}
			failed, survived := contextfabric.FactStatus, contextfabric.FactWork
			if phase == "work" {
				failed, survived = survived, failed
			}
			if counts[failed] != 0 || counts[survived] != 1 {
				t.Fatalf("partial rows leaked or independent content lost: %v", counts)
			}
			assertFreshTupleSurfaces(t, f, phase, result)
		})
	}
}

// The controlled S1 rows are already the capped SQL output, not a substitute
// for the separate real-DDL C+2 query proof. All 200 retained members pass
// through the real S1 decoder and both real content providers.
func TestWorkItemFreshCanonicalBoundReachesSynthesis(t *testing.T) {
	for _, mode := range []string{"aligned_large", "misaligned_route_refusal", "aligned_default"} {
		t.Run(mode, func(t *testing.T) { runFreshTupleCanonicalBound(t, mode) })
	}
}

func runFreshTupleCanonicalBound(t *testing.T, mode string) {
	f := newFreshTupleProducerFixtureWithBudget(t, "", limits.ResourceBudget{MaxItems: 500, MaxTokens: 500000, MaxBytes: 8 << 20})
	f.app.config.MaxItems = 500
	n := 200
	if mode != "aligned_large" {
		f = newFreshTupleProducerFixture(t, "")
	}
	if mode == "aligned_default" {
		n = 34
	}
	if mode == "misaligned_route_refusal" {
		f.engineOptions.MaxItems = 0
		engine, err := contextfabric.NewEngine(f.dependencies, f.engineOptions)
		if err != nil {
			t.Fatal(err)
		}
		f.engine = engine
	}
	f.model.statusOnly = true // 200 status claims fit the existing 250-claim limit.
	f.client.rowsByPhase = map[string][][]any{}
	ids := []string{}
	for i := 0; i < n; i++ {
		workID := fmt.Sprintf("work-%03d", i)
		id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", workID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		f.client.rowsByPhase["s1"] = append(f.client.rowsByPhase["s1"], []any{id, "repo-1", workID, hostedTestRepository, uint8(1), uint64(2001), uint64(2001), uint64(0), uint64(0), uint64(0)})
		f.client.rowsByPhase["status"] = append(f.client.rowsByPhase["status"], []any{workID, "open", "repo-1"})
		f.client.rowsByPhase["work"] = append(f.client.rowsByPhase["work"], []any{workID, "Title " + workID, "repo-1"})
	}
	sort.Strings(ids)
	observed := false
	synthesisSizes := []int{}
	f.model.observe = func(input contextfabric.SynthesisInput) {
		synthesisSizes = append(synthesisSizes, len(input.Graph.Cohort.Members))
		if observed {
			return
		}
		observed = true
		if input.Graph.Cohort == nil || len(input.Graph.Cohort.Members) != n {
			t.Fatalf("S1 retained cohort=%+v", input.Graph.Cohort)
		}
		gotIDs := []string{}
		for _, member := range input.Graph.Cohort.Members {
			gotIDs = append(gotIDs, member.Subject.CanonicalID)
		}
		if !reflect.DeepEqual(gotIDs, ids) {
			t.Fatal("S1 did not retain the canonical lexical identity set")
		}
		facts := map[string]map[contextfabric.FactKind]bool{}
		for _, fact := range input.Facts.Facts {
			if facts[fact.Subject.CanonicalID] == nil {
				facts[fact.Subject.CanonicalID] = map[contextfabric.FactKind]bool{}
			}
			facts[fact.Subject.CanonicalID][fact.Kind] = true
		}
		for _, id := range ids {
			if !facts[id][contextfabric.FactStatus] || !facts[id][contextfabric.FactWork] {
				t.Fatalf("member %s lost a real provider fact: %v", id, facts[id])
			}
		}
	}
	body := investigationRequestBody()
	body.Question = "What is the state and count of this project's work items?"
	body.Options.MaxCohortMembers = 250
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	var result contextfabric.InvestigationResult
	if mode == "misaligned_route_refusal" {
		recorder := roundTripFreshTupleRequest(t, f, body)
		if recorder.Code != http.StatusRequestEntityTooLarge || !bytes.Contains(recorder.Body.Bytes(), []byte(`"measured_items":402`)) || !bytes.Contains(recorder.Body.Bytes(), []byte(`"max_items":50`)) {
			t.Fatalf("default budget refusal=%d %s", recorder.Code, recorder.Body.String())
		}
		if f.engineErr != nil {
			t.Fatalf("Engine failed before response budget: %v", f.engineErr)
		}
		persisted, err := f.store.Get(context.Background(), f.principal, "result_tuple_producer_001")
		if err != nil {
			t.Fatal(err)
		}
		result = persisted.Result
		assertResponseOwnerGateFree(t, f.gate)
	} else {
		result = serveFreshTupleRequest(t, f, body)
	}
	if !observed {
		t.Fatal("actual synthesis did not run")
	}
	if !reflect.DeepEqual(f.client.phases, []string{"s1", "status", "work"}) {
		t.Fatalf("phases=%v", f.client.phases)
	}
	for _, query := range f.client.queries {
		if query.phase == "s1" {
			found := false
			for _, binding := range query.bindings {
				if binding.Name == "serve_limit" {
					found = true
					if binding.Value != uint32(n) {
						t.Fatalf("S1 K=%v", binding.Value)
					}
				}
			}
			if !found {
				t.Fatal("S1 serve limit missing")
			}
		}
	}
	stored, err := f.store.Get(context.Background(), f.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	census := stored.SemanticState.WorkItemCensus
	wantRetained := n
	if mode == "aligned_default" {
		wantRetained = 17
		if !reflect.DeepEqual(synthesisSizes, []int{34, 17}) {
			t.Fatalf("aligned default synthesis sizes=%v", synthesisSizes)
		}
		if len(result.Cohort.Members) != 17 || len(result.ClaimedFacts) != 18 {
			t.Fatalf("aligned default served members/claims=%d/%d", len(result.Cohort.Members), len(result.ClaimedFacts))
		}
		for i, member := range result.Cohort.Members {
			if member.Subject.CanonicalID != ids[i] {
				t.Fatal("retry did not keep the canonical lexical prefix")
			}
		}
	}
	if census.State != contextfabric.WorkItemMembershipCensusFloor || census.Value != 2000 || census.Retained != wantRetained {
		t.Fatalf("persisted canonical floor=%+v", census)
	}
	if !strings.HasSuffix(result.DeterministicAnswer, "Counted at least 2000 work items.") {
		t.Fatalf("floor count prose=%q", result.DeterministicAnswer)
	}
	projection := answerprojection.Project(result, answerprojection.Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatal(err)
	}
	// Membership count outcomes retain the measured population. D47 separately
	// discloses the final served document's cohort count.
	assertFreshTupleFloorMeaning(t, result)
	assertFreshTupleD47(t, result, 2000, len(result.Cohort.Members))
	if mode != "aligned_default" && (len(result.Cohort.Members) != 200 || len(result.ClaimedFacts) != 201) {
		t.Fatalf("served cohort/claims=%d/%d", len(result.Cohort.Members), len(result.ClaimedFacts))
	}
	if mode != "misaligned_route_refusal" {
		assertFreshTupleStoredMCPParity(t, f, result)
	}
	t.Logf("synthesis sizes=%v", synthesisSizes)
	t.Logf("actual producer: state=%s value=%d retained=%d served_members=%d public_claims=%d deterministic_answer=%q", census.State, census.Value, census.Retained, len(result.Cohort.Members), len(result.ClaimedFacts), result.DeterministicAnswer)
}

func assertFreshTupleCountOutcome(t *testing.T, result contextfabric.InvestigationResult, declared, served int) {
	t.Helper()
	found := false
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == "count/member/work_item" && row.Stage != "planning" {
			found = true
			if row.Declared != declared || row.Served != served {
				t.Fatalf("count outcome=%+v, want declared=%d served=%d", row, declared, served)
			}
		}
	}
	if !found {
		t.Fatal("produced count outcome missing")
	}
}

func TestWorkItemFreshCurrentRepositoryScopeReachesReadersAndCensus(t *testing.T) {
	f := newFreshTupleProducerFixture(t, "")
	body := investigationRequestBody()
	body.Question = "What is the state and count of this project's work items?"
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	body.RequestedScope.RepositorySlugs = []string{hostedTestRepository}
	result := serveFreshTupleRequest(t, f, body)
	if !reflect.DeepEqual(f.client.phases, []string{"s1", "status", "work"}) {
		t.Fatalf("phases=%v", f.client.phases)
	}
	for _, query := range f.client.queries {
		values := map[string]any{}
		for _, binding := range query.bindings {
			values[binding.Name] = binding.Value
		}
		t.Logf("phase=%s bindings=%v", query.phase, values)
		for _, name := range []string{"authorized_repo_slugs", "requested_repo_slugs"} {
			if !reflect.DeepEqual(values[name], []string{hostedTestRepository}) {
				t.Fatalf("%s %s=%v", query.phase, name, values[name])
			}
		}
	}
	stored, err := f.store.Get(context.Background(), f.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	census := stored.SemanticState.WorkItemCensus
	if !reflect.DeepEqual(census.RequestedRepositoryScope, body.RequestedScope.RepositorySlugs) {
		t.Fatalf("persisted requested scope=%v", census.RequestedRepositoryScope)
	}
	digest, err := contextfabric.WorkItemAuthorizationDigest(f.principal, body.RequestedScope.RepositorySlugs)
	if err != nil || digest != census.AuthorizationDigest {
		t.Fatalf("current scope digest=%q want=%q err=%v", census.AuthorizationDigest, digest, err)
	}
	assertFreshTupleCountOutcome(t, result, 1, 1)
}

func assertFreshTupleD47(t *testing.T, result contextfabric.InvestigationResult, declared, served int) {
	t.Helper()
	found := false
	for _, detail := range result.Coverage.Details {
		if detail.Code == "kind_census_truncated" && detail.Kind == contextfabric.SubjectWorkItem {
			if found {
				t.Fatal("duplicate work item D47 detail")
			}
			found = true
			if detail.Declared == nil || detail.Served == nil || *detail.Declared != declared || *detail.Served != served {
				t.Fatalf("D47=%+v want declared=%d served=%d", detail, declared, served)
			}
		}
	}
	if !found {
		t.Fatalf("D47 missing: %+v", result.Coverage.Details)
	}
}

func TestWorkItemFreshFloorActualSurfaces(t *testing.T) {
	for _, failure := range []string{"", "status"} {
		t.Run("read_"+failure, func(t *testing.T) {
			f := newFreshTupleProducerFixture(t, failure)
			f.client.rowsByPhase = map[string][][]any{"s1": {{f.client.memberID, "repo-1", "work-1", hostedTestRepository, uint8(1), uint64(2001), uint64(2001), uint64(0), uint64(0), uint64(0)}}}
			body := investigationRequestBody()
			body.Question = "What is the state and count of this project's work items?"
			body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
			result := serveFreshTupleRequest(t, f, body)
			assertFreshTupleD47(t, result, 2000, 1)
			assertFreshTupleFloorMeaning(t, result)
			statusClaims := 0
			for _, claim := range result.ClaimedFacts {
				if claim.Kind == contextfabric.FactStatus {
					statusClaims++
				}
				if claim.Kind == contextfabric.FactStatus || claim.Kind == contextfabric.FactWork {
					if claim.Subject.CanonicalID != f.client.memberID {
						t.Fatal("floor content left retained membership")
					}
				}
			}
			wantStatus := 1
			if failure == "status" {
				wantStatus = 0
			}
			if statusClaims != wantStatus {
				t.Fatalf("floor status claims=%d want=%d", statusClaims, wantStatus)
			}
			for _, query := range f.client.queries {
				if query.phase == "status" || query.phase == "work" {
					found := false
					for _, binding := range query.bindings {
						if binding.Name == "ids" {
							found = true
							if !reflect.DeepEqual(binding.Value, []string{"repo-1:work-1"}) {
								t.Fatalf("%s retained query ids=%v", query.phase, binding.Value)
							}
						}
					}
					if !found {
						t.Fatal("content query subject binding absent")
					}
				}
			}
			name := "floor"
			if failure == "status" {
				name = "floor_status"
			}
			assertFreshTupleSurfaces(t, f, name, result)
		})
	}
}

func assertFreshTupleFloorMeaning(t *testing.T, result contextfabric.InvestigationResult) {
	t.Helper()
	assertFreshTupleCountOutcome(t, result, 2000, 2000)
	claims := 0
	for _, claim := range result.ClaimedFacts {
		if claim.Field == "work_item_count" {
			claims++
			if claim.Value.Integer == nil || *claim.Value.Integer != 2000 {
				t.Fatalf("floor count claim=%+v", claim)
			}
		}
	}
	if claims != 1 {
		t.Fatalf("floor count claims=%d", claims)
	}
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == "count/member/work_item" && row.Stage == "assembled_result" {
			if row.Outcome != "narrowed" || row.Impact != "scope" || row.CauseCoverage != "population_truncated" || !row.CauseObserved || len(row.Refinements) != 0 || row.CauseOverrun != "" || row.CauseNarrowing != "" {
				t.Fatalf("floor count disposition=%+v", row)
			}
		}
	}
	derived := contractsv1.DeriveContextFabricAnswerCompletenessState(result.Completeness.Outcomes)
	if derived == "complete" || result.Completeness.State != derived {
		t.Fatalf("floor completeness=%s derived=%s", result.Completeness.State, derived)
	}
}

func TestWorkItemFreshUnauthorizedRepositoryDoesNotReadMembers(t *testing.T) {
	f := newFreshTupleProducerFixture(t, "")
	body := investigationRequestBody()
	body.Question = "What is the state and count of this project's work items?"
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	body.RequestedScope.RepositorySlugs = []string{"foreign/private"}
	f.graph.allowNoCandidate = true
	recorder := roundTripFreshTupleRequest(t, f, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unauthorized repository HTTP %d: %s; engine=%v", recorder.Code, recorder.Body.String(), f.engineErr)
	}
	if len(f.client.phases) != 0 || f.graph.resolve != 1 || f.graph.discover != 0 {
		t.Fatalf("unauthorized request reached producers: phases=%v resolve=%d discover=%d", f.client.phases, f.graph.resolve, f.graph.discover)
	}
	var result contextfabric.InvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "no_match" || result.Cohort != nil || len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("unauthorized terminal=%+v", result)
	}
	assertFreshTerminalStoredAuthorization(t, f, result)
	assertResponseOwnerGateFree(t, f.gate)
}

func TestWorkItemFreshAuthorizedAmbiguityPreservesAcceptedFrame(t *testing.T) {
	f := newFreshTupleProducerFixture(t, "")
	f.graph.ambiguousCandidates = true
	body := investigationRequestBody()
	body.Question = "What is the state and count of this project's work items?"
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	result := serveFreshTupleRequest(t, f, body)
	if result.Status != "clarification_required" && result.Status != "no_match" {
		t.Fatalf("ambiguous terminal=%s", result.Status)
	}
	if len(result.SubjectResolution.Candidates) != 2 || len(result.SubjectResolution.Committed) != 0 || result.Cohort != nil {
		t.Fatalf("actual ambiguous resolution=%+v cohort=%+v", result.SubjectResolution, result.Cohort)
	}
	if len(f.client.phases) != 0 || f.graph.resolve != 1 || f.graph.discover != 0 {
		t.Fatalf("ambiguous candidates reached readers: phases=%v resolve=%d discover=%d", f.client.phases, f.graph.resolve, f.graph.discover)
	}
	stored, err := f.store.Get(context.Background(), f.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SemanticStateRead != contextfabric.SemanticStateReadAvailable || stored.SemanticState == nil || stored.SemanticState.Frame == nil || !reflect.DeepEqual(stored.SemanticState.Frame.SubjectExpression, f.model.frame.SubjectExpression) || stored.SemanticState.Validation.GateOutcome != "passed" || stored.SemanticState.WorkItemCensus != nil {
		t.Fatalf("accepted ambiguous frame/census=%+v", stored.SemanticState)
	}
	assertFreshTerminalStoredAuthorization(t, f, result)
}
