package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Accept-first create (Prefer: respond-async) on the memory store: the
// record is written under the run lease before the 202, the run goes on with
// no viewer, and the submission status serves its stage and its end.

// respondAsync posts an accept-first create of submissionID to server as
// token and returns the response with its body read.
func (h *submissionHarness) respondAsync(server *httptest.Server, token, submissionID string, extra func(*http.Request)) (*http.Response, []byte) {
	h.t.Helper()
	request := h.streamRequest(server, token, submissionID)
	request.Header.Set(contractsv1.ContextFabricPreferHeader, contractsv1.ContextFabricRespondAsyncPreference)
	if extra != nil {
		extra(request)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := http.DefaultClient.Do(request.WithContext(ctx))
	if err != nil {
		h.t.Fatalf("accept-first create did not answer while the run is held: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatalf("accept-first body: %v", err)
	}
	return response, body
}

// statusBody is the decoded 200 status of submissionID on app as token.
func (h *submissionHarness) statusBody(app int, token, submissionID string) contractsv1.ContextFabricSubmissionStatus {
	h.t.Helper()
	recorder := h.status(app, token, submissionID)
	if recorder.Code != http.StatusOK {
		h.t.Fatalf("status of %s = %d %s", submissionID, recorder.Code, recorder.Body.String())
	}
	var status contractsv1.ContextFabricSubmissionStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		h.t.Fatal(err)
	}
	if err := status.Validate(); err != nil {
		h.t.Fatalf("status of %s fails its contract: %v", submissionID, err)
	}
	return status
}

func TestRespondAsyncRecordBeforeResponse(t *testing.T) {
	harness, store, logs := newLeaseHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	response, body := harness.respondAsync(server, harness.tokenA, "submission-async-001", nil)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("accept-first create = %d %s", response.StatusCode, body)
	}
	if got := response.Header.Get(contractsv1.ContextFabricPreferenceAppliedHeader); got != contractsv1.ContextFabricRespondAsyncPreference {
		t.Fatalf("Preference-Applied = %q", got)
	}
	var accepted contractsv1.ContextFabricSubmissionStatus
	if err := json.Unmarshal(body, &accepted); err != nil {
		t.Fatal(err)
	}
	if err := accepted.Validate(); err != nil {
		t.Fatalf("202 body fails the status contract: %v", err)
	}
	if accepted.State != contractsv1.ContextFabricSubmissionRunning || accepted.SubmissionID != "submission-async-001" || accepted.Terminal != nil {
		t.Fatalf("202 body %+v, want the running record", accepted)
	}
	// The run is held inside the investigator: the 202 came first.
	waitEntered(t, harness.investigator.entered)
	principal, _ := harness.investigator.lastPrincipal.Load().(storage.Principal)
	key, err := contextfabric.SubmissionKeyFor(principal, "submission-async-001")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.GetSubmission(context.Background(), key, harness.clock.Now())
	if err != nil || record.State != contextfabric.SubmissionRunning || !record.Leased() {
		t.Fatalf("record %+v (%v), want running under a lease", record, err)
	}
	if record.ReservedResultID != accepted.ReservedResultID {
		t.Fatalf("202 reserved %q, record %q", accepted.ReservedResultID, record.ReservedResultID)
	}
	opened := logs[0].waitLine(t, "context fabric investigation event stream opened", nil)
	if opened["respond_async"] != true || opened["leased"] != true {
		t.Fatalf("opened line %v, want respond_async and leased", opened)
	}
	close(harness.investigator.release)
	waitFor(t, func() bool {
		return harness.statusBody(0, harness.tokenA, "submission-async-001").State == contractsv1.ContextFabricSubmissionDone
	})
	// The run had no viewer from its start: it never tried the connection.
	if detached := logs[0].lines("context fabric investigation event stream viewer detached"); len(detached) != 0 {
		t.Fatalf("an accept-first run wrote to a connection: %v", detached)
	}
	done := harness.statusBody(0, harness.tokenA, "submission-async-001")
	if done.Terminal == nil || done.Terminal.Kind != contractsv1.ContextFabricSubmissionTerminalResult || done.ResultID == "" {
		t.Fatalf("done status %+v, want terminal result", done)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
	// The result reads by id like any stored result.
	if recorder := harness.get(0, harness.tokenA, done.ResultID); recorder.Code != http.StatusOK {
		t.Fatalf("result read = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestRespondAsyncStatusOtherOrgAndOtherUserNotFound(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	if response, body := harness.respondAsync(server, harness.tokenA, "submission-async-own", nil); response.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	waitEntered(t, harness.investigator.entered)
	otherOrg := issueOrgCredential(t, harness.credentials, "org_2", harness.clock.Now(), []string{auth.ScopeContextRead}, []string{hostedTestRepository})
	unknown := harness.status(0, harness.tokenA, "submission-unknown-9")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d", unknown.Code)
	}
	for name, token := range map[string]string{"other user, same org": harness.tokenB, "other org": otherOrg} {
		recorder := harness.status(0, token, "submission-async-own")
		if recorder.Code != http.StatusNotFound || !bytes.Equal(recorder.Body.Bytes(), unknown.Body.Bytes()) {
			t.Fatalf("%s: status = %d %s, want the unknown-id answer %s", name, recorder.Code, recorder.Body.String(), unknown.Body.String())
		}
		if cancel := harness.cancelRun(0, token, "submission-async-own"); cancel.Code != http.StatusNotFound {
			t.Fatalf("%s: cancel = %d", name, cancel.Code)
		}
	}
	if own := harness.statusBody(0, harness.tokenA, "submission-async-own"); own.State != contractsv1.ContextFabricSubmissionRunning {
		t.Fatalf("own status %+v after the others' reads", own)
	}
	close(harness.investigator.release)
}

func TestRespondAsyncStatusRefreshedCredentialSameRecord(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	source := harness.issueDeviceCredential(t, "user_1")
	if response, body := harness.respondAsync(server, source.Token, "submission-async-rot", nil); response.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	waitEntered(t, harness.investigator.entered)
	successor := harness.rotateSelf(t, source)
	if status := harness.statusBody(0, successor, "submission-async-rot"); status.State != contractsv1.ContextFabricSubmissionRunning {
		t.Fatalf("successor's status %+v, want the running record", status)
	}
	close(harness.investigator.release)
	waitFor(t, func() bool {
		return harness.statusBody(0, successor, "submission-async-rot").State == contractsv1.ContextFabricSubmissionDone
	})
}

func TestRespondAsyncCancelReachesRun(t *testing.T) {
	harness, _, logs := newLeaseHarness(t, 2)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	if response, body := harness.respondAsync(server, harness.tokenA, "submission-async-cxl", nil); response.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	waitEntered(t, harness.investigator.entered)
	if recorder := harness.cancelRun(1, harness.tokenA, "submission-async-cxl"); recorder.Code != http.StatusAccepted {
		t.Fatalf("cancel on the other replica = %d %s", recorder.Code, recorder.Body.String())
	}
	effective := logs[0].waitLine(t, "context fabric investigation cancel effective", nil)
	if effective["cause"] != "cancel_requested" {
		t.Fatalf("cancel effective %v, want cause cancel_requested", effective)
	}
	waitFor(t, func() bool {
		return harness.statusBody(1, harness.tokenA, "submission-async-cxl").Terminal != nil
	})
	status := harness.statusBody(1, harness.tokenA, "submission-async-cxl")
	if status.State != contractsv1.ContextFabricSubmissionAbandoned || status.Terminal.Kind != contractsv1.ContextFabricSubmissionTerminalError || status.Terminal.ErrorCode != contractsv1.ErrorCodeCanceled {
		t.Fatalf("status %+v / %+v, want abandoned + canceled", status, status.Terminal)
	}
}

// lostAsyncRun starts an accept-first run on app 0 whose holder then stalls,
// and moves the clock past the lease: the run is lost.
func lostAsyncRun(t *testing.T, apps int, submissionID string) (*submissionHarness, *stallingRunStore, []*lockedBuffer) {
	t.Helper()
	harness, store, logs := newLeaseHarness(t, apps)
	stalled := &stallingRunStore{RunStore: store}
	harness.apps[0].runtime.InvestigationRuns = stalled
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	t.Cleanup(stalled.resume)
	if response, body := harness.respondAsync(server, harness.tokenA, submissionID, nil); response.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	waitEntered(t, harness.investigator.entered)
	stalled.stall()
	harness.clock.Advance(contextfabric.RunLeaseTTL + time.Second)
	return harness, stalled, logs
}

func TestRespondAsyncStatusLostLeaseRunLost(t *testing.T) {
	harness, _, _ := lostAsyncRun(t, 2, "submission-async-lst")
	status := harness.statusBody(1, harness.tokenA, "submission-async-lst")
	if status.State != contractsv1.ContextFabricSubmissionAbandoned || status.Terminal == nil ||
		status.Terminal.Kind != contractsv1.ContextFabricSubmissionTerminalError || status.Terminal.ErrorCode != contractsv1.ErrorCodeRunLost {
		t.Fatalf("status %+v / %+v, want abandoned + run_lost", status, status.Terminal)
	}
}

func TestRespondAsyncStatusLostLeaseTakeoverOnce(t *testing.T) {
	harness, _, logs := lostAsyncRun(t, 3, "submission-async-tk1")
	var wg sync.WaitGroup
	statuses := make([]contractsv1.ContextFabricSubmissionStatus, 2)
	codes := make([]int, 2)
	for index, app := range []int{1, 2} {
		wg.Add(1)
		go func(index, app int) {
			defer wg.Done()
			recorder := harness.status(app, harness.tokenA, "submission-async-tk1")
			codes[index] = recorder.Code
			_ = json.Unmarshal(recorder.Body.Bytes(), &statuses[index])
		}(index, app)
	}
	wg.Wait()
	for index, status := range statuses {
		if codes[index] != http.StatusOK || status.State != contractsv1.ContextFabricSubmissionAbandoned || status.Terminal == nil || status.Terminal.ErrorCode != contractsv1.ErrorCodeRunLost {
			t.Fatalf("read %d = %d %+v / %+v, want abandoned + run_lost", index, codes[index], status, status.Terminal)
		}
	}
	takeovers := len(logs[1].lines(ContextFabricRunTakeoverLogMessage)) + len(logs[2].lines(ContextFabricRunTakeoverLogMessage))
	if takeovers != 1 {
		t.Fatalf("%d takeover lines over the two reads, want exactly 1", takeovers)
	}
	run := contextfabric.RunKey{OrgID: "org_1", ReservedResultID: statuses[0].ReservedResultID}
	page, err := harness.apps[1].investigationRuns().ReadRunEvents(context.Background(), run, 0, contextfabric.RunEventLogMaxEvents)
	if err != nil {
		t.Fatal(err)
	}
	terminals := 0
	for _, event := range page.Events {
		if event.Terminal != nil {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("%d terminal events in the log, want exactly 1", terminals)
	}
}

func TestRespondAsyncStatusLastStage(t *testing.T) {
	harness, store, _ := newLeaseHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	_, body := harness.respondAsync(server, harness.tokenA, "submission-async-stg", nil)
	var accepted contractsv1.ContextFabricSubmissionStatus
	if err := json.Unmarshal(body, &accepted); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, harness.investigator.entered)
	if status := harness.statusBody(0, harness.tokenA, "submission-async-stg"); status.LastStage != "" {
		t.Fatalf("last_stage %q before any stage started", status.LastStage)
	}
	run := contextfabric.RunKey{OrgID: "org_1", ReservedResultID: accepted.ReservedResultID}
	page, err := store.ReadRunEvents(context.Background(), run, 0, contextfabric.RunEventLogMaxEvents)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendRunEvent(context.Background(), run, contextfabric.RunEvent{
		Seq: page.LastSeq + 1, Type: contractsv1.ContextFabricInvestigationEventHeartbeat,
		LastStageStarted: contractsv1.ContextFabricStageReadingFacts, Data: []byte(`{}`),
	}, contextfabric.RunEventLogMaxEvents, harness.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if status := harness.statusBody(0, harness.tokenA, "submission-async-stg"); status.LastStage != contractsv1.ContextFabricStageReadingFacts || status.Terminal != nil {
		t.Fatalf("status %+v, want last_stage reading_facts and no terminal", status)
	}
	close(harness.investigator.release)
}

func TestRespondAsyncRefusals(t *testing.T) {
	t.Run("run lease off", func(t *testing.T) {
		harness, _ := newMemorySubmissionHarness(t, 1)
		harness.apps[0].config.ContextFabricEventStreamEnabled = true
		server := harness.serveApp(0)
		response, body := harness.respondAsync(server, harness.tokenA, "submission-async-off", nil)
		code, message := errorCodeOf(t, body)
		if response.StatusCode != http.StatusNotAcceptable || code != contractsv1.ErrorCodeStreamUnavailable || message != contractsv1.ContextFabricRespondAsyncUnavailableMessage {
			t.Fatalf("lease off = %d %s", response.StatusCode, body)
		}
		if harness.investigator.executions.Load() != 0 || harness.status(0, harness.tokenA, "submission-async-off").Code != http.StatusNotFound {
			t.Fatal("a refused accept-first create admitted or ran something")
		}
	})
	harness, _, _ := newLeaseHarness(t, 1)
	server := harness.serveApp(0)
	for name, tc := range map[string]struct {
		edit    func(*http.Request)
		message string
	}{
		"no submission id": {func(r *http.Request) { r.Header.Del(contractsv1.ContextFabricSubmissionIDHeader) }, "Prefer: respond-async needs an X-ACR-Submission-ID"},
		"with the stream": {func(r *http.Request) {
			r.Header.Set("Accept", contractsv1.ContextFabricInvestigationEventStreamMediaType)
		}, "Prefer: respond-async does not take Accept: text/event-stream"},
	} {
		response, body := harness.respondAsync(server, harness.tokenA, "submission-async-bad", tc.edit)
		code, message := errorCodeOf(t, body)
		if response.StatusCode != http.StatusBadRequest || code != "invalid_request" || message != tc.message {
			t.Fatalf("%s = %d %s", name, response.StatusCode, body)
		}
	}
	if harness.investigator.executions.Load() != 0 {
		t.Fatal("a refused accept-first create ran")
	}
}

func errorCodeOf(t *testing.T, body []byte) (string, string) {
	t.Helper()
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("error body %s: %v", body, err)
	}
	return envelope.Error.Code, envelope.Error.Message
}

func TestWantsRespondAsync(t *testing.T) {
	for _, tc := range []struct {
		values []string
		want   bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"respond-async"}, true},
		{[]string{"RESPOND-ASYNC"}, true},
		{[]string{" respond-async "}, true},
		{[]string{"respond-async; wait=10"}, true},
		{[]string{"return=minimal, respond-async"}, true},
		{[]string{"return=minimal", "respond-async"}, true},
		{[]string{"respond-asyncx"}, false},
		{[]string{"wait=10"}, false},
		{[]string{"return=respond-async"}, false},
		{[]string{"respond-async=1"}, true},
		{[]string{`include="status, respond-async, result"`}, false},
		{[]string{`include="a\"b, respond-async"`}, false},
		{[]string{`wait=10; note="respond-async, x"`}, false},
		{[]string{`x`, `include="respond-async"`}, false},
		{[]string{`include="respond-async=1"`}, false},
		{[]string{`include="respond-async; wait=1"`}, false},
		{[]string{`include="status, x", respond-async`}, true},
		{[]string{`include="a\"b", respond-async`}, true},
		{[]string{`include="respond-async`}, false},
		{[]string{`note="a\", respond-async`}, false},
	} {
		request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, nil)
		for _, value := range tc.values {
			request.Header.Add(contractsv1.ContextFabricPreferHeader, value)
		}
		if got := wantsRespondAsync(request); got != tc.want {
			t.Errorf("Prefer %q = %v, want %v", strings.Join(tc.values, " | "), got, tc.want)
		}
	}
}

