package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-11913: the submission status read and the thread link, through the
// real routes. Each status body is validated against the published schema.

const submissionTestThreadID = "thread-0000-11913-ask-dev"

func decodeSubmissionStatus(t *testing.T, recorder *httptest.ResponseRecorder) contractsv1.ContextFabricSubmissionStatus {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status read = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	if err := contractcheck.ValidateSerializedStrict("", "context_fabric_submission_status.v1.schema.json", recorder.Body.Bytes()); err != nil {
		t.Fatalf("schema rejects the status body: %v\n%s", err, recorder.Body.String())
	}
	var status contractsv1.ContextFabricSubmissionStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return status
}

// The owner reads its record, with the final result id and the thread; any
// other principal of the same org and grant, an unknown id and a malformed id
// all get one answer.
func TestSubmissionStatusIsTheOwnersOwnRecord(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	served := servedResultID(t, harness.postThread(0, harness.tokenA, "submission-status-001", submissionTestThreadID, submissionQuestion))
	status := decodeSubmissionStatus(t, harness.status(1, harness.tokenA, "submission-status-001"))
	if status.State != contractsv1.ContextFabricSubmissionDone || status.ResultID != served || status.ReservedResultID != served || status.ThreadID != submissionTestThreadID || status.SubmissionID != "submission-status-001" {
		t.Fatalf("status = %+v, want done naming %s on thread %s", status, served, submissionTestThreadID)
	}
	other := harness.status(1, harness.tokenB, "submission-status-001")
	unknown := harness.status(1, harness.tokenB, "submission-never-admitted")
	malformed := harness.status(1, harness.tokenA, "bad")
	for name, recorder := range map[string]*httptest.ResponseRecorder{"other principal": other, "malformed": malformed} {
		if recorder.Code != http.StatusNotFound || !sameErrorBody(t, recorder, unknown) {
			t.Fatalf("%s = %d %s, want the unknown-id answer %d %s", name, recorder.Code, recorder.Body.String(), unknown.Code, unknown.Body.String())
		}
	}
	if !strings.Contains(harness.logs[1].String(), `"surface":"submission_status","outcome":"not_found"`) || !strings.Contains(harness.logs[1].String(), `"surface":"submission_status","outcome":"served","state":"done"`) {
		t.Fatalf("status lines missing:\n%s", harness.logs[1].String())
	}
	// Reading the status runs nothing.
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// Every state reads as itself; a running record past its expiry reads as
// abandoned; a request with no thread has none on its record; a retry with
// another thread keeps the first.
func TestSubmissionStatusReadsEveryState(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	done := make(chan struct{})
	go func() {
		harness.postThread(0, harness.tokenA, "submission-status-run", submissionTestThreadID, submissionQuestion)
		close(done)
	}()
	<-harness.investigator.entered
	running := decodeSubmissionStatus(t, harness.status(0, harness.tokenA, "submission-status-run"))
	if running.State != contractsv1.ContextFabricSubmissionRunning || running.ResultID != "" || running.ReservedResultID == "" {
		t.Fatalf("running = %+v", running)
	}
	// A retry on another thread does not move the record's thread.
	expectSubmissionRefusal(t, harness.postThread(0, harness.tokenA, "submission-status-run", "thread-0000-11913-other", submissionQuestion), contractsv1.ErrorCodeSubmissionInProgress)
	close(harness.investigator.release)
	<-done
	after := decodeSubmissionStatus(t, harness.status(0, harness.tokenA, "submission-status-run"))
	if after.State != contractsv1.ContextFabricSubmissionDone || after.ResultID != running.ReservedResultID || after.ThreadID != submissionTestThreadID {
		t.Fatalf("after the run = %+v, want done on the first thread", after)
	}

	harness.investigator.mode.Store(int32(runUnsaved))
	harness.post(0, harness.tokenA, "submission-status-uns", submissionQuestion)
	if status := decodeSubmissionStatus(t, harness.status(0, harness.tokenA, "submission-status-uns")); status.State != contractsv1.ContextFabricSubmissionDoneUnsaved || status.ResultID != "" || status.ThreadID != "" {
		t.Fatalf("unsaved = %+v", status)
	}
	harness.investigator.mode.Store(int32(runFail))
	harness.post(0, harness.tokenA, "submission-status-fail", submissionQuestion)
	if status := decodeSubmissionStatus(t, harness.status(0, harness.tokenA, "submission-status-fail")); status.State != contractsv1.ContextFabricSubmissionAbandoned {
		t.Fatalf("failed = %+v", status)
	}

	harness.investigator.mode.Store(int32(runBlockThenSave))
	harness.investigator.release = make(chan struct{})
	late := make(chan struct{})
	go func() { harness.post(0, harness.tokenA, "submission-status-exp", submissionQuestion); close(late) }()
	<-harness.investigator.entered
	harness.clock.Advance(2 * time.Minute)
	if status := decodeSubmissionStatus(t, harness.status(0, harness.tokenA, "submission-status-exp")); status.State != contractsv1.ContextFabricSubmissionAbandoned {
		t.Fatalf("past expiry = %+v, want abandoned", status)
	}
	close(harness.investigator.release)
	<-late
}

func TestSubmissionThreadHeaderIsValidated(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	for _, ids := range [][]string{{"short"}, {"thread one", "x"}, {"thread-0000-11913-aa", "thread-0000-11913-bb"}} {
		request := investigationRequest(t, harness.tokenA)
		request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, "submission-thread-bad")
		for _, id := range ids {
			request.Header.Add(contractsv1.ContextFabricThreadIDHeader, id)
		}
		recorder := httptest.NewRecorder()
		harness.apps[0].Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%v: status = %d, want 400", ids, recorder.Code)
		}
	}
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	if recorder := harness.status(0, harness.tokenA, "submission-thread-bad"); recorder.Code != http.StatusNotFound {
		t.Fatalf("a refused request left a record: %d", recorder.Code)
	}
}

func TestSubmissionStatusWithoutAStoreIsUnavailable(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.apps[0].runtime.InvestigationSubmissions = nil
	if recorder := harness.status(0, harness.tokenA, "submission-status-none"); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

// The published golden is the route's own body for a done submission on a
// thread. ACR_WRITE_SUBMISSION_GOLDENS=1 rewrites it.
func TestSubmissionStatusGoldenIsTheRoutesOwnBody(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.apps[0].runtime.NewResultID = func() string { return "result_11913_status_golden" }
	servedResultID(t, harness.postThread(0, harness.tokenA, "submission-status-golden", submissionTestThreadID, submissionQuestion))
	body := bytes.TrimRight(harness.status(0, harness.tokenA, "submission-status-golden").Body.Bytes(), "\n")
	body = append(body, '\n')
	path := repopath.Path(t, "contracts", "examples", "v1", "context_fabric_submission_status.v1.json")
	if os.Getenv("ACR_WRITE_SUBMISSION_GOLDENS") == "1" {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	golden, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (regenerate with ACR_WRITE_SUBMISSION_GOLDENS=1)", err)
	}
	if !bytes.Equal(golden, body) {
		t.Fatalf("golden differs from the route's body:\n%s\nwant\n%s", body, golden)
	}
}
