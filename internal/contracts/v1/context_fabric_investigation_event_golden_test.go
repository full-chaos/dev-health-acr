package v1

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// The investigation event goldens (CHAOS-11909) are PRODUCED, never written
// by hand: every file under contracts/examples/v1 named
// context_fabric_investigation_event_* (and the stream_unavailable error) is
// the encoder's output for Go values built here from the production types.
// The embedded result bodies are the published result goldens decoded into
// the production types, validated, and encoded again exactly as the route
// encodes its JSON body (json.Marshal, marshalContextFabricResponse).
//
// To regenerate after a deliberate contract change:
//
//	ACR_WRITE_INVESTIGATION_EVENT_GOLDENS=1 go test ./internal/contracts/v1 -run TestInvestigationEventGoldens
//
// and review the diff. Without the variable the test fails on any difference.

const investigationEventSchemaFile = "context_fabric_investigation_event.v1.schema.json"

// investigationEventGoldenStart is the started_at of every golden stream.
var investigationEventGoldenStart = time.Date(2026, 8, 11, 16, 0, 0, 0, time.UTC)

func int64Ptr(value int64) *int64 { return &value }

// encodedGoldenBody decodes a published golden, validates it with the
// production validator for its schema, and encodes it the way the route
// encodes the JSON response body.
func encodedGoldenBody(t *testing.T, name string) []byte {
	t.Helper()
	raw := contextFabricGolden(t, name)
	var head struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatalf("golden %s: %v", name, err)
	}
	var value any
	switch head.SchemaVersion {
	case ContextFabricInvestigationResultSchema, ContextFabricInvestigationResultSchemaV2:
		var result ContextFabricInvestigationResult
		if err := decodeContextFabricStrict(raw, &result); err != nil {
			t.Fatalf("decode golden %s: %v", name, err)
		}
		validate := result.Validate
		if head.SchemaVersion == ContextFabricInvestigationResultSchemaV2 {
			validate = result.ValidateV2
		}
		if err := validate(); err != nil {
			t.Fatalf("golden %s does not validate: %v", name, err)
		}
		value = result
	case ContextFabricAnswerProjectionSchema:
		var projection ContextFabricAnswerProjection
		if err := decodeContextFabricStrict(raw, &projection); err != nil {
			t.Fatalf("decode golden %s: %v", name, err)
		}
		if err := projection.Validate(); err != nil {
			t.Fatalf("golden %s does not validate: %v", name, err)
		}
		value = projection
	default:
		t.Fatalf("golden %s has schema_version %q, not a create-investigation body", name, head.SchemaVersion)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// goldenErrorEnvelope decodes a published error golden into the production
// type and moves it onto requestID: a stream's envelope repeats the stream's
// request id.
func goldenErrorEnvelope(t *testing.T, name, requestID string) *ErrorEnvelope {
	t.Helper()
	var envelope ErrorEnvelope
	if err := decodeContextFabricStrict(contextFabricGolden(t, name), &envelope); err != nil {
		t.Fatalf("decode golden %s: %v", name, err)
	}
	envelope.RequestID = requestID
	return &envelope
}

type investigationEventGoldens struct {
	resultStream []ContextFabricInvestigationEvent
	errorStream  []ContextFabricInvestigationEvent
	// single maps each single-event golden file to its event.
	single map[string]ContextFabricInvestigationEvent
}

func buildInvestigationEventGoldens(t *testing.T) investigationEventGoldens {
	t.Helper()
	resultBody := encodedGoldenBody(t, "context_fabric_investigation_result.v1.json")
	var identity struct {
		RequestID string `json:"request_id"`
		ResultID  string `json:"result_id"`
	}
	if err := json.Unmarshal(resultBody, &identity); err != nil {
		t.Fatal(err)
	}
	requestID, reserved := identity.RequestID, identity.ResultID

	seq := int64(0)
	event := func(kind ContextFabricInvestigationEventType) ContextFabricInvestigationEvent {
		seq++
		return ContextFabricInvestigationEvent{SchemaVersion: ContextFabricInvestigationEventSchema, Type: kind, RequestID: requestID, Seq: seq}
	}
	accepted := func() ContextFabricInvestigationEvent {
		e := event(ContextFabricInvestigationEventAccepted)
		e.ReservedResultID = reserved
		e.StartedAt = timePtr(investigationEventGoldenStart)
		e.BudgetSeconds = 120
		return e
	}
	stage := func(name ContextFabricInvestigationStage, state ContextFabricInvestigationStageState, elapsed int64) ContextFabricInvestigationEvent {
		e := event(ContextFabricInvestigationEventStage)
		e.Stage, e.State, e.ElapsedMS = name, state, int64Ptr(elapsed)
		return e
	}
	heartbeat := func(open ContextFabricInvestigationStage, elapsed int64) ContextFabricInvestigationEvent {
		e := event(ContextFabricInvestigationEventHeartbeat)
		e.Stage, e.ElapsedMS = open, int64Ptr(elapsed)
		return e
	}

	resultStream := []ContextFabricInvestigationEvent{
		accepted(),
		stage(ContextFabricStageInterpreting, ContextFabricStageStarted, 4),
		stage(ContextFabricStageInterpreting, ContextFabricStageCompleted, 2210),
		stage(ContextFabricStageResolvingSubjects, ContextFabricStageStarted, 2211),
		stage(ContextFabricStageResolvingSubjects, ContextFabricStageCompleted, 3030),
		stage(ContextFabricStageReadingFacts, ContextFabricStageStarted, 3031),
		heartbeat(ContextFabricStageReadingFacts, 13031),
		stage(ContextFabricStageReadingFacts, ContextFabricStageCompleted, 14550),
		stage(ContextFabricStageSynthesizing, ContextFabricStageStarted, 14551),
		stage(ContextFabricStageSynthesizing, ContextFabricStageCompleted, 21980),
		stage(ContextFabricStageValidating, ContextFabricStageStarted, 21981),
		stage(ContextFabricStageValidating, ContextFabricStageCompleted, 22140),
	}
	result := event(ContextFabricInvestigationEventResult)
	result.Result = resultBody
	resultStream = append(resultStream, result)

	seq = 0
	errorStream := []ContextFabricInvestigationEvent{
		accepted(),
		stage(ContextFabricStageInterpreting, ContextFabricStageStarted, 3),
		stage(ContextFabricStageInterpreting, ContextFabricStageCompleted, 1980),
		stage(ContextFabricStageResolvingSubjects, ContextFabricStageStarted, 1981),
		stage(ContextFabricStageResolvingSubjects, ContextFabricStageCompleted, 2604),
		stage(ContextFabricStageReadingFacts, ContextFabricStageStarted, 2605),
		stage(ContextFabricStageReadingFacts, ContextFabricStageCompleted, 4120),
		stage(ContextFabricStageSynthesizing, ContextFabricStageStarted, 4121),
		heartbeat(ContextFabricStageSynthesizing, 14121),
	}
	modelDeadline := event(ContextFabricInvestigationEventError)
	modelDeadline.ReservedResultID = reserved
	modelDeadline.Stage = ContextFabricStageSynthesizing
	modelDeadline.Error = goldenErrorEnvelope(t, "error_upstream_unavailable_model_deadline.v1.json", requestID)
	errorStream = append(errorStream, modelDeadline)

	// Terminal events of other runs, each a stream's last event.
	terminal := func(kind ContextFabricInvestigationEventType, request string, at int64) ContextFabricInvestigationEvent {
		return ContextFabricInvestigationEvent{SchemaVersion: ContextFabricInvestigationEventSchema, Type: kind, RequestID: request, Seq: at}
	}
	bodyRequestID := func(body []byte) string {
		var head struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(body, &head); err != nil {
			t.Fatal(err)
		}
		return head.RequestID
	}
	v2Body := encodedGoldenBody(t, "context_fabric_investigation_result.v2.json")
	resultV2 := terminal(ContextFabricInvestigationEventResult, bodyRequestID(v2Body), 13)
	resultV2.Result = v2Body
	projectionBody := encodedGoldenBody(t, "context_fabric_answer_projection.v1.json")
	resultProjection := terminal(ContextFabricInvestigationEventResult, bodyRequestID(projectionBody), 13)
	resultProjection.Result = projectionBody

	requestDeadline := terminal(ContextFabricInvestigationEventError, requestID, 7)
	requestDeadline.ReservedResultID = reserved
	requestDeadline.Stage = ContextFabricStageReadingFacts
	requestDeadline.Error = &ErrorEnvelope{
		SchemaVersion: ErrorSchema,
		RequestID:     requestID,
		Error: ErrorDetail{
			Code:       "upstream_unavailable",
			Message:    "The Context Fabric investigation timed out",
			HTTPStatus: 504,
			Retryable:  true,
			Details:    map[string]any{DeadlineDetailsKey: DeadlineRequest},
		},
	}
	budgetRefusal := terminal(ContextFabricInvestigationEventError, requestID, 13)
	budgetRefusal.ReservedResultID = reserved
	budgetRefusal.Stage = ContextFabricStageValidating
	budgetRefusal.Error = goldenErrorEnvelope(t, "error_context_fabric_budget_refusal.v1.json", requestID)

	return investigationEventGoldens{
		resultStream: resultStream,
		errorStream:  errorStream,
		single: map[string]ContextFabricInvestigationEvent{
			"context_fabric_investigation_event_accepted.v1.json":               resultStream[0],
			"context_fabric_investigation_event_stage_started.v1.json":          resultStream[1],
			"context_fabric_investigation_event_stage_completed.v1.json":        resultStream[2],
			"context_fabric_investigation_event_heartbeat.v1.json":              resultStream[6],
			"context_fabric_investigation_event_result.v1.json":                 resultStream[len(resultStream)-1],
			"context_fabric_investigation_event_result_v2.v1.json":              resultV2,
			"context_fabric_investigation_event_result_projection.v1.json":      resultProjection,
			"context_fabric_investigation_event_error_model_deadline.v1.json":   modelDeadline,
			"context_fabric_investigation_event_error_request_deadline.v1.json": requestDeadline,
			"context_fabric_investigation_event_error_budget_refusal.v1.json":   budgetRefusal,
		},
	}
}

// streamUnavailableBody is the 406 body, encoded as the route's writeJSON
// encodes every error body (json.Encoder, one value and a newline).
func streamUnavailableBody(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := json.NewEncoder(&buffer).Encode(ErrorEnvelope{
		SchemaVersion: ErrorSchema,
		RequestID:     "req_01J0ACR011",
		Error: ErrorDetail{
			Code:       ErrorCodeStreamUnavailable,
			Message:    ContextFabricStreamUnavailableMessage,
			HTTPStatus: ContextFabricStreamUnavailableHTTPStatus,
			Retryable:  false,
		},
	}); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func encodeStreamFrames(t *testing.T, events []ContextFabricInvestigationEvent) []byte {
	t.Helper()
	var stream bytes.Buffer
	for _, event := range events {
		frame, err := MarshalContextFabricInvestigationEventFrame(event)
		if err != nil {
			t.Fatalf("encode frame seq %d: %v", event.Seq, err)
		}
		stream.Write(frame)
	}
	return stream.Bytes()
}

// compareOrWriteGolden holds a golden file equal to want, or writes it when
// regeneration was asked for.
func compareOrWriteGolden(t *testing.T, name string, want []byte) {
	t.Helper()
	path := repopath.Path(t, "contracts", "examples", "v1", name)
	if os.Getenv("ACR_WRITE_INVESTIGATION_EVENT_GOLDENS") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", name, len(want))
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with ACR_WRITE_INVESTIGATION_EVENT_GOLDENS=1)", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden %s differs from the encoder's output for its production values (%d bytes on disk, %d encoded); regenerate with ACR_WRITE_INVESTIGATION_EVENT_GOLDENS=1 and review the diff", name, len(got), len(want))
	}
}

