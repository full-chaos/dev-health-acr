package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/evidence/"+ref, nil)
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

func (f failingLookupResults) ResultIDsCitingEvidence(ctx context.Context, principal storage.Principal, ref string, limit int) ([]string, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	if f.ids != nil {
		return f.ids, nil
	}
	return f.Store.ResultIDsCitingEvidence(ctx, principal, ref, limit)
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
		return map[string]any{"candidate_count": candidate, "citing_count": citing, "unreadable_count": unreadable, "admitted_count": admitted, "denied_count": denied, "unavailable_count": unavailable}
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
