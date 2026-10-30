package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// The route side of the investigation event stream (CHAOS-11911). Every
// stream here goes through a REAL http.Server (httptest.NewServer) and the
// production handler chain, because the write deadline, the flush and the
// disconnect only exist on a real connection: an httptest.ResponseRecorder
// cannot take a write deadline, and the route refuses to stream on it.

// streamTestRequestID is a request id the request-id middleware keeps (it
// mints its own for any value not shaped req_ + 32 hex), so the JSON answer
// and the stream of one case name the same request.
const streamTestRequestID = "req_0123456789abcdef0123456789abcdef"

func newStreamTestApp(t *testing.T, investigator contextfabric.Investigator) (*App, string, *bytes.Buffer) {
	t.Helper()
	app, token, logs := newContextFabricTestAppWithLogs(t, investigator)
	app.config.ContextFabricEventStreamEnabled = true
	return app, token, logs
}

// investigationHTTPRequest is the create-investigation request with the
// fixed request id, so the JSON and stream answers of one fault name the
// same request.
func investigationHTTPRequest(t *testing.T, target, token, accept string, body contractsv1.ContextFabricInvestigationRequest) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, target+ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", streamTestRequestID)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	return request
}

// serveJSON answers one request on the JSON path, in process.
func serveJSON(t *testing.T, app *App, token string, body contractsv1.ContextFabricInvestigationRequest) *httptest.ResponseRecorder {
	t.Helper()
	request := investigationHTTPRequest(t, "", token, "application/json", body)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder
}

type streamResponse struct {
	status int
	header http.Header
	raw    []byte
	events []contractsv1.ContextFabricInvestigationEvent
}

// serveStream answers one request on the stream path, over a real server,
// and reads the body with the contract's own strict frame reader.
func serveStream(t *testing.T, app *App, token string, body contractsv1.ContextFabricInvestigationRequest, configure func(*http.Server)) streamResponse {
	t.Helper()
	server := httptest.NewUnstartedServer(app.Handler())
	if configure != nil {
		configure(server.Config)
	}
	server.Start()
	defer server.Close()
	response, err := server.Client().Do(investigationHTTPRequest(t, server.URL, token, contractsv1.ContextFabricInvestigationEventStreamMediaType, body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read stream: %v", err)
	}
	out := streamResponse{status: response.StatusCode, header: response.Header, raw: raw}
	if response.StatusCode == http.StatusOK && response.Header.Get("Content-Type") == contractsv1.ContextFabricInvestigationEventStreamMediaType {
		events, decodeErr := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(raw), 8<<20)
		if decodeErr != nil {
			t.Fatalf("the stream is not the writer's frames: %v\n%s", decodeErr, raw)
		}
		out.events = events
	}
	return out
}

