package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The Ask Dev conversation routes (CHAOS-11948) on the submission harness:
// one memory store, two principals of one organization with the same grant.

const threadTestID = "thread-11948-ask-dev"

// turn posts one thread turn: submission id, thread id, question and the
// retention header (none when "").
func (h *submissionHarness) turn(token, submissionID, threadID, question, retention string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.turnOn(0, token, submissionID, threadID, question, retention)
}

// turnOn is turn through app index.
func (h *submissionHarness) turnOn(app int, token, submissionID, threadID, question, retention string) *httptest.ResponseRecorder {
	h.t.Helper()
	body := investigationRequestBody()
	body.Question = question
	encoded, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	if submissionID != "" {
		request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, submissionID)
	}
	if threadID != "" {
		request.Header.Set(contractsv1.ContextFabricThreadIDHeader, threadID)
	}
	if retention != "" {
		request.Header.Set(contractsv1.ContextFabricThreadRetentionHeader, retention)
	}
	recorder := httptest.NewRecorder()
	h.apps[app].Handler().ServeHTTP(recorder, request)
	return recorder
}

func (h *submissionHarness) threadCall(method, token, path string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.threadCallOn(0, method, token, path)
}

// threadCallOn is threadCall through app index.
func (h *submissionHarness) threadCallOn(app int, method, token, path string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	recorder := httptest.NewRecorder()
	h.apps[app].Handler().ServeHTTP(recorder, request)
	return recorder
}

func threadPath(threadID string) string {
	return strings.Replace(ContextFabricThreadPath, "{thread_id}", threadID, 1)
}

func readThreadBody(t *testing.T, recorder *httptest.ResponseRecorder) contractsv1.ContextFabricThread {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("thread read = %d %s", recorder.Code, recorder.Body.String())
	}
	thread, err := contractsv1.ParseContextFabricThread(recorder.Body.Bytes())
	if err != nil {
		t.Fatalf("the served thread fails its contract: %v (%s)", err, recorder.Body.String())
	}
	return thread
}

func readThreadListBody(t *testing.T, recorder *httptest.ResponseRecorder) contractsv1.ContextFabricThreadList {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("thread list = %d %s", recorder.Code, recorder.Body.String())
	}
	list, err := contractsv1.ParseContextFabricThreadList(recorder.Body.Bytes())
	if err != nil {
		t.Fatalf("the served list fails its contract: %v (%s)", err, recorder.Body.String())
	}
	return list
}

// threadHarness builds a harness whose memory store reads the harness clock,
// so the closed-thread rule on a result read follows the clock the routes
// pass.
func threadHarness(t *testing.T) *submissionHarness {
	t.Helper()
	var h *submissionHarness
	store := memoryinvestigation.NewStoreWithClock(func() time.Time { return h.clock.Now() })
	h = newSubmissionHarness(t, store)
	return h
}