// terminalHoldingRunStore holds every terminal append until released: the
// window in which the holder wrote the record but not yet the log's end.
type terminalHoldingRunStore struct {
	contextfabric.RunStore
	held    chan struct{}
	release chan struct{}
}

func (s *terminalHoldingRunStore) AppendRunEvent(ctx context.Context, run contextfabric.RunKey, event contextfabric.RunEvent, maxEvents int, now time.Time) (bool, error) {
	if event.Terminal != nil {
		s.held <- struct{}{}
		<-s.release
	}
	return s.RunStore.AppendRunEvent(ctx, run, event, maxEvents, now)
}

func TestRespondAsyncAbandonedBeforeLogTerminalNamesNoCause(t *testing.T) {
	harness, store, _ := newLeaseHarness(t, 1)
	holding := &terminalHoldingRunStore{RunStore: store, held: make(chan struct{}, 1), release: make(chan struct{})}
	harness.apps[0].runtime.InvestigationRuns = holding
	harness.investigator.mode.Store(int32(runFail))
	server := harness.serveApp(0)
	if response, body := harness.respondAsync(server, harness.tokenA, "submission-async-win", nil); response.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	var once sync.Once
	release := func() { once.Do(func() { close(holding.release) }) }
	t.Cleanup(release)
	<-holding.held
	status := harness.statusBody(0, harness.tokenA, "submission-async-win")
	if status.State != contractsv1.ContextFabricSubmissionAbandoned || status.Terminal != nil {
		t.Fatalf("status %+v / %+v, want abandoned with no terminal before the log holds it", status, status.Terminal)
	}
	release()
	waitFor(t, func() bool { return harness.statusBody(0, harness.tokenA, "submission-async-win").Terminal != nil })
	final := harness.statusBody(0, harness.tokenA, "submission-async-win")
	if final.Terminal.Kind != contractsv1.ContextFabricSubmissionTerminalError || final.Terminal.ErrorCode == "" ||
		final.Terminal.ErrorCode == contractsv1.ErrorCodeRunLost || final.Terminal.ErrorCode == contractsv1.ErrorCodeCanceled {
		t.Fatalf("terminal %+v, want the run's own error", final.Terminal)
	}
}