func goldenResultBody(t *testing.T, name string) contextfabric.InvestigationResult {
	t.Helper()
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "examples", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	var result contextfabric.InvestigationResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

// answering returns the golden result as this request's answer.
func answering(t *testing.T, name string, edit func(*contextfabric.InvestigationResult)) func(context.Context, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	result := goldenResultBody(t, name)
	return func(_ context.Context, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		served := result
		served.RequestID = request.RequestID
		if edit != nil {
			edit(&served)
		}
		return served, nil
	}
}

func failingWith(err error) func(context.Context, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	return func(context.Context, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, err
	}
}

func investigatorOf(fn func(context.Context, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error)) investigatorFunc {
	return investigatorFunc(func(ctx context.Context, _ storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return fn(ctx, request)
	})
}

// TestTheStreamEndsWithTheJSONOutcomeOnEveryExit is the fault list of the
// stream (plan §5.1 I-S03): for every exit, the stream is the contract's
// grammar with exactly one terminal, and that terminal IS what the JSON path
// answers for the same fault: the 200 body byte for byte as `result`, the
// error.v1 envelope as `error`.
func TestTheStreamEndsWithTheJSONOutcomeOnEveryExit(t *testing.T) {
	cases := []struct {
		name    string
		fn      func(context.Context, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error)
		timeout time.Duration
		edit    func(*contractsv1.ContextFabricInvestigationRequest)
	}{
		{name: "answered", fn: answering(t, "context_fabric_investigation_result.v1.json", nil)},
		// The v2 golden is a clarification (clarification_required with an
		// anchor membership offer): the early-clarification exit.
		{name: "early clarification, under the v2 contract", fn: answering(t, "context_fabric_investigation_result.v2.json", nil)},
		{name: "answered with a degraded, unsupported result", fn: answering(t, "context_fabric_investigation_result_unsupported.v1.json", nil)},
		{name: "answered as the bounded projection", fn: answering(t, "context_fabric_investigation_result.v1.json", nil), edit: func(body *contractsv1.ContextFabricInvestigationRequest) { body.Options.MaxSerializedBytes = 8192 }},
		{name: "a reused answer naming an earlier request", fn: answering(t, "context_fabric_investigation_result.v1.json", func(result *contextfabric.InvestigationResult) {
			result.RequestID, result.Reused = "request_earlier01", true
		})},
		{name: "interpretation rejected", fn: failingWith(fmt.Errorf("%w: requested_judgment over its bound", contextfabric.ErrInterpretationRejected))},
		{name: "fact read error", fn: failingWith(fmt.Errorf("%w: read canonical facts", contextfabric.ErrFactReadAborted))},
		{name: "model deadline", fn: failingWith(fmt.Errorf("%w: %w", contextfabric.ErrModelTimeout, context.DeadlineExceeded))},
		{name: "request deadline", timeout: 300 * time.Millisecond, fn: func(ctx context.Context, _ contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
			<-ctx.Done()
			return contextfabric.InvestigationResult{}, ctx.Err()
		}},
		{name: "budget refusal", fn: failingWith(contextfabric.AnswerBudgetRefusal{
			Overrun: contractsv1.ContextFabricBudgetOverrunItems, MeasuredItems: 41, MaxItems: 30, MaxSerializedBytes: 1 << 20,
			RetryAttempted: true, Family: contextfabric.QuestionFamilyUnclassified, NarrowerContinuationAxis: contextfabric.NarrowingContinuationNone,
		})},
		{name: "panic", fn: func(context.Context, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
			panic("investigator panics")
		}},
		{name: "org purged", fn: failingWith(fmt.Errorf("save result: %w", storage.ErrOrgPurged))},
		{name: "save error", fn: failingWith(fmt.Errorf("save result: %w", errors.New("postgres refused the write")))},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			body := investigationRequestBody()
			if test.edit != nil {
				test.edit(&body)
			}
			jsonApp, jsonToken, _ := newStreamTestApp(t, investigatorOf(test.fn))
			streamApp, streamToken, streamLogs := newStreamTestApp(t, investigatorOf(test.fn))
			if test.timeout > 0 {
				jsonApp.config.RequestTimeout, streamApp.config.RequestTimeout = test.timeout, test.timeout
			}
			jsonAnswer := serveJSON(t, jsonApp, jsonToken, body)
			stream := serveStream(t, streamApp, streamToken, body, nil)
			if stream.status != http.StatusOK {
				t.Fatalf("stream status = %d, want 200 (body %s)", stream.status, stream.raw)
			}
			for header, want := range map[string]string{
				"Content-Type": contractsv1.ContextFabricInvestigationEventStreamMediaType, "Cache-Control": "no-store, no-transform", "X-Accel-Buffering": "no",
			} {
				if got := stream.header.Get(header); got != want {
					t.Fatalf("%s = %q, want %q", header, got, want)
				}
			}
			if err := contractsv1.ValidateContextFabricInvestigationEventStream(stream.events); err != nil {
				t.Fatalf("the stream breaks the contract grammar: %v\n%s", err, stream.raw)
			}
			terminals := 0
			for _, event := range stream.events {
				if event.Type.Terminal() {
					terminals++
				}
			}
			if terminals != 1 {
				t.Fatalf("%d terminal events, want exactly 1", terminals)
			}
			terminal := stream.events[len(stream.events)-1]
			switch {
			case jsonAnswer.Code == http.StatusOK:
				if terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
					t.Fatalf("JSON answered 200, the stream ended on %s", terminal.Type)
				}
				if !bytes.Equal(terminal.Result, jsonAnswer.Body.Bytes()) {
					t.Fatalf("the result event is not the JSON body byte for byte\nstream: %s\njson:   %s", terminal.Result, jsonAnswer.Body.Bytes())
				}
			default:
				if terminal.Type != contractsv1.ContextFabricInvestigationEventError {
					t.Fatalf("JSON answered %d, the stream ended on %s", jsonAnswer.Code, terminal.Type)
				}
				var jsonEnvelope contractsv1.ErrorEnvelope
				if err := json.Unmarshal(jsonAnswer.Body.Bytes(), &jsonEnvelope); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(*terminal.Error, jsonEnvelope) {
					t.Fatalf("the error event is not the JSON error envelope\nstream: %#v\njson:   %#v", *terminal.Error, jsonEnvelope)
				}
				if terminal.Error.Error.HTTPStatus != jsonAnswer.Code {
					t.Fatalf("http_status %d, JSON answered %d", terminal.Error.Error.HTTPStatus, jsonAnswer.Code)
				}
			}
			accepted := stream.events[0]
			if accepted.Type != contractsv1.ContextFabricInvestigationEventAccepted || accepted.RequestID != streamTestRequestID {
				t.Fatalf("first event %+v, want accepted for this request", accepted)
			}
			wantBudget := int64((streamApp.config.RequestTimeout + time.Second - 1) / time.Second)
			if accepted.BudgetSeconds != wantBudget {
				t.Fatalf("budget_seconds %d, want the request deadline rounded up (%d)", accepted.BudgetSeconds, wantBudget)
			}
			if test.timeout == 0 && strings.Contains(streamLogs.String(), "context fabric investigation cancel effective") {
				t.Fatalf("a run that ended on its own logged a cancel:\n%s", streamLogs.String())
			}
		})
	}
}

