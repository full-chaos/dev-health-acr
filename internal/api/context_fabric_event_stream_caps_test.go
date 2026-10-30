package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

// The stream's byte caps (design "Ask Dev: browser calls ACR directly", §6):
// one frame at most max serialized bytes + 64 KiB, the stream at most 8 MiB,
// and the last max serialized bytes + 64 KiB of it reserved for the terminal.

func TestTheStreamByteCapsFollowTheServiceBound(t *testing.T) {
	for _, maxSerialized := range []int{8192, 262144, 1048576} {
		settings := defaultInvestigationEventStreamSettings(maxSerialized)
		if want := int64(maxSerialized) + 64<<10; settings.frameMaxBytes != want || settings.terminalReserve != want {
			t.Errorf("max serialized %d: frame cap %d reserve %d, want both %d", maxSerialized, settings.frameMaxBytes, settings.terminalReserve, want)
		}
		if settings.totalMaxBytes != 8<<20 {
			t.Errorf("max serialized %d: total %d, want 8 MiB", maxSerialized, settings.totalMaxBytes)
		}
		if settings.totalMaxBytes-settings.terminalReserve <= 0 {
			t.Errorf("max serialized %d: no room for any non-terminal frame", maxSerialized)
		}
	}
	app, _, _ := newStreamTestApp(t, investigatorOf(failingWith(errors.New("unused"))))
	if want := int64(app.config.MaxSerializedBytes) + 64<<10; app.eventStream.frameMaxBytes != want {
		t.Errorf("the app's frame cap %d, want its own max serialized bytes + 64 KiB (%d)", app.eventStream.frameMaxBytes, want)
	}
}

// TestAFrameOverTheFrameCapIsNotWritten: a stage frame over the cap never
// reaches the wire and never moves the contract check, so the stream that
// goes on is still the contract's grammar.
func TestAFrameOverTheFrameCapIsNotWritten(t *testing.T) {
	recorder := &deadlineRecorder{}
	stream, logs := newUnitStreamWithLogs(t, recorder)
	if err := stream.accepted(30, "", ""); err != nil {
		t.Fatal(err)
	}
	afterAccepted := recorder.body.Len()
	stream.settings.frameMaxBytes = 40
	err := stream.transition(contextfabric.InvestigationStageTransition{Stage: contractsv1.ContextFabricStageInterpreting, State: contractsv1.ContextFabricStageStarted, At: time.Now()})
	if !errors.Is(err, errInvestigationStreamFrameOverCap) {
		t.Fatalf("an oversized stage frame: err %v, want the frame cap refusal", err)
	}
	if recorder.body.Len() != afterAccepted {
		t.Fatalf("an oversized frame reached the wire:\n%s", recorder.body.String())
	}
	line := logLine(t, logs, "context fabric investigation event refused: the frame is over the frame cap")
	if line["failure_class"] != "event_stream_frame_cap" || line["request_id"] != streamTestRequestID || line["frame_max_bytes"] != float64(40) {
		t.Fatalf("frame cap line %v", line)
	}
	stream.settings.frameMaxBytes = 1 << 20
	if err := stream.transition(contextfabric.InvestigationStageTransition{Stage: contractsv1.ContextFabricStageInterpreting, State: contractsv1.ContextFabricStageStarted, At: time.Now()}); err != nil {
		t.Fatalf("the same stage under the cap: %v", err)
	}
	outcome := newInvestigationOutcomeCapture(recorder)
	writeError(outcome, httptest.NewRequest(http.MethodPost, "/", nil), http.StatusServiceUnavailable, "upstream_unavailable", "down", true, nil)
	if got := stream.finish(outcome); got != contractsv1.ContextFabricInvestigationEventError {
		t.Fatalf("terminal %q, want error", got)
	}
	events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(recorder.body.Bytes()), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(events); err != nil {
		t.Fatalf("the stream after a refused frame breaks the grammar: %v", err)
	}
	if len(events) != 3 || events[1].Seq != 2 {
		t.Fatalf("events %d (second seq %d), want accepted, the stage written under the cap as seq 2, and the terminal", len(events), events[1].Seq)
	}
}

