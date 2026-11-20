package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	<-holding.held
	status := harness.statusBody(0, harness.tokenA, "submission-async-win")
	if status.State != contractsv1.ContextFabricSubmissionAbandoned || status.Terminal != nil {
		t.Fatalf("status %+v / %+v, want abandoned with no terminal before the log holds it", status, status.Terminal)
	}
	close(holding.release)
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