// TestTheStreamOfARealEngineRunIsTheJSONBodyWithItsStages runs the real
// engine through the route twice -- server synthesis and client synthesis,
// which carries synthesis_input in the body -- and holds the stream's
// result byte-equal to the JSON body of the same run, with the engine's
// stages in between.
func TestTheStreamOfARealEngineRunIsTheJSONBodyWithItsStages(t *testing.T) {
	for _, mode := range []contractsv1.ContextFabricSynthesisMode{"", contractsv1.ContextFabricSynthesisModeClient} {
		t.Run("synthesis_mode="+string(mode), func(t *testing.T) {
			body := investigationRequestBody()
			body.SynthesisMode = mode
			body.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
			jsonRig := newClientRouteRig(t, singleSubjectFixture())
			jsonAnswer := serveJSON(t, jsonRig.app, jsonRig.token, body)
			if jsonAnswer.Code != http.StatusOK {
				t.Fatalf("JSON status = %d (%s)", jsonAnswer.Code, jsonAnswer.Body.String())
			}
			var jsonResult struct {
				ResultID string `json:"result_id"`
			}
			if err := json.Unmarshal(jsonAnswer.Body.Bytes(), &jsonResult); err != nil {
				t.Fatal(err)
			}
			streamRig := newClientRouteRig(t, singleSubjectFixture())
			streamRig.app.config.ContextFabricEventStreamEnabled = true
			// Both rigs' engines mint the same id. The stream reserves a
			// DIFFERENT id of the same length, so the answer can only carry it
			// if the reservation reached the engine; the JSON body with that one
			// id swapped is then the stream's answer byte for byte.
			reserved := []byte(jsonResult.ResultID)
			for i := len("result_"); i < len(reserved); i++ {
				if reserved[i] == '9' {
					reserved[i] = '0'
				} else {
					reserved[i] = '9'
				}
			}
			streamRig.app.eventStream.reserveID = func() string { return string(reserved) }
			stream := serveStream(t, streamRig.app, streamRig.token, body, nil)
			if stream.status != http.StatusOK {
				t.Fatalf("stream status = %d (%s)", stream.status, stream.raw)
			}
			if err := contractsv1.ValidateContextFabricInvestigationEventStream(stream.events); err != nil {
				t.Fatalf("grammar: %v\n%s", err, stream.raw)
			}
			stages := map[contractsv1.ContextFabricInvestigationStage]bool{}
			for _, event := range stream.events {
				if event.Type == contractsv1.ContextFabricInvestigationEventStage {
					stages[event.Stage] = true
				}
			}
			if len(stages) < 2 {
				t.Fatalf("the stream shows %d distinct stages before its result, want at least 2:\n%s", len(stages), stream.raw)
			}
			terminal := stream.events[len(stream.events)-1]
			if terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
				t.Fatalf("terminal %s", terminal.Type)
			}
			if bytes.Contains(terminal.Result, []byte(jsonResult.ResultID)) {
				t.Fatalf("the streamed answer carries the engine's own id %q, not the reserved %q: the reservation did not reach the engine", jsonResult.ResultID, reserved)
			}
			swapped := bytes.ReplaceAll(jsonAnswer.Body.Bytes(), []byte(jsonResult.ResultID), reserved)
			if !bytes.Equal(terminal.Result, swapped) {
				t.Fatalf("the streamed answer is not the JSON body (with the reserved id) byte for byte\nstream: %s\njson:   %s", terminal.Result, swapped)
			}
			if stream.events[0].ReservedResultID != string(reserved) {
				t.Fatalf("accepted reserved %q, the route reserved %q", stream.events[0].ReservedResultID, reserved)
			}
		})
	}
}