func TestInvestigationEventGoldens(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	if len(goldens.single) != 10 {
		t.Fatalf("built %d single-event goldens, want 10", len(goldens.single))
	}
	for name, event := range goldens.single {
		t.Run(name, func(t *testing.T) {
			data, err := MarshalContextFabricInvestigationEventData(event)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			// The published schema accepts it as a PRODUCER document: every
			// key is declared.
			if err := contractcheck.ValidateSerializedStrict("", investigationEventSchemaFile, data); err != nil {
				t.Fatalf("schema rejects the encoder's own output: %v", err)
			}
			// It decodes back strictly and re-encodes to the same bytes.
			var decoded ContextFabricInvestigationEvent
			if err := decodeContextFabricStrict(data, &decoded); err != nil {
				t.Fatalf("strict decode: %v", err)
			}
			again, err := MarshalContextFabricInvestigationEventData(decoded)
			if err != nil || !bytes.Equal(again, data) {
				t.Fatalf("round trip changed the event (err %v)", err)
			}
			compareOrWriteGolden(t, name, append(data, '\n'))
		})
	}
	for name, events := range map[string][]ContextFabricInvestigationEvent{
		"context_fabric_investigation_event_stream_result.v1.sse": goldens.resultStream,
		"context_fabric_investigation_event_stream_error.v1.sse":  goldens.errorStream,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateContextFabricInvestigationEventStream(events); err != nil {
				t.Fatalf("golden stream breaks the stream grammar: %v", err)
			}
			compareOrWriteGolden(t, name, encodeStreamFrames(t, events))
		})
	}
	t.Run("error_context_fabric_stream_unavailable.v1.json", func(t *testing.T) {
		body := streamUnavailableBody(t)
		if err := contractcheck.ValidateSerializedStrict("", "error.v1.schema.json", body); err != nil {
			t.Fatalf("schema rejects the 406 body: %v", err)
		}
		compareOrWriteGolden(t, "error_context_fabric_stream_unavailable.v1.json", body)
	})
}