// TestATerminalOverTheFrameCapEndsAsBudgetRefusal: a result that does not fit
// one frame is not written; the stream ends with error{budget_refusal}
// (stream_frame_cap), never with a cut or an internal_error.
func TestATerminalOverTheFrameCapEndsAsBudgetRefusal(t *testing.T) {
	app, token, logs := newStreamTestApp(t, investigatorOf(answering(t, "context_fabric_investigation_result.v1.json", nil)))
	app.eventStream.frameMaxBytes = 2048
	response := serveStream(t, app, token, investigationRequestBody(), nil)
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(response.events); err != nil {
		t.Fatalf("grammar: %v\n%s", err, response.raw)
	}
	last := response.events[len(response.events)-1]
	if last.Type != contractsv1.ContextFabricInvestigationEventError || last.Error.Error.Code != "budget_refusal" || last.Error.Error.HTTPStatus != http.StatusRequestEntityTooLarge || last.Error.Error.Details["reason"] != "stream_frame_cap" {
		t.Fatalf("terminal %+v, want error budget_refusal 413 reason stream_frame_cap", last)
	}
	for _, frame := range bytes.SplitAfter(response.raw, []byte("\n\n")) {
		if int64(len(frame)) > app.eventStream.frameMaxBytes {
			t.Fatalf("a %d-byte frame was written over the %d-byte cap", len(frame), app.eventStream.frameMaxBytes)
		}
	}
	if line := logLine(t, logs, "context fabric investigation outcome could not be streamed"); line["served_code"] != "budget_refusal" || line["failure_class"] != "event_stream_outcome" {
		t.Fatalf("outcome line %v", line)
	}
}

// TestAStreamThatReachesItsByteBudgetEndsWithAValidTerminalInsideTheReserve:
// heartbeats fill the non-terminal budget; the frame that would cross it is
// not written, error{budget_refusal} (stream_byte_budget) is written inside
// the reserve, the run is cancelled, and nothing follows the terminal. The
// whole stream stays inside the total and is the contract's grammar.
func TestAStreamThatReachesItsByteBudgetEndsWithAValidTerminalInsideTheReserve(t *testing.T) {
	cancelled := make(chan struct{})
	app, token, logs := newStreamTestApp(t, investigatorOf(func(ctx context.Context, _ contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		select {
		case <-ctx.Done():
			close(cancelled)
			return contextfabric.InvestigationResult{}, ctx.Err()
		case <-time.After(10 * time.Second):
			return contextfabric.InvestigationResult{}, errors.New("the run was never cancelled")
		}
	}))
	app.config.RequestTimeout = 20 * time.Second
	app.eventStream.heartbeat = 2 * time.Millisecond
	app.eventStream.frameMaxBytes = 4096
	app.eventStream.terminalReserve = 4096
	app.eventStream.totalMaxBytes = 4096 + 2400
	response := serveStream(t, app, token, investigationRequestBody(), nil)
	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("the run was not cancelled when the stream reached its byte budget")
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(response.events); err != nil {
		t.Fatalf("grammar: %v\n%s", err, response.raw)
	}
	if int64(len(response.raw)) > app.eventStream.totalMaxBytes {
		t.Fatalf("the stream took %d bytes, over its %d-byte total", len(response.raw), app.eventStream.totalMaxBytes)
	}
	last := response.events[len(response.events)-1]
	if last.Type != contractsv1.ContextFabricInvestigationEventError || last.Error.Error.Code != "budget_refusal" || last.Error.Error.Details["reason"] != "stream_byte_budget" {
		t.Fatalf("terminal %+v, want error budget_refusal reason stream_byte_budget", last)
	}
	terminalStart := bytes.LastIndex(response.raw, []byte("id: "))
	if nonTerminal := int64(terminalStart); nonTerminal > app.eventStream.totalMaxBytes-app.eventStream.terminalReserve {
		t.Fatalf("non-terminal frames took %d bytes, over the %d-byte non-terminal budget", nonTerminal, app.eventStream.totalMaxBytes-app.eventStream.terminalReserve)
	}
	if heartbeats := strings.Count(string(response.raw), "event: heartbeat"); heartbeats < 2 {
		t.Fatalf("%d heartbeats before the budget, want the budget reached by them", heartbeats)
	}
	if line := logLine(t, logs, "context fabric investigation event stream reached its byte budget"); line["failure_class"] != "event_stream_byte_budget" || line["request_id"] != streamTestRequestID {
		t.Fatalf("budget line %v", line)
	}
	if line := logLine(t, logs, "context fabric investigation cancel effective"); line["cause"] != "stream_byte_budget" {
		t.Fatalf("cancel line %v, want cause stream_byte_budget", line)
	}
	if line := logLine(t, logs, "context fabric investigation event stream closed"); line["budget_exhausted"] != true || line["terminal"] != "error" || line["bytes_written"] != float64(len(response.raw)) {
		t.Fatalf("closed line %v, want the budget terminal and the bytes written (%d)", line, len(response.raw))
	}
	if strings.Contains(logs.String(), "the stream already carries its terminal event") {
		t.Fatalf("a frame offered after the budget terminal was logged as a second terminal:\n%s", logs.String())
	}
}