// TestAStreamRequestIsRefusedWhileTheStreamIsOff: 406 stream_unavailable,
// written before any admission work -- the investigator never runs and the
// body is never read (an unparseable body still gets the 406) -- and
// byte-equal to the published golden.
func TestAStreamRequestIsRefusedWhileTheStreamIsOff(t *testing.T) {
	var calls atomic.Int32
	app, token, _ := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		calls.Add(1)
		return contextfabric.InvestigationResult{}, nil
	}))
	golden, err := os.ReadFile(repopath.Path(t, "contracts", "examples", "v1", "error_context_fabric_stream_unavailable.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{mustJSON(t, investigationRequestBody()), "{not json"} {
		request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		request.Header.Set("Accept", contractsv1.ContextFabricInvestigationEventStreamMediaType)
		request.Header.Set("X-Request-ID", streamTestRequestID)
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotAcceptable {
			t.Fatalf("status = %d, want 406 (%s)", recorder.Code, recorder.Body.String())
		}
		// The golden names a documentation request id; the body is the
		// golden byte for byte with this request's id in its place.
		want := bytes.Replace(golden, []byte(`"req_01J0ACR011"`), []byte(`"`+streamTestRequestID+`"`), 1)
		if bytes.Equal(want, golden) {
			t.Fatal("the golden's request id was not found, so the comparison would be vacuous")
		}
		if !bytes.Equal(recorder.Body.Bytes(), want) {
			t.Fatalf("the 406 body is not the golden\ngot:    %s\ngolden: %s", recorder.Body.Bytes(), golden)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("the investigator ran %d times behind a 406", calls.Load())
	}
	// Two Accept headers, one of them the stream type, are not "exactly
	// text/event-stream": the JSON response, flag off or on.
	twoHeaders := investigationHTTPRequest(t, "", token, "", investigationRequestBody())
	twoHeaders.Header.Add("Accept", contractsv1.ContextFabricInvestigationEventStreamMediaType)
	twoHeaders.Header.Add("Accept", "application/json")
	app.config.ContextFabricEventStreamEnabled = false
	twoRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(twoRecorder, twoHeaders)
	if twoRecorder.Code == http.StatusNotAcceptable || twoRecorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("two Accept headers: status %d %q, want the JSON response", twoRecorder.Code, twoRecorder.Header().Get("Content-Type"))
	}
	// Any other Accept is the JSON response, flag off or on.
	for _, accept := range []string{"", "application/json", "text/event-stream, application/json", "*/*"} {
		app.config.ContextFabricEventStreamEnabled = false
		off := serveJSONWithAccept(t, app, token, accept)
		app.config.ContextFabricEventStreamEnabled = true
		on := serveJSONWithAccept(t, app, token, accept)
		if off.Code != on.Code || !bytes.Equal(off.Body.Bytes(), on.Body.Bytes()) || on.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("Accept %q: the JSON response changed with the stream flag (off %d, on %d %q)", accept, off.Code, on.Code, on.Header().Get("Content-Type"))
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func serveJSONWithAccept(t *testing.T, app *App, token, accept string) *httptest.ResponseRecorder {
	t.Helper()
	request := investigationHTTPRequest(t, "", token, accept, investigationRequestBody())
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder
}

// TestAFailureBeforeAdmissionKeepsItsJSONAnswer: a stream request that fails
// a check before the investigation is admitted gets that check's status and
// error.v1 JSON body, never a stream.
func TestAFailureBeforeAdmissionKeepsItsJSONAnswer(t *testing.T) {
	app, token, _ := newStreamTestApp(t, investigatorOf(failingWith(errors.New("never reached"))))
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+ContextFabricInvestigationsPath, strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Accept", contractsv1.ContextFabricInvestigationEventStreamMediaType)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope contractsv1.ErrorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest || response.Header.Get("Content-Type") != "application/json" || envelope.Error.Code != "invalid_request" {
		t.Fatalf("status %d content-type %q code %q, want the JSON 400", response.StatusCode, response.Header.Get("Content-Type"), envelope.Error.Code)
	}
}

// TestTheWriteDeadlineMovesWithEveryFrame: http.Server.WriteTimeout is an
// absolute deadline for the whole response. With it at 300 ms and a run of a
// second and a half, the stream survives only because every frame write moves
// the connection's deadline forward; without that, the server cuts the
// connection and the stream ends with no terminal. The same test proves the
// handler chain unwraps to the connection: a wrapper without Unwrap makes the
// deadline unsettable and the route refuses to stream.
func TestTheWriteDeadlineMovesWithEveryFrame(t *testing.T) {
	answer := answering(t, "context_fabric_investigation_result.v1.json", nil)
	app, token, logs := newStreamTestApp(t, investigatorOf(func(ctx context.Context, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return contextfabric.InvestigationResult{}, ctx.Err()
		}
		return answer(ctx, request)
	}))
	app.config.RequestTimeout = 10 * time.Second
	app.eventStream.heartbeat = 100 * time.Millisecond
	// Each frame write moves the deadline 400 ms ahead. If only the first
	// write did, the connection would die 400 ms in, long before the answer.
	app.eventStream.writeTimeout = 400 * time.Millisecond
	stream := serveStream(t, app, token, investigationRequestBody(), func(server *http.Server) { server.WriteTimeout = 300 * time.Millisecond })
	if stream.status != http.StatusOK {
		t.Fatalf("status = %d (%s)\nlogs: %s", stream.status, stream.raw, logs.String())
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(stream.events); err != nil {
		t.Fatalf("the stream did not survive the server's absolute write timeout: %v\n%s", err, stream.raw)
	}
	heartbeats := 0
	for _, event := range stream.events {
		if event.Type == contractsv1.ContextFabricInvestigationEventHeartbeat {
			heartbeats++
		}
	}
	// 1.5 s of silence at a 100 ms interval: the heartbeat is what kept the
	// connection busy. A floor well below 15 tolerates a loaded runner.
	if heartbeats < 5 {
		t.Fatalf("%d heartbeats over 1.5 s at a 100 ms interval, want at least 5", heartbeats)
	}
	// budget_seconds is the 10 s request deadline rounded UP: a few ms have
	// passed since the deadline was set, and rounding down would say 9.
	if stream.events[0].BudgetSeconds != 10 {
		t.Fatalf("budget_seconds %d, want 10", stream.events[0].BudgetSeconds)
	}
}

// TestAWriterWithoutAWriteDeadlineGetsNoStream: the route proves it can move
// the write deadline before it writes the 200. A writer that cannot take one
// (here an httptest.ResponseRecorder, which the request wrappers unwrap to)
// gets the error.v1 internal_error answer, not a stream that
// http.Server.WriteTimeout would cut.
func TestAWriterWithoutAWriteDeadlineGetsNoStream(t *testing.T) {
	ran := false
	app, token, logs := newStreamTestApp(t, investigatorOf(func(ctx context.Context, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		ran = true
		return contextfabric.InvestigationResult{}, errors.New("unused")
	}))
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationHTTPRequest(t, "", token, contractsv1.ContextFabricInvestigationEventStreamMediaType, investigationRequestBody()))
	if recorder.Code != http.StatusInternalServerError || recorder.Header().Get("Content-Type") != "application/json" || !strings.Contains(recorder.Body.String(), `"internal_error"`) {
		t.Fatalf("status %d %q %s, want the internal_error JSON answer", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	if ran {
		t.Fatal("the investigation ran although no stream could be opened")
	}
	if line := logLine(t, logs, "context fabric investigation event stream cannot set a write deadline"); line["failure_class"] != "event_stream_writer_unsupported" {
		t.Fatalf("refusal line %v", line)
	}
}

// TestTheProductionStreamTimingIsTheContracts: the heartbeat interval the
// route runs is the contract's, and the per-frame write deadline is 30 s.
func TestTheProductionStreamTimingIsTheContracts(t *testing.T) {
	settings := defaultInvestigationEventStreamSettings()
	if settings.heartbeat != contractsv1.ContextFabricInvestigationEventHeartbeatInterval {
		t.Fatalf("heartbeat %s, want the contract's interval", settings.heartbeat)
	}
	if contractsv1.ContextFabricInvestigationEventHeartbeatInterval != 10*time.Second {
		t.Fatalf("the contract's heartbeat interval is %s, want 10 s", contractsv1.ContextFabricInvestigationEventHeartbeatInterval)
	}
	if settings.writeTimeout != 30*time.Second {
		t.Fatalf("write timeout %s, want 30 s", settings.writeTimeout)
	}
	if settings.reserveID == nil || !strings.HasPrefix(settings.reserveID(), "result_") {
		t.Fatal("the reserved id does not come from the result id generator")
	}
}

// TestADisconnectCancelsTheRun: the client reads `accepted` and goes away.
// The run context ends (the investigator sees it), and one cancel effective
// line records the cause and t_eff.
func TestADisconnectCancelsTheRun(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan error, 1)
	app, token, logs := newStreamTestApp(t, investigatorOf(func(ctx context.Context, _ contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			cancelled <- ctx.Err()
			return contextfabric.InvestigationResult{}, ctx.Err()
		case <-time.After(20 * time.Second):
			cancelled <- nil
			return contextfabric.InvestigationResult{}, errors.New("never cancelled")
		}
	}))
	app.config.RequestTimeout = 30 * time.Second
	server := httptest.NewServer(app.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	request := investigationHTTPRequest(t, server.URL, token, contractsv1.ContextFabricInvestigationEventStreamMediaType, investigationRequestBody()).WithContext(ctx)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 16)
	if _, err := io.ReadFull(response.Body, first); err != nil || !bytes.HasPrefix(first, []byte("id: 1\nevent: acc")) {
		t.Fatalf("first bytes %q (%v), want the accepted frame", first, err)
	}
	<-started
	disconnected := time.Now()
	cancel()
	response.Body.Close()
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the run ended with %v, want the disconnect's cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run was not cancelled within 10 s of the disconnect")
	}
	server.Close()
	line := logLine(t, logs, "context fabric investigation cancel effective")
	if line["cause"] != "client_disconnected" || line["request_id"] != streamTestRequestID {
		t.Fatalf("cancel effective line %v, want cause client_disconnected for this request", line)
	}
	tEff, err := time.Parse(time.RFC3339Nano, fmt.Sprint(line["t_eff"]))
	if err != nil || tEff.Before(disconnected.Add(-time.Second)) || tEff.After(disconnected.Add(10*time.Second)) {
		t.Fatalf("t_eff %v (%v) is not the moment after the disconnect at %s", line["t_eff"], err, disconnected)
	}
}

