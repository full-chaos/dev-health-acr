package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-11911 on CHAOS-11912's submissions: a streamed run IS the submission's
// run. The stream's `accepted` names the submission and the result id its
// record reserved, the answer carries that id, and the record ends exactly as
// it does on the JSON path (done on an answer, abandoned on an error), so a
// JSON retry of the key afterwards replays or refuses without a second
// execution.

// streamSubmission posts one create-investigation request for the stream,
// with the submission id, over a real server, and reads the frames when the
// answer is the stream.
func streamSubmission(t *testing.T, harness *submissionHarness, app int, submissionID string) streamResponse {
	t.Helper()
	harness.apps[app].config.ContextFabricEventStreamEnabled = true
	server := httptest.NewServer(harness.apps[app].Handler())
	defer server.Close()
	body := investigationRequestBody()
	body.Question = submissionQuestion
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+harness.tokenA)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	request.Header.Set("Accept", contractsv1.ContextFabricInvestigationEventStreamMediaType)
	request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, submissionID)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	out := streamResponse{status: response.StatusCode, header: response.Header, raw: raw}
	if response.Header.Get("Content-Type") == contractsv1.ContextFabricInvestigationEventStreamMediaType {
		events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(raw), 8<<20)
		if err != nil {
			t.Fatalf("the stream is not the writer's frames: %v\n%s", err, raw)
		}
		out.events = events
	}
	return out
}

func TestAStreamedSubmissionIsTheSubmissionsRun(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	stream := streamSubmission(t, harness, 0, "submission-stream-01")
	if stream.status != http.StatusOK || len(stream.events) == 0 {
		t.Fatalf("status %d (%s)", stream.status, stream.raw)
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(stream.events); err != nil {
		t.Fatalf("grammar: %v\n%s", err, stream.raw)
	}
	accepted, terminal := stream.events[0], stream.events[len(stream.events)-1]
	if accepted.SubmissionID != "submission-stream-01" || accepted.ReservedResultID == "" {
		t.Fatalf("accepted %+v, want the submission and its reserved id", accepted)
	}
	if terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("terminal %+v", terminal)
	}
	var served struct {
		ResultID string `json:"result_id"`
	}
	if err := json.Unmarshal(terminal.Result, &served); err != nil || served.ResultID != accepted.ReservedResultID {
		t.Fatalf("the answer's result_id %q (%v), want the reserved %q", served.ResultID, err, accepted.ReservedResultID)
	}
	if harness.investigator.executions.Load() != 1 || harness.investigator.reservedSeen.Load() != 1 {
		t.Fatalf("executions %d, reservations seen %d: want one run under the submission's reservation", harness.investigator.executions.Load(), harness.investigator.reservedSeen.Load())
	}
	// The shared delivery completed the record, on the line the JSON path
	// writes (the save already made it final).
	if !strings.Contains(harness.logs[0].String(), `"completion":"already_final"`) {
		t.Fatalf("the streamed answer did not complete the submission:\n%s", harness.logs[0].String())
	}
	// The record is done: a JSON retry of the key replays the same result
	// and runs nothing.
	if again := servedResultID(t, harness.post(0, harness.tokenA, "submission-stream-01", submissionQuestion)); again != accepted.ReservedResultID {
		t.Fatalf("the JSON retry replayed %q, want %q", again, accepted.ReservedResultID)
	}
	// A stream retry of the key is answered the same way, before any stream
	// starts: it executes nothing.
	repeat := streamSubmission(t, harness, 0, "submission-stream-01")
	if repeat.header.Get("Content-Type") == contractsv1.ContextFabricInvestigationEventStreamMediaType {
		t.Fatalf("a repeat of a done key opened a stream:\n%s", repeat.raw)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions after the retries = %d, want 1", got)
	}
}