// TestInvestigationEventStreamGoldensReadBack reads the whole-stream goldens
// the way a consumer does: frame by frame, each data object against the
// published schema, then the grammar. It reads the FILES, so a hand edit that
// keeps the encoder test green cannot slip past it.
func TestInvestigationEventStreamGoldensReadBack(t *testing.T) {
	cases := map[string]ContextFabricInvestigationEventType{
		"context_fabric_investigation_event_stream_result.v1.sse": ContextFabricInvestigationEventResult,
		"context_fabric_investigation_event_stream_error.v1.sse":  ContextFabricInvestigationEventError,
	}
	for name, terminal := range cases {
		t.Run(name, func(t *testing.T) {
			raw := contextFabricGolden(t, name)
			events, err := DecodeContextFabricInvestigationEventFrames(bytes.NewReader(raw), 1<<20)
			if err != nil {
				t.Fatalf("decode frames: %v", err)
			}
			if err := ValidateContextFabricInvestigationEventStream(events); err != nil {
				t.Fatalf("grammar: %v", err)
			}
			if last := events[len(events)-1].Type; last != terminal {
				t.Fatalf("stream ends on %s, want %s", last, terminal)
			}
			stages := map[ContextFabricInvestigationStage]bool{}
			for _, event := range events {
				data, err := MarshalContextFabricInvestigationEventData(event)
				if err != nil {
					t.Fatal(err)
				}
				if err := contractcheck.ValidateSerializedStrict("", investigationEventSchemaFile, data); err != nil {
					t.Fatalf("seq %d: schema rejects the frame: %v", event.Seq, err)
				}
				if event.Type == ContextFabricInvestigationEventStage {
					stages[event.Stage] = true
				}
			}
			// The plan's first live exit needs at least two distinct stages
			// before a result; the goldens show the shape it reads.
			if len(stages) < 2 {
				t.Fatalf("stream shows %d distinct stages, want at least 2", len(stages))
			}
			if !strings.HasSuffix(string(raw), "\n\n") {
				t.Fatal("stream golden does not end with a complete frame")
			}
		})
	}
}