func logLine(t *testing.T, logs *bytes.Buffer, message string) map[string]any {
	t.Helper()
	for _, raw := range strings.Split(logs.String(), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == message {
			return line
		}
	}
	t.Fatalf("no %q line in the logs:\n%s", message, logs.String())
	return nil
}

// deadlineRecorder is a ResponseWriter that takes write deadlines and
// flushes, for the stream writer's unit tests.
type deadlineRecorder struct {
	mu        sync.Mutex
	header    http.Header
	body      bytes.Buffer
	deadlines int
}

func (d *deadlineRecorder) Header() http.Header {
	if d.header == nil {
		d.header = http.Header{}
	}
	return d.header
}
func (d *deadlineRecorder) WriteHeader(int) {}
func (d *deadlineRecorder) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.body.Write(p)
}
func (d *deadlineRecorder) SetWriteDeadline(time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadlines++
	return nil
}
func (d *deadlineRecorder) FlushError() error { return nil }

// requestWithStreamID is a request whose context carries the stream test's
// request id, as the request-id middleware would set it.
func requestWithStreamID() *http.Request {
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, nil)
	return request.WithContext(context.WithValue(request.Context(), requestIDContextKey, streamTestRequestID))
}

func newUnitStream(t *testing.T, recorder *deadlineRecorder) *investigationEventStream {
	t.Helper()
	stream, _ := newUnitStreamWithLogs(t, recorder)
	return stream
}

func newUnitStreamWithLogs(t *testing.T, recorder *deadlineRecorder) (*investigationEventStream, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	return &investigationEventStream{
		w: recorder, controller: http.NewResponseController(recorder), settings: defaultInvestigationEventStreamSettings(),
		logger: testLogger(logs), logCtx: context.Background(), requestID: streamTestRequestID, reserved: "result_12345678",
		startedAt: time.Now(),
	}, logs
}

// TestTheStreamWritesOneTerminalOnly: a second terminal is refused and never
// reaches the wire, and every frame moved the write deadline.
func TestTheStreamWritesOneTerminalOnly(t *testing.T) {
	recorder := &deadlineRecorder{}
	stream, logs := newUnitStreamWithLogs(t, recorder)
	if err := stream.accepted(30, ""); err != nil {
		t.Fatal(err)
	}
	body := goldenResultBody(t, "context_fabric_investigation_result.v1.json")
	body.RequestID = streamTestRequestID
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	outcome := newInvestigationOutcomeCapture(recorder)
	writeEncodedJSON(outcome, http.StatusOK, encoded)
	if got := stream.finish(outcome); got != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("first terminal %q, want result", got)
	}
	failure := newInvestigationOutcomeCapture(recorder)
	writeError(failure, requestWithStreamID(), http.StatusInternalServerError, "internal_error", "late failure", false, nil)
	if got := stream.finish(failure); got != "" {
		t.Fatalf("a second terminal %q was written", got)
	}
	// The writer's own guard refuses it and says why; it is not read as an
	// outcome the contract refused (which would try an internal_error
	// fallback and log that the outcome could not be streamed).
	if line := logLine(t, logs, "context fabric investigation event refused: the stream already carries its terminal event"); line["failure_class"] != "event_stream_second_terminal" {
		t.Fatalf("second-terminal line %v", line)
	}
	if strings.Contains(logs.String(), "could not be streamed") || strings.Contains(logs.String(), "refused by the stream contract") {
		t.Fatalf("a second terminal was handled as a refused outcome:\n%s", logs.String())
	}
	events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(recorder.body.Bytes()), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(events); err != nil || len(events) != 2 {
		t.Fatalf("%d events, grammar %v: want accepted and one result", len(events), err)
	}
	if recorder.deadlines != len(events) {
		t.Fatalf("%d write deadlines for %d frames, want one per frame", recorder.deadlines, len(events))
	}
}