func TestAFailedStreamedSubmissionIsAbandoned(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.investigator.mode.Store(int32(runFail))
	stream := streamSubmission(t, harness, 0, "submission-stream-02")
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(stream.events); err != nil {
		t.Fatalf("grammar: %v\n%s", err, stream.raw)
	}
	if terminal := stream.events[len(stream.events)-1]; terminal.Type != contractsv1.ContextFabricInvestigationEventError {
		t.Fatalf("terminal %+v, want error", terminal)
	}
	if !strings.Contains(harness.logs[0].String(), `"completion":"abandoned_on_error"`) {
		t.Fatalf("the streamed failure did not abandon the submission:\n%s", harness.logs[0].String())
	}
	expectSubmissionRefusal(t, harness.post(0, harness.tokenA, "submission-stream-02", submissionQuestion), contractsv1.ErrorCodeSubmissionAbandoned)
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// TestAnUnsavedStreamedAnswerMarksItsSubmission: an answer served without
// being stored makes the record done_unsaved through the shared delivery, so
// a JSON retry of the key is refused submission_unsaved, never run again.
func TestAnUnsavedStreamedAnswerMarksItsSubmission(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.investigator.mode.Store(int32(runUnsaved))
	stream := streamSubmission(t, harness, 0, "submission-stream-03")
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(stream.events); err != nil {
		t.Fatalf("grammar: %v\n%s", err, stream.raw)
	}
	if terminal := stream.events[len(stream.events)-1]; terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("terminal %+v, want the served answer", terminal)
	}
	expectSubmissionRefusal(t, harness.post(0, harness.tokenA, "submission-stream-03", submissionQuestion), contractsv1.ErrorCodeSubmissionUnsaved)
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// TestAStreamThatCannotStartAbandonsItsSubmission (round-2 finding): every
// exit of the stream handler that never reaches the shared delivery
// abandons the admitted record -- the first frame that cannot be written,
// and a panic before the outcome -- so a retry of the key is answered
// submission_abandoned at once, never submission_in_progress for a run that
// does not exist.
func TestAStreamThatCannotStartAbandonsItsSubmission(t *testing.T) {
	t.Run("the first frame cannot be written", func(t *testing.T) {
		harness, _ := newMemorySubmissionHarness(t, 1)
		harness.apps[0].config.ContextFabricEventStreamEnabled = true
		body := investigationRequestBody()
		body.Question = submissionQuestion
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
		request.Header.Set("Authorization", "Bearer "+harness.tokenA)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", submissionTestRequestID)
		request.Header.Set("Accept", contractsv1.ContextFabricInvestigationEventStreamMediaType)
		request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, "submission-stream-04")
		harness.apps[0].Handler().ServeHTTP(&failingWriter{}, request)
		expectSubmissionRefusal(t, harness.post(0, harness.tokenA, "submission-stream-04", submissionQuestion), contractsv1.ErrorCodeSubmissionAbandoned)
		if got := harness.investigator.executions.Load(); got != 0 {
			t.Fatalf("executions = %d, want 0", got)
		}
	})
	t.Run("a panic after the outcome keeps the record", func(t *testing.T) {
		harness, _ := newMemorySubmissionHarness(t, 1)
		harness.apps[0].logger = slog.New(panicOnMessage{Handler: harness.apps[0].logger.Handler(), message: "context fabric investigation event stream closed"})
		stream := streamSubmission(t, harness, 0, "submission-stream-06")
		if terminal := stream.events[len(stream.events)-1]; terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
			t.Fatalf("terminal %+v, want the result written before the panic", terminal)
		}
		// The delivery already made the record final: the panic must not
		// abandon it, nor even try.
		if strings.Contains(harness.logs[0].String(), `"completion":"abandoned_on_error"`) {
			t.Fatalf("a panic after the outcome tried to abandon the record:\n%s", harness.logs[0].String())
		}
		accepted := stream.events[0]
		if again := servedResultID(t, harness.post(0, harness.tokenA, "submission-stream-06", submissionQuestion)); again != accepted.ReservedResultID {
			t.Fatalf("the JSON retry replayed %q, want %q", again, accepted.ReservedResultID)
		}
	})
	t.Run("a panic before the outcome", func(t *testing.T) {
		harness, _ := newMemorySubmissionHarness(t, 1)
		harness.apps[0].logger = slog.New(panicOnMessage{Handler: harness.apps[0].logger.Handler(), message: "context fabric investigation event stream opened"})
		stream := streamSubmission(t, harness, 0, "submission-stream-05")
		if err := contractsv1.ValidateContextFabricInvestigationEventStream(stream.events); err != nil {
			t.Fatalf("grammar: %v\n%s", err, stream.raw)
		}
		expectSubmissionRefusal(t, harness.post(0, harness.tokenA, "submission-stream-05", submissionQuestion), contractsv1.ErrorCodeSubmissionAbandoned)
		if got := harness.investigator.executions.Load(); got != 0 {
			t.Fatalf("executions = %d, want 0", got)
		}
	})
}
