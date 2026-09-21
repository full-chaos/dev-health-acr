package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func evidenceRequest(t *testing.T, token, ref string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/evidence/"+url.PathEscape(ref), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	return request
}

// citingStoredResult is the shared valid result citing refs at the result
// level, with the label map the engine stamps over the closure.
func citingStoredResult(resultID string, refs ...string) contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	result.ResultID = resultID
	result.EvidenceRefIDs = append(result.EvidenceRefIDs, refs...)
	result.EvidenceRefLabels = map[string]string{}
	for ref := range contractsv1.ContextFabricEvidenceRefClosure(result) {
		label, _ := contractsv1.ContextFabricEvidenceRefLabel(ref)
		result.EvidenceRefLabels[ref] = label
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

// resultsWithoutLookup exposes only the InvestigationResultStore port, so the
// evidence route finds no citing-result search.
type resultsWithoutLookup struct {
	contextfabric.InvestigationResultStore
}

// failingLookupResults is a store whose search or reads fail.
type failingLookupResults struct {
	*memoryinvestigation.Store
	lookupErr error
	getErr    error
	ids       []string
}

func (f failingLookupResults) ResultIDsCitingEvidence(ctx context.Context, principal storage.Principal, ref string, offset, limit int) ([]string, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	if f.ids != nil {
		if offset > 0 {
			return nil, nil
		}
		return f.ids, nil
	}
	return f.Store.ResultIDsCitingEvidence(ctx, principal, ref, offset, limit)
}

func (f failingLookupResults) Get(ctx context.Context, principal storage.Principal, resultID string) (contextfabric.StoredInvestigationResult, error) {
	if f.getErr != nil {
		return contextfabric.StoredInvestigationResult{}, f.getErr
	}
	return f.Store.Get(ctx, principal, resultID)
}

// Every expansion reason, each driven through the real evidence route, the
// real stored-result gate and the real in-memory store, and certified
// against its eventspec declaration from the emitted line.
func TestEvidenceRouteExpandsContextFabricRefsWithAClassifiedDecision(t *testing.T) {
	cited := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "repo-cited-0001")
	// A type segment longer than an evidence source's entity_type bound: a
	// ref a stored result may carry, whose expansion cannot validate.
	oversized := "acr:v1:" + strings.Repeat("t", 120) + ":x-0001"
	counts := func(candidate, citing, unreadable, admitted, denied, unavailable int) map[string]any {
		return map[string]any{"candidate_count": candidate, "citing_count": citing, "unreadable_count": unreadable, "admitted_count": admitted, "denied_count": denied, "unavailable_count": unavailable, "withheld_count": 0}
	}
	type cell struct {
		name   string
		ref    string
		setup  func(app *App, store *memoryinvestigation.Store)
		status int
		reason contextfabric.EvidenceExpansionReason
		entity string
		want   map[string]any
	}
	otherRepo := subjectNodeGraph{grant: []string{"other-org/secret-service"}}
	cells := []cell{
		{name: "served", ref: cited, status: http.StatusOK, reason: contextfabric.EvidenceExpansionServed, entity: "repository",
			want: merge(counts(1, 1, 0, 1, 0, 0), map[string]any{"authorization_reason": "subjects_admitted"})},
		{name: "malformed", ref: "acr:v1:repository", status: http.StatusNotFound, reason: contextfabric.EvidenceExpansionMalformedRef, entity: contextfabric.EvidenceExpansionUnregisteredType,
			want: counts(0, 0, 0, 0, 0, 0)},
		{name: "no lookup", ref: cited, status: http.StatusNotFound, reason: contextfabric.EvidenceExpansionLookupUnavailable, entity: "repository",
			setup: func(app *App, store *memoryinvestigation.Store) {
				app.runtime.InvestigationResults = resultsWithoutLookup{store}
			}, want: counts(0, 0, 0, 0, 0, 0)},
		{name: "lookup failed", ref: cited, status: http.StatusServiceUnavailable, reason: contextfabric.EvidenceExpansionLookupFailed, entity: "repository",
			setup: func(app *App, store *memoryinvestigation.Store) {
				app.runtime.InvestigationResults = failingLookupResults{Store: store, lookupErr: errors.New("search down")}
			}, want: merge(counts(0, 0, 0, 0, 0, 0), map[string]any{"error_class": "internal"})},
		{name: "not cited", ref: contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "team-uncited-0001"), status: http.StatusNotFound, reason: contextfabric.EvidenceExpansionNotCited, entity: "team",
			want: counts(0, 0, 0, 0, 0, 0)},
		{name: "over-reported", ref: cited, status: http.StatusNotFound, reason: contextfabric.EvidenceExpansionNotCited, entity: "repository",
			setup: func(app *App, store *memoryinvestigation.Store) {
				seedResult3355(t, store, "org_1", citingStoredResult("result_expand_cites_other", contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "team-other-0001")))
				app.runtime.InvestigationResults = failingLookupResults{Store: store, ids: []string{"result_expand_cites_other"}}
			}, want: counts(1, 0, 0, 0, 0, 0)},
		{name: "unreadable", ref: cited, status: http.StatusServiceUnavailable, reason: contextfabric.EvidenceExpansionResultUnreadable, entity: "repository",
			setup: func(app *App, store *memoryinvestigation.Store) {
				app.runtime.InvestigationResults = failingLookupResults{Store: store, getErr: contextfabric.ErrUnavailable}
			}, want: merge(counts(1, 0, 1, 0, 0, 0), map[string]any{"error_class": "dependency_unavailable"})},
		{name: "denied", ref: cited, status: http.StatusNotFound, reason: contextfabric.EvidenceExpansionAuthorizationDenied, entity: "repository",
			setup: func(app *App, _ *memoryinvestigation.Store) {
				app.runtime.StoredResultGate = contextfabric.NewStoredResultGate(otherRepo)
			}, want: merge(counts(1, 1, 0, 0, 1, 0), map[string]any{"authorization_reason": "subject_denied"})},
		{name: "authorization unavailable", ref: cited, status: http.StatusServiceUnavailable, reason: contextfabric.EvidenceExpansionAuthorizationUnavailable, entity: "repository",
			setup: func(app *App, _ *memoryinvestigation.Store) {
				app.runtime.StoredResultGate = contextfabric.NewStoredResultGate(subjectNodeGraph{readErr: errors.New("graph down")})
			}, want: merge(counts(1, 1, 0, 0, 0, 1), map[string]any{"authorization_reason": "graph_read_failed", "error_class": "internal"})},
		{name: "expansion invalid", ref: oversized, status: http.StatusServiceUnavailable, reason: contextfabric.EvidenceExpansionInvalid, entity: contextfabric.EvidenceExpansionUnregisteredType,
			want: merge(counts(1, 1, 0, 1, 0, 0), map[string]any{"authorization_reason": "subjects_admitted", "error_class": "internal"})},
	}
	executed := map[contextfabric.EvidenceExpansionReason]bool{}
	for _, tc := range cells {
		t.Run(tc.name, func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			seedResult3355(t, store, "org_1", citingStoredResult("result_expand_"+strings.ReplaceAll(tc.name, " ", "_"), cited, oversized))
			logs := &bytes.Buffer{}
			app, token := newParityHostedAppWithLogs(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
			if tc.setup != nil {
				tc.setup(app, store)
			}
			logs.Reset()
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, evidenceRequest(t, token, tc.ref))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusOK {
				var expanded contractsv1.ExpandedEvidence
				if err := json.Unmarshal(rec.Body.Bytes(), &expanded); err != nil {
					t.Fatal(err)
				}
				if err := expanded.Validate(); err != nil {
					t.Fatalf("served expansion invalid: %v", err)
				}
				if expanded.Evidence.EvidenceRefID != tc.ref || expanded.Evidence.Source.EntityType != tc.entity || expanded.Evidence.Source.EntityID != "repo-cited-0001" {
					t.Fatalf("served %+v for %s", expanded.Evidence, tc.ref)
				}
				if expanded.Structured["result_id"] != "result_expand_served" {
					t.Fatalf("structured result_id = %v", expanded.Structured["result_id"])
				}
				gateLines, err := certify.Parse(logs.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := certify.Certify(gateLines, certify.Assertion{Event: eventspec.StoredResultAuthorization, Want: map[string]any{
					"org_id": "org_1", "surface": string(contextfabric.StoredResultSurfaceEvidenceExpansion), "decision": "admitted", "reason": "subjects_admitted",
					"request_id": rec.Header().Get("X-Request-ID"),
				}}); err != nil {
					t.Fatalf("certify stored result authorization: %v\n%s", err, logs.String())
				}
			}
			parsed, err := certify.Parse(logs.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			want := map[string]any{"org_id": "org_1", "reason": string(tc.reason), "entity_type": tc.entity, "request_id": rec.Header().Get("X-Request-ID")}
			for key, value := range tc.want {
				want[key] = value
			}
			if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: want}); err != nil {
				t.Fatalf("certify: %v\n%s", err, logs.String())
			}
			executed[tc.reason] = true
		})
	}
	for _, reason := range contextfabric.EvidenceExpansionReasonVocabulary() {
		if !executed[reason] {
			t.Errorf("reason %q has no executed cell", reason)
		}
	}
}