// TestAStreamBudgetTerminalIsAnErrorLatency: a stream that ends with
// error{budget_refusal} delivered no answer, so its latency is "error", as on
// the JSON path's 413, even when the run produced an answer: a result over the
// frame cap, and an answer the run returned after the byte budget cancelled it.
func TestAStreamBudgetTerminalIsAnErrorLatency(t *testing.T) {
	answer := answering(t, "context_fabric_investigation_result.v1.json", nil)
	cells := []struct {
		name      string
		investig  func(context.Context, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error)
		configure func(*App)
		reason    string
	}{
		{"frame cap", answer, func(app *App) { app.eventStream.frameMaxBytes = 2048 }, "stream_frame_cap"},
		{"byte budget", func(ctx context.Context, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
				return contextfabric.InvestigationResult{}, errors.New("the run was never cancelled")
			}
			return answer(context.Background(), request)
		}, func(app *App) {
			app.config.RequestTimeout = 20 * time.Second
			app.eventStream.heartbeat = 2 * time.Millisecond
			app.eventStream.frameMaxBytes = 1 << 20
			app.eventStream.terminalReserve = 1 << 20
			app.eventStream.totalMaxBytes = 1<<20 + 2400
		}, "stream_byte_budget"},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
			app, token, _ := newStreamTestApp(t, investigatorOf(cell.investig))
			app.metrics = instruments
			cell.configure(app)
			response := serveStream(t, app, token, investigationRequestBody(), nil)
			last := response.events[len(response.events)-1]
			if last.Type != contractsv1.ContextFabricInvestigationEventError || last.Error.Error.Details["reason"] != cell.reason {
				t.Fatalf("terminal %+v, want error budget_refusal reason %s", last, cell.reason)
			}
			got := read()
			samples := int64(0)
			for key, value := range got {
				if strings.HasPrefix(key, hostedmetrics.InvestigationLatencyName+"{") {
					samples += value
				}
			}
			if got[hostedmetrics.InvestigationLatencyName+"{status=error}"] != 1 || samples != 1 {
				t.Fatalf("latency cells %v, want one error sample and no other", got)
			}
		})
	}
}