func TestThreadRoutes_listReadAndDelete(t *testing.T) {
	h := threadHarness(t)
	first := h.turn(h.tokenA, "submission-11948-0001", threadTestID, "Which teams own the ops repository?", "")
	firstResult := servedResultID(t, first)
	h.clock.Advance(time.Minute)
	servedResultID(t, h.turn(h.tokenA, "submission-11948-0002", threadTestID, "And last month?", "30"))
	h.clock.Advance(time.Minute)
	servedResultID(t, h.turn(h.tokenA, "submission-11948-0003", threadTestID, "Compare with the quarter before.", ""))

	list := readThreadListBody(t, h.threadCall(http.MethodGet, h.tokenA, ContextFabricThreadsPath))
	if len(list.Threads) != 1 || list.NextCursor != "" {
		t.Fatalf("list = %+v", list)
	}
	summary := list.Threads[0]
	if summary.ThreadID != threadTestID || summary.TurnCount != 3 || summary.RetentionDays != contractsv1.ContextFabricThreadRetentionMonth {
		t.Fatalf("summary = %+v", summary)
	}

	thread := readThreadBody(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)))
	if len(thread.Turns) != 3 || thread.EarlierTurnsCursor != "" || thread.Turns[0].Question != "Which teams own the ops repository?" ||
		thread.Turns[0].ResultID != firstResult || thread.Turns[2].State != contractsv1.ContextFabricSubmissionDone {
		t.Fatalf("thread = %+v", thread)
	}

	if response := h.threadCall(http.MethodDelete, h.tokenA, threadPath(threadTestID)); response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("delete = %d %s", response.Code, response.Body.String())
	}
	expectErrorCode(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)), http.StatusNotFound, "not_found")
	if list := readThreadListBody(t, h.threadCall(http.MethodGet, h.tokenA, ContextFabricThreadsPath)); len(list.Threads) != 0 {
		t.Fatalf("list after delete = %+v", list)
	}
	// Idempotent: a second delete answers the same.
	if response := h.threadCall(http.MethodDelete, h.tokenA, threadPath(threadTestID)); response.Code != http.StatusNoContent {
		t.Fatalf("second delete = %d", response.Code)
	}
	// Closed everywhere (design §6 T6): the turn's status, its result by id.
	expectErrorCode(t, h.status(0, h.tokenA, "submission-11948-0001"), http.StatusNotFound, "not_found")
	expectErrorCode(t, h.get(0, h.tokenA, firstResult), http.StatusNotFound, "not_found")
	// T1: a new turn and a repeat of a turn's key are refused, and nothing ran.
	executions := h.investigator.executions.Load()
	expectErrorCode(t, h.turn(h.tokenA, "submission-11948-0004", threadTestID, "One more?", ""), http.StatusConflict, contractsv1.ErrorCodeThreadClosed)
	expectErrorCode(t, h.turn(h.tokenA, "submission-11948-0001", threadTestID, "Which teams own the ops repository?", ""), http.StatusConflict, contractsv1.ErrorCodeThreadClosed)
	if got := h.investigator.executions.Load(); got != executions {
		t.Fatalf("a closed thread's turn ran: %d executions, want %d", got, executions)
	}
	if !strings.Contains(h.logs[0].String(), `"outcome":"refused_thread_closed"`) || !strings.Contains(h.logs[0].String(), `"thread_closed_reason":"deleted"`) {
		t.Fatalf("the refusal is not on the admission line:\n%s", h.logs[0].String())
	}
}

func TestThreadRoutes_existenceNeutralAcrossPrincipals(t *testing.T) {
	h := threadHarness(t)
	servedResultID(t, h.turn(h.tokenA, "submission-11948-a001", threadTestID, "Owner question", ""))
	unknown := h.threadCall(http.MethodGet, h.tokenB, threadPath("thread-11948-unknown"))
	foreign := h.threadCall(http.MethodGet, h.tokenB, threadPath(threadTestID))
	malformed := h.threadCall(http.MethodGet, h.tokenB, threadPath("bad"))
	for name, response := range map[string]*httptest.ResponseRecorder{"unknown": unknown, "foreign": foreign, "malformed": malformed} {
		expectErrorCode(t, response, http.StatusNotFound, "not_found")
		if !bytes.Equal(response.Body.Bytes(), unknown.Body.Bytes()) {
			t.Fatalf("%s answer differs from the unknown-id answer: %s vs %s", name, response.Body.String(), unknown.Body.String())
		}
	}
	// B's delete of A's thread id touches nothing of A's.
	if response := h.threadCall(http.MethodDelete, h.tokenB, threadPath(threadTestID)); response.Code != http.StatusNoContent {
		t.Fatalf("foreign delete = %d", response.Code)
	}
	readThreadBody(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)))
	// B's turn with the same thread id is B's own thread.
	servedResultID(t, h.turn(h.tokenB, "submission-11948-b001", threadTestID, "Stranger question", ""))
	a := readThreadBody(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)))
	b := readThreadBody(t, h.threadCall(http.MethodGet, h.tokenB, threadPath(threadTestID)))
	if a.TurnCount != 1 || b.TurnCount != 1 || a.Turns[0].Question != "Owner question" || b.Turns[0].Question != "Stranger question" {
		t.Fatalf("threads mixed across principals: a=%+v b=%+v", a, b)
	}
	if list := readThreadListBody(t, h.threadCall(http.MethodGet, h.tokenB, ContextFabricThreadsPath)); len(list.Threads) != 1 || list.Threads[0].TurnCount != 1 {
		t.Fatalf("B's list = %+v", list)
	}
}