// A packet evidence handle never reaches the Context Fabric expansion.
func TestEvidenceRouteKeepsPacketHandlesOnThePacketStore(t *testing.T) {
	store := memoryinvestigation.NewStore()
	logs := &bytes.Buffer{}
	app, token := newParityHostedAppWithLogs(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
	logs.Reset()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, evidenceRequest(t, token, "ev2_packet_handle_0001"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the packet store's not-found", rec.Code)
	}
	if strings.Contains(logs.String(), contextfabric.EvidenceExpansionLogMessage) {
		t.Fatalf("a packet handle reached the Context Fabric expansion:\n%s", logs.String())
	}
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// pagedResults serves every lookup page from a fixed newest-first id list,
// and fails Get for the ids in unreadable.
type pagedResults struct {
	*memoryinvestigation.Store
	ids        []string
	unreadable map[string]bool
}

func (p pagedResults) ResultIDsCitingEvidence(_ context.Context, _ storage.Principal, _ string, offset, limit int) ([]string, error) {
	if offset >= len(p.ids) {
		return nil, nil
	}
	end := min(offset+limit, len(p.ids))
	return append([]string(nil), p.ids[offset:end]...), nil
}

func (p pagedResults) Get(ctx context.Context, principal storage.Principal, resultID string) (contextfabric.StoredInvestigationResult, error) {
	if p.unreadable[resultID] {
		return contextfabric.StoredInvestigationResult{}, contextfabric.ErrUnavailable
	}
	return p.Store.Get(ctx, principal, resultID)
}

// subjectCitingResult is citingStoredResult about one project subject.
func subjectCitingResult(resultID, subjectID string, generated time.Time, refs ...string) contractsv1.ContextFabricInvestigationResult {
	result := citingStoredResult(resultID, refs...)
	subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: subjectID, Label: "Project " + subjectID}
	result.SubjectResolution.Committed = []contractsv1.ContextFabricSubjectRef{subject}
	result.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{}
	for i := range result.Drivers {
		result.Drivers[i].AffectedSubjects = []contractsv1.ContextFabricSubjectRef{subject}
	}
	result.GeneratedAt = generated
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

// A readable citing result is found however many newer citing results the
// caller may not read: the search pages instead of stopping at a cap.
func TestEvidenceRouteFindsAReadableResultBehindMoreThanOnePageOfDeniedOnes(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "repo-paged-0001")
	store := memoryinvestigation.NewStore()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	denied := contextfabric.CitedEvidenceCandidateLimit + 1
	nodes := map[string]map[string]interface{}{"project\x00project_readable": {"authorization_repositories": []string{hostedTestRepository}}}
	var ids []string
	for i := 0; i < denied; i++ {
		id := fmt.Sprintf("result_paged_denied_%02d", i)
		seedResult3355(t, store, "org_1", subjectCitingResult(id, fmt.Sprintf("project_secret_%02d", i), base.Add(time.Duration(denied-i)*time.Hour), ref))
		nodes[fmt.Sprintf("project\x00project_secret_%02d", i)] = map[string]interface{}{"authorization_repositories": []string{"other-org/secret-service"}}
		ids = append(ids, id)
	}
	seedResult3355(t, store, "org_1", subjectCitingResult("result_paged_readable", "project_readable", base, ref))
	ids = append(ids, "result_paged_readable")
	for _, results := range []contextfabric.InvestigationResultStore{store, pagedResults{Store: store, ids: ids}} {
		logs := &bytes.Buffer{}
		app, token := newParityHostedAppWithLogs(t, nil, results, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
		app.runtime.StoredResultGate = contextfabric.NewStoredResultGate(subjectNodeGraph{nodes: nodes})
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, evidenceRequest(t, token, ref))
		if rec.Code != http.StatusOK {
			t.Fatalf("%T: status = %d, want 200 from the readable oldest result: %s\n%s", results, rec.Code, rec.Body.String(), logs.String())
		}
		if !strings.Contains(rec.Body.String(), "result_paged_readable") {
			t.Fatalf("%T: served from the wrong result: %s", results, rec.Body.String())
		}
	}
}

// An unreadable candidate may be the readable one: the outcome is a 503, not
// the denial the other candidate earned.
func TestEvidenceRouteReportsAnUnreadableCandidateOverADeniedOne(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "repo-mixed-0001")
	store := memoryinvestigation.NewStore()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seedResult3355(t, store, "org_1", subjectCitingResult("result_mixed_unreadable", "project_unknown", base.Add(time.Hour), ref))
	seedResult3355(t, store, "org_1", subjectCitingResult("result_mixed_denied", "project_secret", base, ref))
	logs := &bytes.Buffer{}
	results := pagedResults{Store: store, ids: []string{"result_mixed_unreadable", "result_mixed_denied"}, unreadable: map[string]bool{"result_mixed_unreadable": true}}
	app, token := newParityHostedAppWithLogs(t, nil, results, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
	app.runtime.StoredResultGate = contextfabric.NewStoredResultGate(subjectNodeGraph{nodes: map[string]map[string]interface{}{
		"project\x00project_secret": {"authorization_repositories": []string{"other-org/secret-service"}},
	}})
	logs.Reset()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, evidenceRequest(t, token, ref))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s\n%s", rec.Code, rec.Body.String(), logs.String())
	}
	parsed, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{
		"org_id": "org_1", "reason": string(contextfabric.EvidenceExpansionResultUnreadable), "candidate_count": 2, "citing_count": 1, "unreadable_count": 1, "denied_count": 1, "withheld_count": 0,
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, logs.String())
	}
}

