package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// callerOrgID matches the organization issueScopedCredential mints tokens
// for. The cross-tenant test needs a DIFFERENT organization to seed a
// foreign result under.
const (
	callerOrgID  = "org_1"
	foreignOrgID = "org_2"
)

func investigationResultRequest(t *testing.T, token, resultID string) *http.Request {
	t.Helper()
	path := strings.Replace(ContextFabricInvestigationResultPath, "{result_id}", resultID, 1)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	return request
}

// seedResult stores one result under orgID, using the real in-memory store
// so organization scoping is genuinely exercised rather than stubbed.
func seedResult(t *testing.T, store *memoryinvestigation.Store, orgID, resultID string) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	result := validContextFabricInvestigationResult()
	result.ResultID = resultID
	// An empty snapshot and a nil epoch are the CHAOS-3782 "answer reuse is
	// off" values, and the current-axis key is CHAOS-3781's fixed literal.
	// These tests seed results to exercise RETRIEVAL, which does not depend
	// on reuse bookkeeping either way.
	if err := store.Save(context.Background(), storage.Principal{OrgID: orgID}, result, contextfabric.SourceWatermarkSnapshot{}, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	return result
}

func TestContextFabricInvestigationResultRouteReturnsStoredResult(t *testing.T) {
	store := memoryinvestigation.NewStore()
	seeded := seedResult(t, store, callerOrgID, "result_route_test01")
	app, token := newContextFabricTestAppWithResults(t, nil, store)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, seeded.ResultID))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ResultID != seeded.ResultID {
		t.Errorf("result_id = %q, want %q", got.ResultID, seeded.ResultID)
	}
	// The retrieval route must hand back the canonical contract, not a
	// projection: a consumer fetches here precisely when it wants the
	// full detail a projection dropped.
	if got.SchemaVersion != contractsv1.ContextFabricInvestigationResultSchema {
		t.Errorf("schema_version = %q, want the canonical result contract", got.SchemaVersion)
	}
	if got.DirectJudgment != seeded.DirectJudgment {
		t.Errorf("direct_judgment was not carried through")
	}
}

// TestContextFabricInvestigationResultRouteHidesForeignResults is the
// tenancy test. A result belonging to another organization must be
// indistinguishable from one that does not exist: any observable difference
// turns result_id into a cross-tenant existence oracle.
func TestContextFabricInvestigationResultRouteHidesForeignResults(t *testing.T) {
	store := memoryinvestigation.NewStore()
	foreign := seedResult(t, store, foreignOrgID, "result_foreign_0001")
	app, token := newContextFabricTestAppWithResults(t, nil, store)

	foreignRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(foreignRecorder, investigationResultRequest(t, token, foreign.ResultID))

	unknownRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(unknownRecorder, investigationResultRequest(t, token, "result_unknown_0001"))

	if foreignRecorder.Code != http.StatusNotFound {
		t.Fatalf("foreign result status = %d, want 404", foreignRecorder.Code)
	}
	if unknownRecorder.Code != http.StatusNotFound {
		t.Fatalf("unknown result status = %d, want 404", unknownRecorder.Code)
	}
	// Compare every part of the body except request_id, which is a
	// per-request correlation value and is SUPPOSED to differ. Everything
	// else -- code, message, retryability -- must be byte-identical, or a
	// caller could tell the two cases apart.
	foreignBody := errorBodyWithoutRequestID(t, foreignRecorder.Body.Bytes())
	unknownBody := errorBodyWithoutRequestID(t, unknownRecorder.Body.Bytes())
	if !reflect.DeepEqual(foreignBody, unknownBody) {
		t.Errorf("foreign and unknown results produced distinguishable responses:\n foreign = %v\n unknown = %v", foreignBody, unknownBody)
	}
	if strings.Contains(foreignRecorder.Body.String(), foreignOrgID) {
		t.Errorf("response leaked the owning organization")
	}
	// Headers must not betray the difference either. X-Request-ID is the
	// header form of the same per-request correlation value and is
	// excluded for the same reason.
	if !reflect.DeepEqual(headersWithoutRequestID(foreignRecorder), headersWithoutRequestID(unknownRecorder)) {
		t.Errorf("foreign and unknown results produced distinguishable headers:\n foreign = %v\n unknown = %v",
			headersWithoutRequestID(foreignRecorder), headersWithoutRequestID(unknownRecorder))
	}
}

func headersWithoutRequestID(recorder *httptest.ResponseRecorder) http.Header {
	header := recorder.Header().Clone()
	header.Del("X-Request-ID")
	return header
}

// errorBodyWithoutRequestID decodes an error envelope and drops the
// per-request correlation ID, leaving only the parts that must be
// identical across indistinguishable failures.
func errorBodyWithoutRequestID(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	delete(decoded, "request_id")
	return decoded
}