// TestTheStreamKeepsTheGrammarWhenTransitionsAreLost: the writer holds the
// stream to the contract even when the sink lost a transition (a completion
// dropped, a repeat delivered): it completes an open stage before the next
// one starts and ignores what would go back.
func TestTheStreamKeepsTheGrammarWhenTransitionsAreLost(t *testing.T) {
	recorder := &deadlineRecorder{}
	stream := newUnitStream(t, recorder)
	if err := stream.accepted(30, ""); err != nil {
		t.Fatal(err)
	}
	at := stream.startedAt
	for _, transition := range []contextfabric.InvestigationStageTransition{
		{Stage: contractsv1.ContextFabricStageInterpreting, State: contractsv1.ContextFabricStageStarted, At: at.Add(time.Millisecond)},
		// interpreting's completion was dropped.
		{Stage: contractsv1.ContextFabricStageResolvingSubjects, State: contractsv1.ContextFabricStageStarted, At: at.Add(2 * time.Second)},
		{Stage: contractsv1.ContextFabricStageInterpreting, State: contractsv1.ContextFabricStageStarted, At: at.Add(3 * time.Second)},
		// A repeat of the open stage (the budget re-synthesis shape).
		{Stage: contractsv1.ContextFabricStageResolvingSubjects, State: contractsv1.ContextFabricStageStarted, At: at.Add(3500 * time.Millisecond)},
		{Stage: contractsv1.ContextFabricStageReadingFacts, State: contractsv1.ContextFabricStageCompleted, At: at.Add(4 * time.Second)},
	} {
		if err := stream.transition(transition); err != nil {
			t.Fatalf("transition %+v: %v", transition, err)
		}
	}
	// A heartbeat while resolving_subjects is open names it, and its elapsed
	// (the wall clock, about 0 ms) is held at the 2 s already written rather
	// than going back.
	if err := stream.heartbeat(); err != nil {
		t.Fatal(err)
	}
	if err := stream.transition(contextfabric.InvestigationStageTransition{Stage: contractsv1.ContextFabricStageResolvingSubjects, State: contractsv1.ContextFabricStageCompleted, At: at.Add(5 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	failure := newInvestigationOutcomeCapture(recorder)
	writeError(failure, requestWithStreamID(), http.StatusGatewayTimeout, "upstream_unavailable", "timed out", true, nil)
	if got := stream.finish(failure); got != contractsv1.ContextFabricInvestigationEventError {
		t.Fatalf("terminal %q, want error:\n%s", got, recorder.body.String())
	}
	events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(recorder.body.Bytes()), 8<<20)
	if err != nil {
		t.Fatalf("frames: %v\n%s", err, recorder.body.String())
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(events); err != nil {
		t.Fatalf("grammar: %v\n%s", err, recorder.body.String())
	}
}

// TestADroppedTransitionIsCountedAndLogged: after the run, the route records
// the sink's drop counts: one line per stage that lost transitions, with its
// count, and none for a stage that lost none.
func TestADroppedTransitionIsCountedAndLogged(t *testing.T) {
	app, _, logs := newStreamTestApp(t, investigatorOf(failingWith(errors.New("unused"))))
	app.recordInvestigationEventsDropped(context.Background(), streamTestRequestID, map[contractsv1.ContextFabricInvestigationStage]int64{
		contractsv1.ContextFabricStageReadingFacts: 2, contractsv1.ContextFabricStageSynthesizing: 0,
	})
	line := logLine(t, logs, "context fabric investigation events dropped")
	if line["stage"] != "reading_facts" || line["count"] != float64(2) || line["request_id"] != streamTestRequestID || line["failure_class"] != "event_stream_sink_full" {
		t.Fatalf("dropped line %v", line)
	}
	if got := strings.Count(logs.String(), "context fabric investigation events dropped"); got != 1 {
		t.Fatalf("%d drop lines, want 1 (a stage with no drops logs nothing):\n%s", got, logs.String())
	}
}

// TestAnOutcomeTheContractRefusesEndsAsInternalError: a 200 body the result
// event cannot carry (here, one that is not a create-investigation body) is a
// route defect; the stream ends on an internal_error terminal, never silently.
func TestAnOutcomeTheContractRefusesEndsAsInternalError(t *testing.T) {
	recorder := &deadlineRecorder{}
	stream := newUnitStream(t, recorder)
	if err := stream.accepted(30, ""); err != nil {
		t.Fatal(err)
	}
	outcome := newInvestigationOutcomeCapture(recorder)
	writeEncodedJSON(outcome, http.StatusOK, []byte(`{"schema_version":"context_packet.v1"}`))
	if got := stream.finish(outcome); got != contractsv1.ContextFabricInvestigationEventError {
		t.Fatalf("terminal %q, want error", got)
	}
	events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(recorder.body.Bytes()), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(events); err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; last.Error.Error.Code != "internal_error" || last.Error.Error.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("terminal envelope %+v, want internal_error 500", last.Error.Error)
	}
}

// failingWriter is a connection that refuses every write.
type failingWriter struct{ deadlineRecorder }

func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// TestAFailedWriteEndsTheRun: nobody reads a stream that cannot be written,
// so a failed frame write cancels the run, and the stream stays failed.
func TestAFailedWriteEndsTheRun(t *testing.T) {
	writer := &failingWriter{}
	cancelled := 0
	logs := &bytes.Buffer{}
	stream := &investigationEventStream{
		w: writer, controller: http.NewResponseController(writer), settings: defaultInvestigationEventStreamSettings(),
		logger: testLogger(logs), logCtx: context.Background(), requestID: streamTestRequestID, reserved: "result_12345678",
		startedAt: time.Now(), onWriteFailure: func() { cancelled++ },
	}
	if err := stream.accepted(30, ""); err == nil {
		t.Fatal("the write failed and accepted reported success")
	}
	if cancelled != 1 {
		t.Fatalf("the run was cancelled %d times, want once", cancelled)
	}
	// The stream stays failed: a later event returns the write's own error
	// and is never handed to the stream check (which would log a contract
	// refusal for an event nobody tried to write).
	if err := stream.heartbeat(); err == nil || !strings.Contains(err.Error(), "broken pipe") || cancelled != 1 {
		t.Fatalf("a failed stream wrote again (err %v, cancels %d)", err, cancelled)
	}
	if strings.Contains(logs.String(), "refused by the stream contract") {
		t.Fatalf("a failed stream handed a later event to the stream check:\n%s", logs.String())
	}
	if line := logLine(t, logs, "context fabric investigation event stream write failed"); line["request_id"] != streamTestRequestID {
		t.Fatalf("write failure line %v", line)
	}
}

// TestAStreamedRefusalKeepsItsDenialClass: writeError marks the denial on
// the writer it is given; the capture passes it to the real writer, so the
// access log and observability classify a streamed refusal like a JSON one.
func TestAStreamedRefusalKeepsItsDenialClass(t *testing.T) {
	outer := &statusWriter{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	capture := newInvestigationOutcomeCapture(outer)
	writeError(capture, requestWithStreamID(), http.StatusTooManyRequests, "rate_limited", "Context Fabric is rate limited; retry later", true, nil)
	if outer.denialCode != "rate_limited" {
		t.Fatalf("denial code %q reached the real writer, want rate_limited", outer.denialCode)
	}
	if capture.status != http.StatusTooManyRequests || !strings.Contains(capture.body.String(), `"rate_limited"`) {
		t.Fatalf("capture %d %s", capture.status, capture.body.String())
	}
}

// TestThePumpDrainsWhatTheSinkHolds: when the run ends, the pump writes every
// transition the sink still holds before the terminal, so a stage the engine
// reported in its last moments is not lost to the select order.
func TestThePumpDrainsWhatTheSinkHolds(t *testing.T) {
	recorder := &deadlineRecorder{}
	stream := newUnitStream(t, recorder)
	if err := stream.accepted(30, ""); err != nil {
		t.Fatal(err)
	}
	transitions := make(chan contextfabric.InvestigationStageTransition, 16)
	at := stream.startedAt
	for i, stage := range []contractsv1.ContextFabricInvestigationStage{
		contractsv1.ContextFabricStageInterpreting, contractsv1.ContextFabricStageResolvingSubjects, contractsv1.ContextFabricStageReadingFacts,
		contractsv1.ContextFabricStageSynthesizing, contractsv1.ContextFabricStageValidating,
	} {
		transitions <- contextfabric.InvestigationStageTransition{Stage: stage, State: contractsv1.ContextFabricStageStarted, At: at.Add(time.Duration(2*i) * time.Millisecond)}
		transitions <- contextfabric.InvestigationStageTransition{Stage: stage, State: contractsv1.ContextFabricStageCompleted, At: at.Add(time.Duration(2*i+1) * time.Millisecond)}
	}
	done := make(chan struct{})
	close(done)
	stream.pump(transitions, done)
	events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(recorder.body.Bytes()), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 11 {
		t.Fatalf("%d events after the run ended, want accepted and all 10 transitions:\n%s", len(events), recorder.body.String())
	}
}

// TestARunWithNoOutcomeEndsWithNoTerminal: when the post-processing wrote
// nothing (the client is gone, so the JSON path writes nothing either), the
// stream writes no terminal at all -- not a result with no body, not an
// internal_error -- and a consumer reads it as interrupted.
func TestARunWithNoOutcomeEndsWithNoTerminal(t *testing.T) {
	recorder := &deadlineRecorder{}
	stream, logs := newUnitStreamWithLogs(t, recorder)
	if err := stream.accepted(30, ""); err != nil {
		t.Fatal(err)
	}
	if got := stream.finish(newInvestigationOutcomeCapture(recorder)); got != "" {
		t.Fatalf("terminal %q for a run with no outcome", got)
	}
	events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(recorder.body.Bytes()), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("%d events, want accepted only:\n%s", len(events), recorder.body.String())
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(events); !errors.Is(err, contractsv1.ErrContextFabricInvestigationStreamInterrupted) {
		t.Fatalf("stream check %v, want interrupted", err)
	}
	if strings.Contains(logs.String(), "could not be streamed") {
		t.Fatalf("a run with no outcome was handled as a refused outcome:\n%s", logs.String())
	}
}

// disconnectedRun runs the disconnect scenario of TestADisconnectCancelsTheRun
// on app: the client reads `accepted` and leaves; the investigator returns
// settle after it sees the cancel. It returns once the server has finished
// the handler.
func disconnectedRun(t *testing.T, configure func(*App), settle time.Duration) *bytes.Buffer {
	t.Helper()
	started := make(chan struct{})
	app, token, logs := newStreamTestApp(t, investigatorOf(func(ctx context.Context, _ contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			time.Sleep(settle)
			return contextfabric.InvestigationResult{}, ctx.Err()
		case <-time.After(20 * time.Second):
			return contextfabric.InvestigationResult{}, errors.New("never cancelled")
		}
	}))
	app.config.RequestTimeout = 30 * time.Second
	configure(app)
	server := httptest.NewServer(app.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	response, err := server.Client().Do(investigationHTTPRequest(t, server.URL, token, contractsv1.ContextFabricInvestigationEventStreamMediaType, investigationRequestBody()).WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 16)
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatalf("first bytes: %v", err)
	}
	<-started
	cancel()
	response.Body.Close()
	server.Close()
	return logs
}

// TestALostCancelWatchStillLogsTheCancel: when the stop wins the race with
// the watcher's start (the run returned before the watcher ran), the route
// logs the cancel itself, exactly once.
func TestALostCancelWatchStillLogsTheCancel(t *testing.T) {
	logs := disconnectedRun(t, func(app *App) {
		app.eventStream.afterFunc = func(context.Context, func()) func() bool { return func() bool { return true } }
	}, 0)
	if got := strings.Count(logs.String(), "context fabric investigation cancel effective"); got != 1 {
		t.Fatalf("%d cancel effective lines, want exactly 1:\n%s", got, logs.String())
	}
	if line := logLine(t, logs, "context fabric investigation cancel effective"); line["cause"] != "client_disconnected" {
		t.Fatalf("cancel line %v", line)
	}
}

// TestTheCancelLineIsWrittenBeforeTheResponseEnds: when the watcher has
// started, the route waits for its line, so the line is in the log by the
// time the handler returns (a slow watcher here takes 300 ms).
func TestTheCancelLineIsWrittenBeforeTheResponseEnds(t *testing.T) {
	logs := disconnectedRun(t, func(app *App) {
		app.eventStream.afterFunc = func(ctx context.Context, f func()) func() bool {
			var started atomic.Bool
			go func() {
				<-ctx.Done()
				started.Store(true)
				time.Sleep(300 * time.Millisecond)
				f()
			}()
			return func() bool { return !started.Load() }
		}
	}, 50*time.Millisecond)
	if got := strings.Count(logs.String(), "context fabric investigation cancel effective"); got != 1 {
		t.Fatalf("%d cancel effective lines when the handler returned, want exactly 1:\n%s", got, logs.String())
	}
}

// TestTheCancelLineIsStampedAtTheCancel: t_eff is the moment the run context
// ended, not the moment the run returned. The investigator here keeps
// running 1.5 s after it sees the cancel; the production watcher stamps t_eff
// at the cancel.
func TestTheCancelLineIsStampedAtTheCancel(t *testing.T) {
	before := time.Now()
	logs := disconnectedRun(t, func(*App) {}, 1500*time.Millisecond)
	line := logLine(t, logs, "context fabric investigation cancel effective")
	tEff, err := time.Parse(time.RFC3339Nano, fmt.Sprint(line["t_eff"]))
	if err != nil {
		t.Fatal(err)
	}
	if returned := time.Now(); returned.Sub(tEff) < time.Second {
		t.Fatalf("t_eff %s is %s before the handler returned at %s, want at least 1 s: it was stamped at the run's return, not at the cancel", tEff, returned.Sub(tEff), returned)
	}
	if tEff.Before(before) {
		t.Fatalf("t_eff %s is before the test started at %s", tEff, before)
	}
}

// panicOnMessage is a slog handler that panics on one message, to plant a
// panic outside the investigator after the stream started (round-1
// IS03-001).
type panicOnMessage struct {
	slog.Handler
	message string
}

func (p panicOnMessage) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == p.message {
		panic("planted: " + p.message)
	}
	return p.Handler.Handle(ctx, record)
}

func (p panicOnMessage) WithAttrs(attrs []slog.Attr) slog.Handler {
	return panicOnMessage{Handler: p.Handler.WithAttrs(attrs), message: p.message}
}

func (p panicOnMessage) WithGroup(name string) slog.Handler {
	return panicOnMessage{Handler: p.Handler.WithGroup(name), message: p.message}
}

// TestAPanicAfterTheStreamStartedEndsItWithAnInternalError: once the 200 and
// `accepted` are written, the response is the stream. A panic outside the
// investigator (here, in a log line the route writes after `accepted`, and
// again in the one it writes when the stream closes) must end the stream
// with an internal_error terminal and the grammar intact -- never reach the
// recovery middleware, which would write a JSON error body into the stream.
func TestAPanicAfterTheStreamStartedEndsItWithAnInternalError(t *testing.T) {
	for _, message := range []string{
		"context fabric investigation event stream opened",
		"context fabric investigation event stream closed",
	} {
		t.Run(message, func(t *testing.T) {
			app, token, logs := newStreamTestApp(t, investigatorOf(answering(t, "context_fabric_investigation_result.v1.json", nil)))
			app.logger = slog.New(panicOnMessage{Handler: app.logger.Handler(), message: message})
			stream := serveStream(t, app, token, investigationRequestBody(), nil)
			if stream.status != http.StatusOK {
				t.Fatalf("status %d (%s)", stream.status, stream.raw)
			}
			if bytes.Contains(stream.raw, []byte(`"schema_version":"error.v1"`)) && !bytes.Contains(stream.raw, []byte("event: error")) {
				t.Fatalf("a JSON error body was written into the stream:\n%s", stream.raw)
			}
			if err := contractsv1.ValidateContextFabricInvestigationEventStream(stream.events); err != nil {
				t.Fatalf("grammar: %v\n%s", err, stream.raw)
			}
			terminal := stream.events[len(stream.events)-1]
			switch message {
			case "context fabric investigation event stream opened":
				if terminal.Type != contractsv1.ContextFabricInvestigationEventError || terminal.Error.Error.Code != "internal_error" {
					t.Fatalf("terminal %+v, want an internal_error error event", terminal)
				}
			default:
				// The terminal was already written when the closing line
				// panicked: the stream keeps it, and nothing follows it.
				if terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
					t.Fatalf("terminal %+v, want the result written before the panic", terminal)
				}
				// abort sees the terminal and does nothing: no second
				// terminal is even attempted.
				if strings.Contains(logs.String(), "already carries its terminal event") {
					t.Fatalf("the abort tried a second terminal:\n%s", logs.String())
				}
			}
			if line := logLine(t, logs, "request panic recovered"); line["failure_class"] != "event_stream_panic" || line["request_id"] != streamTestRequestID {
				t.Fatalf("panic line %v", line)
			}
		})
	}
}