// storedTupleEvidenceStore serves one stored carrier and finds it for the
// ref it cites.
type storedTupleEvidenceStore struct {
	storedWorkItemTupleRouteStore
}

func (s *storedTupleEvidenceStore) ResultIDsCitingEvidence(_ context.Context, _ storage.Principal, ref string, offset, _ int) ([]string, error) {
	if _, ok := contractsv1.ContextFabricEvidenceRefClosure(s.stored.Result)[ref]; !ok || offset > 0 {
		return nil, nil
	}
	return []string{s.stored.Result.ResultID}, nil
}

// The expansion serves a result only when investigation_result would serve
// it to the same credential: a work-item tuple whose authorization digest
// changed is not found on both routes, and served on both when it matches.
func TestEvidenceRouteFollowsTheResultByIDServingDecision(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "example-org/widget-service:WI-1")
	for _, tc := range []struct {
		name   string
		mutate func(*contextfabric.StoredInvestigationResult)
		status int
	}{
		{"digest matches", func(*contextfabric.StoredInvestigationResult) {}, http.StatusOK},
		{"digest changed", func(stored *contextfabric.StoredInvestigationResult) {
			other := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{"other-org/other-repository"}}
			digest, err := contextfabric.WorkItemAuthorizationDigest(other, stored.SemanticState.WorkItemCensus.RequestedRepositoryScope)
			if err != nil {
				t.Fatal(err)
			}
			stored.SemanticState.WorkItemCensus.AuthorizationDigest = digest
		}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := storedWorkItemTupleRouteFixture(t, principal)
			tc.mutate(&stored)
			store := &storedTupleEvidenceStore{storedWorkItemTupleRouteStore{stored: stored}}
			logs := &bytes.Buffer{}
			app, token := newParityHostedAppWithLogs(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
			byID := httptest.NewRecorder()
			app.Handler().ServeHTTP(byID, investigationResultRequest(t, token, stored.Result.ResultID))
			expand := httptest.NewRecorder()
			app.Handler().ServeHTTP(expand, evidenceRequest(t, token, ref))
			if byID.Code != tc.status || expand.Code != tc.status {
				t.Fatalf("investigation_result status %d, source evidence status %d, want both %d: %s", byID.Code, expand.Code, tc.status, expand.Body.String())
			}
			if tc.status != http.StatusOK && strings.Contains(expand.Body.String(), stored.Result.ResultID) {
				t.Fatalf("withheld expansion disclosed the result id: %s", expand.Body.String())
			}
			reason, withheld := contextfabric.EvidenceExpansionServed, 0
			if tc.status != http.StatusOK {
				reason, withheld = contextfabric.EvidenceExpansionAuthorizationDenied, 1
			}
			parsed, err := certify.Parse(logs.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{
				"org_id": callerOrgID, "reason": string(reason), "entity_type": "work-item", "admitted_count": 1, "withheld_count": withheld,
			}}); err != nil {
				t.Fatalf("certify: %v\n%s", err, logs.String())
			}
		})
	}
}