func TestThreadRoutes_headersAreRefusedNotIgnored(t *testing.T) {
	h := threadHarness(t)
	for name, tc := range map[string]struct{ submission, thread, retention string }{
		"thread without a submission id":    {"", threadTestID, ""},
		"retention without a thread":        {"submission-11948-h001", "", "30"},
		"retention outside the closed set":  {"submission-11948-h002", threadTestID, "7"},
		"retention with leading zero":       {"submission-11948-h003", threadTestID, "00"},
		"retention as a unit":               {"submission-11948-h004", threadTestID, "30d"},
		"thread without submission, with 0": {"", threadTestID, "0"},
	} {
		t.Run(name, func(t *testing.T) {
			expectErrorCode(t, h.turn(h.tokenA, tc.submission, tc.thread, "question", tc.retention), http.StatusBadRequest, "invalid_request")
		})
	}
	if got := h.investigator.executions.Load(); got != 0 {
		t.Fatalf("a refused header ran %d investigations", got)
	}
}

func TestThreadRoutes_doneTurnNamesItsResultOnlyWhenServable(t *testing.T) {
	h := threadHarness(t)
	resultID := servedResultID(t, h.turn(h.tokenA, "submission-11948-g001", threadTestID, "Gate question", "30"))
	if thread := readThreadBody(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID))); thread.Turns[0].ResultID != resultID || thread.Turns[0].ResultWithheld {
		t.Fatalf("before the grant moves: %+v", thread.Turns[0])
	}
	// The live grant no longer admits the stored subjects: the read by id
	// refuses the result, so the thread withholds its id.
	h.grantWithdrawn.Store(true)
	expectErrorCode(t, h.get(0, h.tokenA, resultID), http.StatusNotFound, "not_found")
	thread := readThreadBody(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)))
	if thread.Turns[0].ResultID != "" || !thread.Turns[0].ResultWithheld {
		t.Fatalf("after the grant moves: %+v", thread.Turns[0])
	}
	if !strings.Contains(h.logs[0].String(), `"withheld_denied":1`) {
		t.Fatalf("the thread line does not count the denial:\n%s", h.logs[0].String())
	}
	// An undecidable serving decision fails the whole read.
	h.grantWithdrawn.Store(false)
	h.graphDown.Store(true)
	expectErrorCode(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)), http.StatusServiceUnavailable, "upstream_unavailable")
	if !strings.Contains(h.logs[0].String(), `"outcome":"gate_unavailable"`) {
		t.Fatalf("no gate_unavailable line:\n%s", h.logs[0].String())
	}
}

func TestThreadRoutes_retentionExpiryClosesTheThread(t *testing.T) {
	h := threadHarness(t)
	resultID := servedResultID(t, h.turn(h.tokenA, "submission-11948-e001", threadTestID, "Expiring question", ""))
	thread := readThreadBody(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)))
	if thread.RetentionDays != contractsv1.ContextFabricThreadRetentionDay || !thread.ExpiresAt.Equal(thread.LastTurnAt.Add(24*time.Hour)) {
		t.Fatalf("thread = %+v", thread)
	}
	h.clock.Advance(24 * time.Hour)
	expectErrorCode(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)), http.StatusNotFound, "not_found")
	expectErrorCode(t, h.get(0, h.tokenA, resultID), http.StatusNotFound, "not_found")
	expectErrorCode(t, h.status(0, h.tokenA, "submission-11948-e001"), http.StatusNotFound, "not_found")
	expectErrorCode(t, h.turn(h.tokenA, "submission-11948-e002", threadTestID, "Late question", ""), http.StatusConflict, contractsv1.ErrorCodeThreadClosed)
	if !strings.Contains(h.logs[0].String(), `"thread_closed_reason":"expired"`) {
		t.Fatalf("no expired reason:\n%s", h.logs[0].String())
	}
}

