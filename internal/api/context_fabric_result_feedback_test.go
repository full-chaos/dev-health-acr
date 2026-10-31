package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The Ask Dev answer feedback route (CHAOS-11950) on the submission harness:
// one memory store, two principals of one organization with the same grant.

func feedbackPath(resultID string) string {
	return strings.Replace(ContextFabricResultFeedbackPath, "{result_id}", resultID, 1)
}

func (h *submissionHarness) feedbackCall(method, token, resultID, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.feedbackCallOn(0, method, token, resultID, body)
}

// feedbackCallOn is feedbackCall through app index.
func (h *submissionHarness) feedbackCallOn(app int, method, token, resultID, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var request *http.Request
	if method == http.MethodPost {
		request = httptest.NewRequest(method, feedbackPath(resultID), bytes.NewReader([]byte(body)))
		request.Header.Set("Content-Type", "application/json")
	} else {
		request = httptest.NewRequest(method, feedbackPath(resultID), nil)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	recorder := httptest.NewRecorder()
	h.apps[app].Handler().ServeHTTP(recorder, request)
	return recorder
}

func feedbackRequestBody(correct, useful string) string {
	return `{"schema_version":"context_fabric_result_feedback_request.v1","correct":"` + correct + `","useful":"` + useful + `"}`
}

func readFeedbackBody(t *testing.T, recorder *httptest.ResponseRecorder) contractsv1.ContextFabricResultFeedback {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("feedback = %d %s", recorder.Code, recorder.Body.String())
	}
	feedback, err := contractsv1.ParseContextFabricResultFeedback(recorder.Body.Bytes())
	if err != nil {
		t.Fatalf("the served feedback fails its contract: %v (%s)", err, recorder.Body.String())
	}
	return feedback
}

// requireReadByIDAnswer holds a refused feedback call to the read by id's own
// answer for the same caller and id: the route tells nothing more.
func requireReadByIDAnswer(t *testing.T, h *submissionHarness, token, resultID string, got *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	read := h.get(0, token, resultID)
	if got.Code != wantStatus || read.Code != wantStatus {
		t.Fatalf("feedback = %d %s; read by id = %d %s; want both %d", got.Code, got.Body.String(), read.Code, read.Body.String(), wantStatus)
	}
	// The request ids differ (the calls carry different X-Request-IDs);
	// the error is the same.
	var feedbackError, readError contractsv1.ErrorEnvelope
	if err := json.Unmarshal(got.Body.Bytes(), &feedbackError); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(read.Body.Bytes(), &readError); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(feedbackError.Error, readError.Error) {
		t.Fatalf("feedback answer differs from the read by id's:\nfeedback %s\nread     %s", got.Body.String(), read.Body.String())
	}
}

func TestResultFeedbackRoutes_ownerSetsAndReadsBack(t *testing.T) {
	h := threadHarness(t)
	resultID := servedResultID(t, h.post(0, h.tokenA, "submission-11950-0001", "Which teams own the ops repository?"))
	unset := readFeedbackBody(t, h.feedbackCall(http.MethodGet, h.tokenA, resultID, ""))
	if unset.ResultID != resultID || unset.Correct != contractsv1.ContextFabricResultFeedbackUnset || unset.Useful != contractsv1.ContextFabricResultFeedbackUnset || unset.UpdatedAt != nil {
		t.Fatalf("never set = %+v", unset)
	}
	h.clock.Advance(time.Minute)
	set := readFeedbackBody(t, h.feedbackCall(http.MethodPost, h.tokenA, resultID, feedbackRequestBody("yes", "no")))
	if set.Correct != contractsv1.ContextFabricResultFeedbackYes || set.Useful != contractsv1.ContextFabricResultFeedbackNo || set.UpdatedAt == nil || !set.UpdatedAt.Equal(h.clock.Now()) {
		t.Fatalf("set = %+v", set)
	}
	read := readFeedbackBody(t, h.feedbackCall(http.MethodGet, h.tokenA, resultID, ""))
	if read.Correct != set.Correct || read.Useful != set.Useful || !read.UpdatedAt.Equal(*set.UpdatedAt) {
		t.Fatalf("read back = %+v, want %+v", read, set)
	}
	logs := h.logs[0].String()
	for _, want := range []string{`"msg":"context fabric result feedback"`, `"route":"set","outcome":"set"`, `"route":"get","outcome":"read"`, `"surface":"feedback"`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("the feedback lines lack %s:\n%s", want, logs)
		}
	}
}