// A stored row the result-by-id route refuses to serve (a satisfied
// requirement with no served evidence) is not expanded either: the
// expansion answers 503 as an unreadable candidate, never a 200 carrying
// that row's content.
func TestEvidenceRouteRefusesARowTheResultByIDRouteRefuses(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "team-refused-0001")
	stored := storedResultWithCountOutcome(t, contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: "count/member/team",
		Obligation: contractsv1.ContextFabricAnswerObligationCount, Outcome: contractsv1.ContextFabricRequirementSatisfied,
		Impact: contractsv1.ContextFabricAnswerImpactNone, Served: 3, Declared: 3,
	}, nil)
	stored.EvidenceRefIDs = append(stored.EvidenceRefIDs, ref)
	stored.EvidenceRefLabels = map[string]string{}
	for cited := range contractsv1.ContextFabricEvidenceRefClosure(stored) {
		label, _ := contractsv1.ContextFabricEvidenceRefLabel(cited)
		stored.EvidenceRefLabels[cited] = label
	}
	store := memoryinvestigation.NewStore()
	seedResult3355(t, store, "org_1", stored)
	logs := &bytes.Buffer{}
	app, token := newParityHostedAppWithLogs(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
	byID := httptest.NewRecorder()
	app.Handler().ServeHTTP(byID, investigationResultRequest(t, token, stored.ResultID))
	logs.Reset()
	expand := httptest.NewRecorder()
	app.Handler().ServeHTTP(expand, evidenceRequest(t, token, ref))
	if byID.Code != http.StatusInternalServerError || expand.Code != http.StatusServiceUnavailable {
		t.Fatalf("investigation_result status %d (want 500), source evidence status %d (want 503): %s", byID.Code, expand.Code, expand.Body.String())
	}
	parsed, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{
		"org_id": "org_1", "reason": string(contextfabric.EvidenceExpansionResultUnreadable), "admitted_count": 1, "unreadable_count": 1, "error_class": "internal",
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, logs.String())
	}
}