func TestStatusWithoutRunLeaseHasNoRunDetail(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	server := harness.serveApp(0)
	if response, body := harness.respondAsync(server, harness.tokenA, "submission-async-nol", nil); response.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	waitFor(t, func() bool { return harness.statusBody(0, harness.tokenA, "submission-async-nol").Terminal != nil })
	harness.apps[0].config.ContextFabricRunLeaseEnabled = false
	status := harness.statusBody(0, harness.tokenA, "submission-async-nol")
	if status.State != contractsv1.ContextFabricSubmissionDone || status.Terminal != nil || status.LastStage != "" {
		t.Fatalf("status %+v with the run lease off, want no run detail", status)
	}
}

// leaseDroppingSubmissions admits every record without its run lease.
type leaseDroppingSubmissions struct{ contextfabric.SubmissionStore }

func (s leaseDroppingSubmissions) AdmitSubmission(ctx context.Context, admission contextfabric.SubmissionAdmission, now time.Time) (contextfabric.SubmissionRecord, bool, error) {
	admission.Lease = nil
	return s.SubmissionStore.AdmitSubmission(ctx, admission, now)
}

// An accept-first run is never started without its lease: the client
// closes its connection after the 202, and a connection-owned run would end
// with it.
func TestRespondAsyncRefusesARecordAdmittedWithoutItsLease(t *testing.T) {
	harness, store, _ := newLeaseHarness(t, 1)
	harness.apps[0].runtime.InvestigationSubmissions = leaseDroppingSubmissions{SubmissionStore: store}
	server := harness.serveApp(0)
	response, body := harness.respondAsync(server, harness.tokenA, "submission-async-nls", nil)
	if code, _ := errorCodeOf(t, body); response.StatusCode != http.StatusInternalServerError || code != "internal_error" {
		t.Fatalf("create admitted without a lease = %d %s", response.StatusCode, body)
	}
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	if status := harness.statusBody(0, harness.tokenA, "submission-async-nls"); status.State != contractsv1.ContextFabricSubmissionAbandoned {
		t.Fatalf("status %+v, want abandoned", status)
	}
}