// TestAcceptedEchoesTheRequestsThreadID: `accepted.thread_id` is the
// X-ACR-Thread-ID the request sent, and absent when it sent none.
func TestAcceptedEchoesTheRequestsThreadID(t *testing.T) {
	for _, threadID := range []string{"thread_0123456789abcdef", ""} {
		app, token, _ := newStreamTestApp(t, investigatorOf(answering(t, "context_fabric_investigation_result.v1.json", nil)))
		if threadID != "" {
			// A thread turn is a submission (CHAOS-11948): it needs a
			// submission store and a submission id.
			harness, _ := newMemorySubmissionHarness(t, 1)
			app, token = harness.apps[0], harness.tokenA
			app.config.ContextFabricEventStreamEnabled = true
		}
		server := httptest.NewServer(app.Handler())
		request := investigationHTTPRequest(t, server.URL, token, contractsv1.ContextFabricInvestigationEventStreamMediaType, investigationRequestBody())
		if threadID != "" {
			request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, "submission-accepted-0001")
			request.Header.Set(contractsv1.ContextFabricThreadIDHeader, threadID)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		server.Close()
		events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(raw), 8<<20)
		if err != nil {
			t.Fatalf("thread %q: %v\n%s", threadID, err, raw)
		}
		if err := contractsv1.ValidateContextFabricInvestigationEventStream(events); err != nil {
			t.Fatalf("thread %q: grammar: %v", threadID, err)
		}
		if events[0].Type != contractsv1.ContextFabricInvestigationEventAccepted || events[0].ThreadID != threadID {
			t.Fatalf("thread %q: accepted %+v, want thread_id %q", threadID, events[0], threadID)
		}
	}
}

// TestAFrameOfferedAfterTheBudgetTerminalIsTheBudgetsToRefuse: once the
// stream ended on its byte budget, a frame the pump still offers is refused
// as the budget's, quietly -- never logged as a second terminal -- and never
// reaches the wire.
func TestAFrameOfferedAfterTheBudgetTerminalIsTheBudgetsToRefuse(t *testing.T) {
	recorder := &deadlineRecorder{}
	stream, logs := newUnitStreamWithLogs(t, recorder)
	cancelled := false
	stream.onBudgetExhausted = func() { cancelled = true }
	stream.settings.frameMaxBytes, stream.settings.terminalReserve = 4096, 4096
	if err := stream.accepted(30, "", ""); err != nil {
		t.Fatal(err)
	}
	stream.settings.totalMaxBytes = stream.written + 4096 + 10
	if err := stream.heartbeat(); !errors.Is(err, errInvestigationStreamBudgetExhausted) {
		t.Fatalf("the heartbeat that crosses the budget: err %v, want the budget refusal", err)
	}
	if !cancelled || stream.terminal != contractsv1.ContextFabricInvestigationEventError {
		t.Fatalf("cancelled %v terminal %q, want the run cancelled and the budget terminal written", cancelled, stream.terminal)
	}
	written := recorder.body.Len()
	if err := stream.heartbeat(); !errors.Is(err, errInvestigationStreamBudgetExhausted) {
		t.Fatalf("a heartbeat after the budget terminal: err %v, want the budget refusal", err)
	}
	if recorder.body.Len() != written || strings.Contains(logs.String(), "the stream already carries its terminal event") {
		t.Fatalf("a frame after the budget terminal was written or logged as a second terminal:\n%s", logs.String())
	}
	events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(recorder.body.Bytes()), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(events); err != nil {
		t.Fatalf("grammar: %v", err)
	}
}

// TestATerminalNeverCrossesTheStreamTotal: the reserve is the frame cap, so a
// terminal always fits; should the reserve ever be set below it, a terminal
// that would cross the total is refused, never written over it.
func TestATerminalNeverCrossesTheStreamTotal(t *testing.T) {
	recorder := &deadlineRecorder{}
	stream := newUnitStream(t, recorder)
	stream.settings.frameMaxBytes, stream.settings.terminalReserve = 4096, 16
	if err := stream.accepted(30, "", ""); err != nil {
		t.Fatal(err)
	}
	stream.settings.totalMaxBytes = stream.written + 16 + 50
	outcome := newInvestigationOutcomeCapture(recorder)
	writeError(outcome, httptest.NewRequest(http.MethodPost, "/", nil), http.StatusServiceUnavailable, "upstream_unavailable", "down", true, nil)
	if got := stream.finish(outcome); got != "" {
		t.Fatalf("terminal %q written, want none: it does not fit the total", got)
	}
	if int64(recorder.body.Len()) > stream.settings.totalMaxBytes {
		t.Fatalf("the stream took %d bytes, over its %d-byte total", recorder.body.Len(), stream.settings.totalMaxBytes)
	}
}