func TestThreadRoutes_cursorsArePagedAndBound(t *testing.T) {
	h := threadHarness(t)
	for i := range 3 {
		h.clock.Advance(time.Minute)
		servedResultID(t, h.turn(h.tokenA, fmt.Sprintf("submission-11948-l%03d", i), fmt.Sprintf("thread-11948-list-%d", i), "q", ""))
	}
	first := readThreadListBody(t, h.threadCall(http.MethodGet, h.tokenA, ContextFabricThreadsPath+"?limit=2"))
	if len(first.Threads) != 2 || first.NextCursor == "" || first.Threads[0].ThreadID != "thread-11948-list-2" {
		t.Fatalf("first page = %+v", first)
	}
	second := readThreadListBody(t, h.threadCall(http.MethodGet, h.tokenA, ContextFabricThreadsPath+"?limit=2&cursor="+first.NextCursor))
	if len(second.Threads) != 1 || second.NextCursor != "" || second.Threads[0].ThreadID != "thread-11948-list-0" {
		t.Fatalf("second page = %+v", second)
	}
	// Another principal's cursor, a malformed one and bad limits are refused.
	for name, query := range map[string]string{
		"foreign cursor": "?cursor=" + first.NextCursor,
		"bad cursor":     "?cursor=a+b",
		"empty cursor":   "?cursor=",
		"limit 0":        "?limit=0",
		"limit 51":       "?limit=51",
		"limit 1.0":      "?limit=1.0",
		"limit twice":    "?limit=1&limit=2",
	} {
		token := h.tokenA
		if name == "foreign cursor" {
			token = h.tokenB
		}
		t.Run(name, func(t *testing.T) {
			expectErrorCode(t, h.threadCall(http.MethodGet, token, ContextFabricThreadsPath+query), http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestThreadRoutes_earlierTurnsArePagedByCursor(t *testing.T) {
	h := threadHarness(t)
	total := contractsv1.ContextFabricThreadMaxServedTurns + 3
	for i := range total {
		h.clock.Advance(time.Second)
		servedResultID(t, h.turn(h.tokenA, fmt.Sprintf("submission-11948-p%04d", i), threadTestID, fmt.Sprintf("q%d", i), ""))
	}
	latest := readThreadBody(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)))
	if latest.TurnCount != total || len(latest.Turns) != contractsv1.ContextFabricThreadMaxServedTurns || latest.EarlierTurnsCursor == "" || latest.Turns[0].Question != "q3" {
		t.Fatalf("latest page: count %d turns %d cursor %q first %q", latest.TurnCount, len(latest.Turns), latest.EarlierTurnsCursor, latest.Turns[0].Question)
	}
	earlier := readThreadBody(t, h.threadCall(http.MethodGet, h.tokenA, threadPath(threadTestID)+"?before="+latest.EarlierTurnsCursor))
	if len(earlier.Turns) != 3 || earlier.EarlierTurnsCursor != "" || earlier.Turns[0].Question != "q0" || earlier.Turns[2].Question != "q2" {
		t.Fatalf("earlier page = %+v", earlier.Turns)
	}
	// The cursor is bound to its thread and its owner.
	expectErrorCode(t, h.threadCall(http.MethodGet, h.tokenB, threadPath(threadTestID)+"?before="+latest.EarlierTurnsCursor), http.StatusBadRequest, "invalid_request")
	servedResultID(t, h.turn(h.tokenA, "submission-11948-other", "thread-11948-other", "q", ""))
	expectErrorCode(t, h.threadCall(http.MethodGet, h.tokenA, threadPath("thread-11948-other")+"?before="+latest.EarlierTurnsCursor), http.StatusBadRequest, "invalid_request")
}

func TestThreadRoutes_refuseWithoutAStore(t *testing.T) {
	h := threadHarness(t)
	h.apps[0].runtime.InvestigationThreads = nil
	for _, call := range []struct{ method, path string }{{http.MethodGet, ContextFabricThreadsPath}, {http.MethodGet, threadPath(threadTestID)}, {http.MethodDelete, threadPath(threadTestID)}} {
		if response := h.threadCall(call.method, h.tokenA, call.path); response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s without a store = %d %s", call.method, call.path, response.Code, response.Body.String())
		}
	}
	// Unauthenticated first: no store state leaks.
	request := httptest.NewRequest(http.MethodGet, ContextFabricThreadsPath, nil)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	recorder := httptest.NewRecorder()
	h.apps[0].Handler().ServeHTTP(recorder, request)
	expectErrorCode(t, recorder, http.StatusUnauthorized, "invalid_token")
}