// The 202 frees its connection: on HTTP/1.1 keep-alive the next request on
// the SAME connection (a status poll, a cancel) is answered while the run is
// still held.
func TestRespondAsyncKeepAliveConnectionServesTheNextRequest(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	var dials atomic.Int32
	transport := &http.Transport{MaxConnsPerHost: 1, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	send := func(request *http.Request) (*http.Response, []byte, bool) {
		t.Helper()
		reused := false
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
		ctx, cancel := context.WithTimeout(httptrace.WithClientTrace(context.Background(), trace), 3*time.Second)
		defer cancel()
		response, err := client.Do(request.WithContext(ctx))
		if err != nil {
			t.Fatalf("%s %s on the kept-alive connection: %v", request.Method, request.URL.Path, err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("%s %s body: %v", request.Method, request.URL.Path, err)
		}
		return response, body, reused
	}
	create := harness.streamRequest(server, harness.tokenA, "submission-async-kpa")
	create.Header.Set(contractsv1.ContextFabricPreferHeader, contractsv1.ContextFabricRespondAsyncPreference)
	if response, body, _ := send(create); response.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	waitEntered(t, harness.investigator.entered)
	routed := func(method, path string) *http.Request {
		request, err := http.NewRequest(method, server.URL+strings.Replace(path, "{submission_id}", "submission-async-kpa", 1), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+harness.tokenA)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		request.Header.Set("X-Request-ID", submissionTestRequestID)
		return request
	}
	poll, body, reused := send(routed(http.MethodGet, ContextFabricSubmissionStatusPath))
	if poll.StatusCode != http.StatusOK || !reused || !bytes.Contains(body, []byte(`"state":"running"`)) {
		t.Fatalf("poll = %d reused=%v %s, want 200 running on the same connection", poll.StatusCode, reused, body)
	}
	cancelled, body, reused := send(routed(http.MethodPost, ContextFabricSubmissionCancelPath))
	if cancelled.StatusCode != http.StatusAccepted || !reused {
		t.Fatalf("cancel = %d reused=%v %s, want 202 on the same connection", cancelled.StatusCode, reused, body)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("%d connections dialed, want 1", got)
	}
	waitFor(t, func() bool { return harness.statusBody(0, harness.tokenA, "submission-async-kpa").Terminal != nil })
}

// The app drains its detached runs within the bound it is given.
func TestDrainDetachedRunsIsBounded(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	if response, body := harness.respondAsync(server, harness.tokenA, "submission-async-drn", nil); response.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	waitEntered(t, harness.investigator.entered)
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := harness.apps[0].DrainDetachedRuns(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain with a held run = %v, want the bound's deadline", err)
	}
	close(harness.investigator.release)
	long, cancelLong := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLong()
	if err := harness.apps[0].DrainDetachedRuns(long); err != nil {
		t.Fatalf("drain after the run ended = %v", err)
	}
}

// Past BOTH the lease and the submission expiry, a read still names the
// lost run: one run_lost terminal, however many reads race.
func TestRespondAsyncLateReadAfterBothExpiriesRunLostOnce(t *testing.T) {
	harness, _, _ := lostAsyncRun(t, 3, "submission-async-lat")
	harness.clock.Advance(10 * time.Minute)
	var wg sync.WaitGroup
	statuses := make([]contractsv1.ContextFabricSubmissionStatus, 2)
	codes := make([]int, 2)
	for index, app := range []int{1, 2} {
		wg.Add(1)
		go func(index, app int) {
			defer wg.Done()
			recorder := harness.status(app, harness.tokenA, "submission-async-lat")
			codes[index] = recorder.Code
			_ = json.Unmarshal(recorder.Body.Bytes(), &statuses[index])
		}(index, app)
	}
	wg.Wait()
	for index, status := range statuses {
		if codes[index] != http.StatusOK || status.State != contractsv1.ContextFabricSubmissionAbandoned || status.Terminal == nil || status.Terminal.ErrorCode != contractsv1.ErrorCodeRunLost {
			t.Fatalf("late read %d = %d %+v / %+v, want abandoned + run_lost", index, codes[index], status, status.Terminal)
		}
	}
	run := contextfabric.RunKey{OrgID: "org_1", ReservedResultID: statuses[0].ReservedResultID}
	page, err := harness.apps[1].investigationRuns().ReadRunEvents(context.Background(), run, 0, contextfabric.RunEventLogMaxEvents)
	if err != nil {
		t.Fatal(err)
	}
	terminals := 0
	for _, event := range page.Events {
		if event.Terminal != nil {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("%d terminal events in the log, want exactly 1", terminals)
	}
}

// A done record whose log holds no terminal yet names none: the delivery can
// still end in an error after the save. The terminal is the log's.
func TestRespondAsyncDoneBeforeLogTerminalNamesNone(t *testing.T) {
	harness, store, _ := newLeaseHarness(t, 1)
	holding := &terminalHoldingRunStore{RunStore: store, held: make(chan struct{}, 1), release: make(chan struct{})}
	harness.apps[0].runtime.InvestigationRuns = holding
	server := harness.serveApp(0)
	var once sync.Once
	release := func() { once.Do(func() { close(holding.release) }) }
	t.Cleanup(release)
	_, body := harness.respondAsync(server, harness.tokenA, "submission-async-dbt", nil)
	var accepted contractsv1.ContextFabricSubmissionStatus
	if err := json.Unmarshal(body, &accepted); err != nil {
		t.Fatal(err)
	}
	<-holding.held
	status := harness.statusBody(0, harness.tokenA, "submission-async-dbt")
	if status.State != contractsv1.ContextFabricSubmissionDone || status.Terminal != nil {
		t.Fatalf("status %+v / %+v, want done with no terminal before the log holds it", status, status.Terminal)
	}
	run := contextfabric.RunKey{OrgID: "org_1", ReservedResultID: accepted.ReservedResultID}
	page, err := store.ReadRunEvents(context.Background(), run, 0, contextfabric.RunEventLogMaxEvents)
	if err != nil {
		t.Fatal(err)
	}
	envelope := contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, RequestID: submissionTestRequestID,
		Error: contractsv1.ErrorDetail{Code: "budget_refusal", Message: "Context Fabric investigation response exceeded service limits", HTTPStatus: http.StatusRequestEntityTooLarge}}
	if appended, err := store.AppendRunEvent(context.Background(), run, contextfabric.RunEvent{
		Seq: page.LastSeq + 1, Type: contractsv1.ContextFabricInvestigationEventError,
		Terminal: &contextfabric.RunTerminal{Kind: contextfabric.RunTerminalError, Error: &envelope},
	}, contextfabric.RunEventLogMaxEvents, harness.clock.Now()); err != nil || !appended {
		t.Fatalf("error terminal append = %v %v", appended, err)
	}
	release()
	final := harness.statusBody(0, harness.tokenA, "submission-async-dbt")
	if final.State != contractsv1.ContextFabricSubmissionDone || final.Terminal == nil || final.Terminal.Kind != contractsv1.ContextFabricSubmissionTerminalError || final.Terminal.ErrorCode != "budget_refusal" {
		t.Fatalf("status %+v / %+v, want done with the log's error terminal", final, final.Terminal)
	}
}

// A client-synthesis turn needs its synthesis_input in the create's answer,
// which an accept-first create never delivers: refused before admission.
func TestRespondAsyncClientSynthesisRefusedBeforeAdmission(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	server := harness.serveApp(0)
	response, body := harness.respondAsync(server, harness.tokenA, "submission-async-cls", func(r *http.Request) {
		request := investigationRequestBody()
		request.Question = submissionQuestion
		request.SynthesisMode = contractsv1.ContextFabricSynthesisModeClient
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		r.Body = io.NopCloser(bytes.NewReader(encoded))
		r.ContentLength = int64(len(encoded))
	})
	code, message := errorCodeOf(t, body)
	if response.StatusCode != http.StatusBadRequest || code != "invalid_request" || message != "Prefer: respond-async does not take synthesis_mode client" {
		t.Fatalf("client synthesis = %d %s", response.StatusCode, body)
	}
	if harness.investigator.executions.Load() != 0 || harness.status(0, harness.tokenA, "submission-async-cls").Code != http.StatusNotFound {
		t.Fatal("a refused client-synthesis accept-first create admitted or ran something")
	}
}

// The browser route lets the web origin send Prefer and read
// Preference-Applied.
func TestRespondAsyncCORSAllowsPreferAndExposesPreferenceApplied(t *testing.T) {
	harness := corsHarness(t)
	preflight := harness.cors(0, corsCall{method: http.MethodOptions, path: ContextFabricInvestigationsPath, origin: corsTestOrigin, headers: map[string]string{
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "authorization, content-type, x-acr-submission-id, prefer",
	}})
	allowed := strings.ToLower(preflight.Header().Get("Access-Control-Allow-Headers"))
	if preflight.Code != http.StatusNoContent || !containsValue(strings.Split(strings.ReplaceAll(allowed, " ", ""), ","), "prefer") {
		t.Fatalf("preflight = %d allow-headers %q, want prefer allowed", preflight.Code, allowed)
	}
	actual := harness.cors(0, corsCall{method: http.MethodPost, path: ContextFabricInvestigationsPath, origin: corsTestOrigin, token: harness.tokenA,
		headers: map[string]string{"X-ACR-Client-Version": "1.0.0", "Content-Type": "application/json"}, body: createBody(t)})
	exposed := strings.Split(strings.ReplaceAll(actual.Header().Get("Access-Control-Expose-Headers"), " ", ""), ",")
	if !containsValue(exposed, contractsv1.ContextFabricPreferenceAppliedHeader) {
		t.Fatalf("expose-headers %q, want Preference-Applied", actual.Header().Get("Access-Control-Expose-Headers"))
	}
}