func TestResultFeedbackRoutes_anotherPrincipalGetsTheReadByIDAnswer(t *testing.T) {
	h := threadHarness(t)
	resultID := servedResultID(t, h.post(0, h.tokenA, "submission-11950-0002", "Where is review time going?"))
	readFeedbackBody(t, h.feedbackCall(http.MethodPost, h.tokenA, resultID, feedbackRequestBody("yes", "yes")))
	requireReadByIDAnswer(t, h, h.tokenB, resultID, h.feedbackCall(http.MethodGet, h.tokenB, resultID, ""), http.StatusNotFound)
	requireReadByIDAnswer(t, h, h.tokenB, resultID, h.feedbackCall(http.MethodPost, h.tokenB, resultID, feedbackRequestBody("no", "no")), http.StatusNotFound)
	// An unknown id and a malformed one: the same answer.
	requireReadByIDAnswer(t, h, h.tokenA, "result_11950_unknown", h.feedbackCall(http.MethodGet, h.tokenA, "result_11950_unknown", ""), http.StatusNotFound)
	requireReadByIDAnswer(t, h, h.tokenA, "short", h.feedbackCall(http.MethodPost, h.tokenA, "short", feedbackRequestBody("no", "no")), http.StatusNotFound)
	// The owner's feedback is what the owner set.
	read := readFeedbackBody(t, h.feedbackCall(http.MethodGet, h.tokenA, resultID, ""))
	if read.Correct != contractsv1.ContextFabricResultFeedbackYes || read.Useful != contractsv1.ContextFabricResultFeedbackYes {
		t.Fatalf("the owner's feedback after the refused set = %+v", read)
	}
}

func TestResultFeedbackRoutes_aResultWithNoOwnerTakesNoFeedback(t *testing.T) {
	h := threadHarness(t)
	// No X-ACR-Submission-ID: an implicit admission; the result completes no
	// submission, has no owner, and is readable by id (organization-bearer
	// semantics).
	resultID := servedResultID(t, h.post(0, h.tokenA, "", "What changed last week?"))
	if read := h.get(0, h.tokenA, resultID); read.Code != http.StatusOK {
		t.Fatalf("the unowned result is not readable by id: %d %s", read.Code, read.Body.String())
	}
	for _, call := range []*httptest.ResponseRecorder{
		h.feedbackCall(http.MethodGet, h.tokenA, resultID, ""),
		h.feedbackCall(http.MethodPost, h.tokenA, resultID, feedbackRequestBody("yes", "yes")),
	} {
		if call.Code != http.StatusNotFound || !strings.Contains(call.Body.String(), `"code":"not_found"`) {
			t.Fatalf("feedback on an unowned result = %d %s, want 404 not_found", call.Code, call.Body.String())
		}
	}
	if !strings.Contains(h.logs[0].String(), `"outcome":"not_owned"`) {
		t.Fatalf("the refusal is not named not_owned:\n%s", h.logs[0].String())
	}
}

func TestResultFeedbackRoutes_aWithdrawnGrantIsNotFoundAndAnUndecidableReadIs503(t *testing.T) {
	h := threadHarness(t)
	resultID := servedResultID(t, h.post(0, h.tokenA, "submission-11950-0003", "Which repositories changed?"))
	h.grantWithdrawn.Store(true)
	requireReadByIDAnswer(t, h, h.tokenA, resultID, h.feedbackCall(http.MethodPost, h.tokenA, resultID, feedbackRequestBody("yes", "yes")), http.StatusNotFound)
	h.grantWithdrawn.Store(false)
	h.graphDown.Store(true)
	refused := h.feedbackCall(http.MethodPost, h.tokenA, resultID, feedbackRequestBody("yes", "yes"))
	if refused.Code != http.StatusServiceUnavailable {
		t.Fatalf("an undecidable read = %d %s, want 503", refused.Code, refused.Body.String())
	}
	if !strings.Contains(h.logs[0].String(), `"outcome":"gate_unavailable"`) {
		t.Fatalf("the refusal is not named gate_unavailable:\n%s", h.logs[0].String())
	}
	h.graphDown.Store(false)
	// Neither refused set wrote anything.
	if read := readFeedbackBody(t, h.feedbackCall(http.MethodGet, h.tokenA, resultID, "")); read.UpdatedAt != nil {
		t.Fatalf("a refused set wrote feedback: %+v", read)
	}
}

func TestResultFeedbackRoutes_aDeletedThreadsResultTakesNoFeedback(t *testing.T) {
	h := threadHarness(t)
	resultID := servedResultID(t, h.turn(h.tokenA, "submission-11950-0004", threadTestID, "Which teams own the ops repository?", "30"))
	readFeedbackBody(t, h.feedbackCall(http.MethodPost, h.tokenA, resultID, feedbackRequestBody("no", "yes")))
	if deleted := h.threadCall(http.MethodDelete, h.tokenA, threadPath(threadTestID)); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	requireReadByIDAnswer(t, h, h.tokenA, resultID, h.feedbackCall(http.MethodGet, h.tokenA, resultID, ""), http.StatusNotFound)
	requireReadByIDAnswer(t, h, h.tokenA, resultID, h.feedbackCall(http.MethodPost, h.tokenA, resultID, feedbackRequestBody("yes", "yes")), http.StatusNotFound)
}