// TestInvestigationResultEventCarriesTheJSONBodyByteForByte pins the parity
// rule at the encoder: the data line of a result event contains the JSON body
// unchanged, including a body encoded WITHOUT HTML escaping. With escaping on
// (plain json.Marshal of the event) the second body would come out with
// < for '<' and this test fails.
func TestInvestigationResultEventCarriesTheJSONBodyByteForByte(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	for _, name := range []string{
		"context_fabric_investigation_event_result.v1.json",
		"context_fabric_investigation_event_result_v2.v1.json",
		"context_fabric_investigation_event_result_projection.v1.json",
	} {
		event := goldens.single[name]
		data, err := MarshalContextFabricInvestigationEventData(event)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, event.Result) {
			t.Fatalf("%s: the data line does not carry the body verbatim", name)
		}
	}

	var unescaped bytes.Buffer
	encoder := json.NewEncoder(&unescaped)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]any{
		"schema_version": ContextFabricInvestigationResultSchema,
		"request_id":     "request_12345678",
		"result_id":      "result_12345678",
		"question":       "is a<b && c>d?",
	}); err != nil {
		t.Fatal(err)
	}
	body := bytes.TrimSuffix(unescaped.Bytes(), []byte("\n"))
	if !bytes.Contains(body, []byte("a<b && c>d")) {
		t.Fatal("control: the body was escaped before the test could use it")
	}
	event := ContextFabricInvestigationEvent{SchemaVersion: ContextFabricInvestigationEventSchema, Type: ContextFabricInvestigationEventResult, RequestID: "request_12345678", Seq: 2, Result: body}
	data, err := MarshalContextFabricInvestigationEventData(event)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, body) {
		t.Fatalf("the encoder changed an unescaped body on the way through:\n body %s\n data %s", body, data)
	}
}