// TestContextFabricInvestigationResultRouteRejectsMalformedIDsAsNotFound
// proves a malformed identifier gets the same answer as an unknown one, and
// that the store is never consulted for it.
func TestContextFabricInvestigationResultRouteRejectsMalformedIDsAsNotFound(t *testing.T) {
	store := memoryinvestigation.NewStore()
	seedResult(t, store, callerOrgID, "result_route_test01")
	counting := &countingResultStore{inner: store}
	app, token := newContextFabricTestAppWithResults(t, nil, counting)

	// Too short for the 8..256 contract bound. A trailing-space variant
	// cannot be tested through the mux: net/http normalizes the path
	// before the handler sees it, so the length bound is the reachable
	// half of this guard.
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, "short"))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if counting.calls != 0 {
		t.Errorf("store was consulted %d times for a malformed ID, want 0", counting.calls)
	}
}

func TestContextFabricInvestigationResultRouteWithoutStoreReturns503(t *testing.T) {
	app, token := newContextFabricTestAppWithResults(t, nil, nil)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, "result_route_test01"))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

// TestContextFabricInvestigationResultRouteRequiresAuthEvenWithoutStore
// pins CHAOS-3755 finding H5 for this route: the unconfigured-store 503
// must sit BEHIND authentication, so an anonymous caller cannot probe
// whether the store is wired.
func TestContextFabricInvestigationResultRouteRequiresAuthEvenWithoutStore(t *testing.T) {
	app, _ := newContextFabricTestAppWithResults(t, nil, nil)

	request := httptest.NewRequest(http.MethodGet, strings.Replace(ContextFabricInvestigationResultPath, "{result_id}", "result_route_test01", 1), nil)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an unauthenticated request", recorder.Code)
	}
}

// TestContextFabricInvestigationResultRouteCanceledContextWritesNoResponse
// mirrors the investigation route's cancellation behavior: a client that
// went away gets nothing written on its behalf.
func TestContextFabricInvestigationResultRouteCanceledContextWritesNoResponse(t *testing.T) {
	app, _ := newContextFabricTestAppWithResults(t, nil, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, ContextFabricInvestigationResultPath, nil)
	canceled, cancel := context.WithCancel(request.Context())
	cancel()

	app.writeInvestigationResultError(recorder, request.WithContext(canceled), storage.Principal{OrgID: callerOrgID}, context.Canceled)

	if recorder.Body.Len() != 0 {
		t.Errorf("wrote %q for a canceled request, want nothing", recorder.Body.String())
	}
}

// TestContextFabricInvestigationResultErrorMapping covers the additive
// retrieval mapping. It stays separate from the investigation route's
// mapping on purpose: a stored-result read cannot fail the graph, model, or
// synthesis ways an investigation can.
func TestContextFabricInvestigationResultErrorMapping(t *testing.T) {
	app, _ := newContextFabricTestAppWithResults(t, nil, nil)

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not_found", contextfabric.ErrInvestigationResultNotFound, http.StatusNotFound},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"rate_limited", contextfabric.ErrRateLimited, http.StatusTooManyRequests},
		{"unavailable", contextfabric.ErrUnavailable, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, ContextFabricInvestigationResultPath, nil)
			app.writeInvestigationResultError(recorder, request, storage.Principal{OrgID: callerOrgID}, tc.err)
			if recorder.Code != tc.want {
				t.Errorf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}
}

// TestAdapterNotFoundErrorsClassifyThroughThePort proves the retrieval
// route can classify not-found without knowing which store adapter it
// holds -- the reason contextfabric.ErrInvestigationResultNotFound exists.
func TestAdapterNotFoundErrorsClassifyThroughThePort(t *testing.T) {
	if !isInvestigationResultNotFound(memoryinvestigation.ErrNotFound) {
		t.Error("memoryinvestigation.ErrNotFound does not classify through the port sentinel")
	}
}

func isInvestigationResultNotFound(err error) bool {
	return errors.Is(err, contextfabric.ErrInvestigationResultNotFound)
}

// countingResultStore records how many times Get reached the store, so a
// test can prove a request was rejected before any store work happened.
type countingResultStore struct {
	inner *memoryinvestigation.Store
	calls int
}

func (s *countingResultStore) Save(ctx context.Context, principal storage.Principal, result contextfabric.InvestigationResult, reuseSnapshot contextfabric.SourceWatermarkSnapshot, reuseEpoch contextfabric.RebuildEpoch, timeAxisKey string) error {
	return s.inner.Save(ctx, principal, result, reuseSnapshot, reuseEpoch, timeAxisKey)
}

func (s *countingResultStore) Get(ctx context.Context, principal storage.Principal, resultID string) (contextfabric.InvestigationResult, error) {
	s.calls++
	return s.inner.Get(ctx, principal, resultID)
}