// A malformed body is 400 before anything about the result is decided, so
// it is the same answer for the caller's own result and for an unknown id.
func TestResultFeedbackRoutes_aMalformedBodyIs400WhateverTheResult(t *testing.T) {
	h := threadHarness(t)
	resultID := servedResultID(t, h.post(0, h.tokenA, "submission-11950-0005", "Who reviews the most?"))
	for name, body := range map[string]string{
		"missing flag":   `{"schema_version":"context_fabric_result_feedback_request.v1","correct":"yes"}`,
		"null flag":      `{"schema_version":"context_fabric_result_feedback_request.v1","correct":null,"useful":"no"}`,
		"unknown value":  feedbackRequestBody("yes", "maybe"),
		"unknown key":    `{"schema_version":"context_fabric_result_feedback_request.v1","correct":"yes","useful":"no","comment":"free text"}`,
		"key twice":      `{"schema_version":"context_fabric_result_feedback_request.v1","correct":"yes","useful":"no","useful":"yes"}`,
		"wrong version":  `{"schema_version":"context_fabric_result_feedback.v1","correct":"yes","useful":"no"}`,
		"not json":       `correct=yes`,
		"trailing value": feedbackRequestBody("yes", "no") + `{}`,
	} {
		for _, id := range []string{resultID, "result_11950_unknown"} {
			got := h.feedbackCall(http.MethodPost, h.tokenA, id, body)
			if got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), `"code":"invalid_request"`) {
				t.Fatalf("%s on %s = %d %s, want 400 invalid_request", name, id, got.Code, got.Body.String())
			}
		}
	}
	if read := readFeedbackBody(t, h.feedbackCall(http.MethodGet, h.tokenA, resultID, "")); read.UpdatedAt != nil {
		t.Fatalf("a refused body wrote feedback: %+v", read)
	}
}

// floodingFeedbackStore is a memory store whose Get answers one result with
// a limitation list that fills the cap with service disclosures, so the read
// by id withholds it (the flood rule).
type floodingFeedbackStore struct {
	*memoryinvestigation.Store
	flooded atomic.Value // result id
}

func (s *floodingFeedbackStore) Get(ctx context.Context, principal storage.Principal, resultID string) (contextfabric.StoredInvestigationResult, error) {
	stored, err := s.Store.Get(ctx, principal, resultID)
	if id, _ := s.flooded.Load().(string); err == nil && id == resultID {
		stored.Result.Limitations = storedRouteGapLookalikes(contractsv1.ContextFabricLimitationsMaxCount)
	}
	return stored, err
}

// The flood rule holds on the feedback route by behaviour, not by source
// text: a result the read by id withholds for its flooded limitations gets
// the read by id's own 404 on GET and POST, and the POST writes nothing.
func TestResultFeedbackRoutes_aFloodWithheldResultTakesNoFeedback(t *testing.T) {
	// The store reads on the harness clock: a record with no thread closes
	// 24 hours after its admission (CHAOS-11952).
	var clock func() time.Time
	store := &floodingFeedbackStore{Store: memoryinvestigation.NewStoreWithClock(func() time.Time { return clock() })}
	h := newSubmissionHarness(t, store)
	clock = h.clock.Now
	resultID := servedResultID(t, h.post(0, h.tokenA, "submission-11950-0006", "Which repositories changed?"))
	readFeedbackBody(t, h.feedbackCall(http.MethodPost, h.tokenA, resultID, feedbackRequestBody("yes", "yes")))
	store.flooded.Store(resultID)
	requireReadByIDAnswer(t, h, h.tokenA, resultID, h.feedbackCall(http.MethodGet, h.tokenA, resultID, ""), http.StatusNotFound)
	requireReadByIDAnswer(t, h, h.tokenA, resultID, h.feedbackCall(http.MethodPost, h.tokenA, resultID, feedbackRequestBody("no", "no")), http.StatusNotFound)
	named := 0
	for _, line := range strings.Split(h.logs[0].String(), "\n") {
		if strings.Contains(line, `"msg":"context fabric result feedback"`) && strings.Contains(line, `"outcome":"not_served"`) && strings.Contains(line, `"serving":"flood_withheld"`) {
			named++
		}
	}
	if named != 2 {
		t.Fatalf("%d feedback lines name not_served / flood_withheld, want 2 (GET and POST):\n%s", named, h.logs[0].String())
	}
	store.flooded.Store("")
	read := readFeedbackBody(t, h.feedbackCall(http.MethodGet, h.tokenA, resultID, ""))
	if read.Correct != contractsv1.ContextFabricResultFeedbackYes || read.Useful != contractsv1.ContextFabricResultFeedbackYes {
		t.Fatalf("the refused POST wrote feedback: %+v", read)
	}
}
