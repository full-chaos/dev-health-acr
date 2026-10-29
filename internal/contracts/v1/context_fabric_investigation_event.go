package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// The investigation event stream (CHAOS-11909, plan of record §2.7.1-2.7.2).
//
// POST /api/v1/context-fabric/investigations with `Accept: text/event-stream`
// answers with a stream of Server-Sent Events instead of one JSON body. Each
// frame is
//
//	id: <seq>
//	event: <type>
//	data: <one context_fabric_investigation_event.v1 object, compact JSON>
//	<blank line>
//
// and the stream follows one grammar (ContextFabricInvestigationEventStreamCheck):
//
//   - `accepted` first, exactly once, written with the response headers right
//     after admission. It carries the reserved result id, the start time and
//     the run budget.
//   - `stage` events at the engine's stage boundaries, in the fixed order of
//     ContextFabricInvestigationStages. A stage may be skipped, never revisited,
//     and at most one stage is open at a time.
//   - `heartbeat` every ContextFabricInvestigationEventHeartbeatInterval with no
//     other event, so no hop between ACR and the browser is silent for longer
//     than its idle limit (INV-3).
//   - exactly one terminal, `result` or `error`, last. `result.result` is the
//     JSON body the same request gets without the stream, byte for byte;
//     `error.error` is the error.v1 envelope the same failure gets as JSON.
//     A stream that ends without a terminal is interrupted -- never answered,
//     never failed.
//
// Progress events carry no claims: no subject, label, count, fact, prompt or
// model text. Stage ids are internal tokens a consumer maps to its own words.
//
// The flag ACR_CONTEXT_FABRIC_EVENT_STREAM_ENABLED gates the stream. With the
// flag off, a request that asks for the stream gets 406 stream_unavailable at
// once, before any admission work; there is no JSON fallback. Any other Accept
// value keeps the JSON response unchanged.
//
// This file is the contract only. The engine sink (CHAOS-11910) and the route
// writer (CHAOS-11911) produce it; submission binding (CHAOS-11912) and the
// thread link (CHAOS-11913) fill the optional submission_id and thread_id of
// `accepted`.

// ContextFabricInvestigationEventSchema is the schema_version of every event.
const ContextFabricInvestigationEventSchema = "context_fabric_investigation_event.v1"

// ContextFabricInvestigationEventStreamMediaType is the Accept value that
// selects the stream, and the Content-Type of a streamed response. Only this
// exact value selects it.
const ContextFabricInvestigationEventStreamMediaType = "text/event-stream"

// ContextFabricInvestigationEventHeartbeatInterval is the longest a stream
// stays silent: a heartbeat is written when no other event was written for
// this long. It is shorter than every idle limit between ACR and the browser
// (prod ingress proxy-read-timeout is 60 s).
const ContextFabricInvestigationEventHeartbeatInterval = 10 * time.Second

// Request headers whose values an `accepted` event echoes. The route reads
// them from CHAOS-11912 (submission) and CHAOS-11913 (thread) on; until then
// `accepted` carries neither field. Both values follow
// ValidContextFabricClientCorrelationID.
const (
	ContextFabricSubmissionIDHeader = "X-ACR-Submission-ID"
	ContextFabricThreadIDHeader     = "X-ACR-Thread-ID"
)

// The pre-stream refusal of a stream request while the stream flag is off.
// It is an error.v1 envelope with this code, status and message, written
// before admission work. retryable is false: the same request is refused
// until the deployment turns the stream on.
const (
	ErrorCodeStreamUnavailable = "stream_unavailable"
	// 406 Not Acceptable: the server cannot produce the representation the
	// Accept header asks for. A literal, because this package does not import
	// net/http.
	ContextFabricStreamUnavailableHTTPStatus = 406
	ContextFabricStreamUnavailableMessage    = "The investigation event stream is not enabled on this deployment"
)

// contextFabricEventMaxInteger is the largest integer a JavaScript consumer
// reads exactly (Number.MAX_SAFE_INTEGER). Every integer of the event stays
// at or below it.
const contextFabricEventMaxInteger = 1<<53 - 1

// ContextFabricInvestigationEventType is the closed event vocabulary. Adding a
// member is an in-major addition that needs the consumer pin bump before it is
// emitted (AGENTS.md, CONVENTIONS).
type ContextFabricInvestigationEventType string

const (
	ContextFabricInvestigationEventAccepted  ContextFabricInvestigationEventType = "accepted"
	ContextFabricInvestigationEventStage     ContextFabricInvestigationEventType = "stage"
	ContextFabricInvestigationEventHeartbeat ContextFabricInvestigationEventType = "heartbeat"
	ContextFabricInvestigationEventResult    ContextFabricInvestigationEventType = "result"
	ContextFabricInvestigationEventError     ContextFabricInvestigationEventType = "error"
)

// ContextFabricInvestigationEventTypes lists every member, in the order a
// stream can carry them.
func ContextFabricInvestigationEventTypes() []ContextFabricInvestigationEventType {
	return []ContextFabricInvestigationEventType{
		ContextFabricInvestigationEventAccepted,
		ContextFabricInvestigationEventStage,
		ContextFabricInvestigationEventHeartbeat,
		ContextFabricInvestigationEventResult,
		ContextFabricInvestigationEventError,
	}
}

// Terminal reports whether the type ends the stream.
func (t ContextFabricInvestigationEventType) Terminal() bool {
	return t == ContextFabricInvestigationEventResult || t == ContextFabricInvestigationEventError
}

// ContextFabricInvestigationStage is the closed, ordered stage vocabulary of
// the stream. It is a wire vocabulary: the engine's internal failure stages
// (internal/contextfabric InvestigationStage) are a different, finer set.
type ContextFabricInvestigationStage string

const (
	ContextFabricStageInterpreting      ContextFabricInvestigationStage = "interpreting"
	ContextFabricStageResolvingSubjects ContextFabricInvestigationStage = "resolving_subjects"
	ContextFabricStageReadingFacts      ContextFabricInvestigationStage = "reading_facts"
	ContextFabricStageSynthesizing      ContextFabricInvestigationStage = "synthesizing"
	ContextFabricStageValidating        ContextFabricInvestigationStage = "validating"
)

// ContextFabricInvestigationStages lists every stage in the one order a stream
// may carry them.
func ContextFabricInvestigationStages() []ContextFabricInvestigationStage {
	return []ContextFabricInvestigationStage{
		ContextFabricStageInterpreting,
		ContextFabricStageResolvingSubjects,
		ContextFabricStageReadingFacts,
		ContextFabricStageSynthesizing,
		ContextFabricStageValidating,
	}
}

// Order is the stage's position in ContextFabricInvestigationStages, from 1,
// or 0 for a value outside the vocabulary.
func (s ContextFabricInvestigationStage) Order() int {
	for index, stage := range ContextFabricInvestigationStages() {
		if stage == s {
			return index + 1
		}
	}
	return 0
}

// ContextFabricInvestigationStageState says whether a stage event opens or
// closes its stage.
type ContextFabricInvestigationStageState string

const (
	ContextFabricStageStarted   ContextFabricInvestigationStageState = "started"
	ContextFabricStageCompleted ContextFabricInvestigationStageState = "completed"
)

func validContextFabricStageState(state ContextFabricInvestigationStageState) bool {
	return state == ContextFabricStageStarted || state == ContextFabricStageCompleted
}

// contextFabricClientCorrelationIDPattern is the grammar of a client-minted
// submission or thread id: 8 to 128 characters that are safe in a header, a
// log field and a URL path segment. A UUID matches it.
var contextFabricClientCorrelationIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

// ValidContextFabricClientCorrelationID reports whether value is a valid
// submission or thread id.
func ValidContextFabricClientCorrelationID(value string) bool {
	return contextFabricClientCorrelationIDPattern.MatchString(value)
}

// ContextFabricInvestigationEvent is one event of the stream. It is one Go
// type for every event type; Validate holds each type to the fields the
// schema allows it. A field that is zero (nil for the pointers) is absent on
// the wire.
type ContextFabricInvestigationEvent struct {
	SchemaVersion string                              `json:"schema_version"`
	Type          ContextFabricInvestigationEventType `json:"type"`
	// RequestID is the request's X-Request-ID, the same on every event.
	RequestID string `json:"request_id"`
	// Seq starts at 1 on `accepted` and grows by one per event the writer
	// writes. The writer assigns it, so a gap means a frame was lost in
	// transit. The SSE id field carries the same number.
	Seq int64 `json:"seq"`
	// ReservedResultID (accepted, error) is the result id minted at
	// admission. The stored result carries it, except on answer reuse, where
	// the terminal result's own result_id wins.
	ReservedResultID string `json:"reserved_result_id,omitempty"`
	// SubmissionID and ThreadID (accepted, optional) echo the request's
	// ContextFabricSubmissionIDHeader and ContextFabricThreadIDHeader.
	SubmissionID string `json:"submission_id,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
	// StartedAt (accepted) is when the run started; ElapsedMS counts from it.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// BudgetSeconds (accepted) is the run budget: the request deadline, the
	// smaller of ACR_REQUEST_TIMEOUT and the caller's wait budget, in whole
	// seconds rounded up.
	BudgetSeconds int64 `json:"budget_seconds,omitempty"`
	// Stage is the stage a stage event opens or closes; on a heartbeat, the
	// open stage, absent when none is open; on an error, the last stage
	// started, absent when none started.
	Stage ContextFabricInvestigationStage      `json:"stage,omitempty"`
	State ContextFabricInvestigationStageState `json:"state,omitempty"`
	// ElapsedMS (stage, heartbeat) is milliseconds since StartedAt. It never
	// decreases along a stream.
	ElapsedMS *int64 `json:"elapsed_ms,omitempty"`
	// Result (result) is the JSON response body the request gets without the
	// stream, verbatim: context_fabric_investigation_response v1 or v2, or the
	// bounded context_fabric_answer_projection.v1. It must be compact JSON so
	// it travels on one data line unchanged.
	Result json.RawMessage `json:"result,omitempty"`
	// Error (error) is the error.v1 envelope the same failure gets as a JSON
	// response, http_status included.
	Error *ErrorEnvelope `json:"error,omitempty"`
}

// contextFabricEventField names the type-dependent fields, by wire key.
type contextFabricEventField string

const (
	eventFieldReservedResultID contextFabricEventField = "reserved_result_id"
	eventFieldSubmissionID     contextFabricEventField = "submission_id"
	eventFieldThreadID         contextFabricEventField = "thread_id"
	eventFieldStartedAt        contextFabricEventField = "started_at"
	eventFieldBudgetSeconds    contextFabricEventField = "budget_seconds"
	eventFieldStage            contextFabricEventField = "stage"
	eventFieldState            contextFabricEventField = "state"
	eventFieldElapsedMS        contextFabricEventField = "elapsed_ms"
	eventFieldResult           contextFabricEventField = "result"
	eventFieldError            contextFabricEventField = "error"
)

// contextFabricEventFields lists every type-dependent field in wire order.
var contextFabricEventFields = []contextFabricEventField{
	eventFieldReservedResultID, eventFieldSubmissionID, eventFieldThreadID, eventFieldStartedAt,
	eventFieldBudgetSeconds, eventFieldStage, eventFieldState, eventFieldElapsedMS,
	eventFieldResult, eventFieldError,
}

// contextFabricEventShape is what one event type requires and permits beyond
// the four common fields.
type contextFabricEventShape struct {
	required []contextFabricEventField
	optional []contextFabricEventField
}

// contextFabricEventShapes is the per-type field table. The schema's allOf
// if/then rules say the same thing; the parity test holds the two equal.
var contextFabricEventShapes = map[ContextFabricInvestigationEventType]contextFabricEventShape{
	ContextFabricInvestigationEventAccepted: {
		required: []contextFabricEventField{eventFieldReservedResultID, eventFieldStartedAt, eventFieldBudgetSeconds},
		optional: []contextFabricEventField{eventFieldSubmissionID, eventFieldThreadID},
	},
	ContextFabricInvestigationEventStage: {
		required: []contextFabricEventField{eventFieldStage, eventFieldState, eventFieldElapsedMS},
	},
	ContextFabricInvestigationEventHeartbeat: {
		required: []contextFabricEventField{eventFieldElapsedMS},
		optional: []contextFabricEventField{eventFieldStage},
	},
	ContextFabricInvestigationEventResult: {
		required: []contextFabricEventField{eventFieldResult},
	},
	ContextFabricInvestigationEventError: {
		required: []contextFabricEventField{eventFieldError, eventFieldReservedResultID},
		optional: []contextFabricEventField{eventFieldStage},
	},
}

// presentFields lists the type-dependent fields the event carries on the wire.
func (e ContextFabricInvestigationEvent) presentFields() map[contextFabricEventField]bool {
	present := map[contextFabricEventField]bool{}
	mark := func(field contextFabricEventField, set bool) {
		if set {
			present[field] = true
		}
	}
	mark(eventFieldReservedResultID, e.ReservedResultID != "")
	mark(eventFieldSubmissionID, e.SubmissionID != "")
	mark(eventFieldThreadID, e.ThreadID != "")
	mark(eventFieldStartedAt, e.StartedAt != nil)
	mark(eventFieldBudgetSeconds, e.BudgetSeconds != 0)
	mark(eventFieldStage, e.Stage != "")
	mark(eventFieldState, e.State != "")
	mark(eventFieldElapsedMS, e.ElapsedMS != nil)
	mark(eventFieldResult, len(e.Result) > 0)
	mark(eventFieldError, e.Error != nil)
	return present
}

// Validate checks one event against context_fabric_investigation_event.v1. It
// does not check the event's place in a stream; see
// ContextFabricInvestigationEventStreamCheck.
//
// The result body is checked only for its identity (one compact JSON object,
// a known schema_version, the event's request_id, a result_id): the body is
// the JSON response, which its own response schemas govern, and a stricter
// check here would let the stream refuse an answer the JSON response serves.
func (e ContextFabricInvestigationEvent) Validate() error {
	if e.SchemaVersion != ContextFabricInvestigationEventSchema {
		return fmt.Errorf("investigation event schema_version must be %q", ContextFabricInvestigationEventSchema)
	}
	shape, known := contextFabricEventShapes[e.Type]
	if !known {
		return fmt.Errorf("investigation event type is not a member of the closed vocabulary")
	}
	if !stringLengthBetween(e.RequestID, 1, 256) {
		return fmt.Errorf("investigation event request_id violates v1 bounds")
	}
	if e.Seq < 1 || e.Seq > contextFabricEventMaxInteger {
		return fmt.Errorf("investigation event seq must be between 1 and %d", int64(contextFabricEventMaxInteger))
	}
	present := e.presentFields()
	permitted := map[contextFabricEventField]bool{}
	for _, field := range shape.required {
		permitted[field] = true
		if !present[field] {
			return fmt.Errorf("investigation %s event requires %s", e.Type, field)
		}
	}
	for _, field := range shape.optional {
		permitted[field] = true
	}
	for _, field := range contextFabricEventFields {
		if present[field] && !permitted[field] {
			return fmt.Errorf("investigation %s event must not carry %s", e.Type, field)
		}
	}
	if present[eventFieldReservedResultID] && !stringLengthBetween(e.ReservedResultID, 8, 256) {
		return fmt.Errorf("investigation event reserved_result_id violates v1 bounds")
	}
	if present[eventFieldSubmissionID] && !ValidContextFabricClientCorrelationID(e.SubmissionID) {
		return fmt.Errorf("investigation event submission_id violates the client correlation id grammar")
	}
	if present[eventFieldThreadID] && !ValidContextFabricClientCorrelationID(e.ThreadID) {
		return fmt.Errorf("investigation event thread_id violates the client correlation id grammar")
	}
	if present[eventFieldStartedAt] && e.StartedAt.IsZero() {
		return fmt.Errorf("investigation event started_at must be a real time")
	}
	if present[eventFieldBudgetSeconds] && (e.BudgetSeconds < 1 || e.BudgetSeconds > contextFabricEventMaxInteger) {
		return fmt.Errorf("investigation event budget_seconds must be between 1 and %d", int64(contextFabricEventMaxInteger))
	}
	if present[eventFieldStage] && e.Stage.Order() == 0 {
		return fmt.Errorf("investigation event stage is not a member of the closed vocabulary")
	}
	if present[eventFieldState] && !validContextFabricStageState(e.State) {
		return fmt.Errorf("investigation event state is not a member of the closed vocabulary")
	}
	if present[eventFieldElapsedMS] && (*e.ElapsedMS < 0 || *e.ElapsedMS > contextFabricEventMaxInteger) {
		return fmt.Errorf("investigation event elapsed_ms must be between 0 and %d", int64(contextFabricEventMaxInteger))
	}
	if present[eventFieldResult] {
		if err := validateContextFabricEventResultBody(e.Result, e.RequestID); err != nil {
			return err
		}
	}
	if present[eventFieldError] {
		if err := validateContextFabricEventErrorEnvelope(*e.Error, e.RequestID); err != nil {
			return err
		}
	}
	return nil
}

// contextFabricEventResultSchemas are the schema_versions a result body may
// carry: every body the JSON 200 response of the route can be.
var contextFabricEventResultSchemas = map[string]bool{
	ContextFabricInvestigationResultSchema:   true,
	ContextFabricInvestigationResultSchemaV2: true,
	ContextFabricAnswerProjectionSchema:      true,
}

func validateContextFabricEventResultBody(body json.RawMessage, requestID string) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return fmt.Errorf("investigation result event body is not one JSON value: %w", err)
	}
	if !bytes.Equal(compact.Bytes(), body) {
		return fmt.Errorf("investigation result event body must be compact JSON, so it travels unchanged")
	}
	var identity struct {
		SchemaVersion string `json:"schema_version"`
		RequestID     string `json:"request_id"`
		ResultID      string `json:"result_id"`
	}
	if err := json.Unmarshal(body, &identity); err != nil {
		return fmt.Errorf("investigation result event body must be a JSON object: %w", err)
	}
	if !contextFabricEventResultSchemas[identity.SchemaVersion] {
		return fmt.Errorf("investigation result event body schema_version is not a create-investigation response")
	}
	if identity.RequestID != requestID {
		return fmt.Errorf("investigation result event body request_id differs from the event's")
	}
	if !stringLengthBetween(identity.ResultID, 8, 256) {
		return fmt.Errorf("investigation result event body result_id violates v1 bounds")
	}
	return nil
}

// errorCodes is the closed error.v1 code vocabulary; the published enum is
// held equal to it.
var errorCodes = []string{
	"invalid_request",
	"budget_refusal",
	"device_authorization_conflict",
	"invalid_token",
	"insufficient_scope",
	"feature_not_enabled",
	"repo_forbidden",
	"not_found",
	"rate_limited",
	"version_mismatch",
	"upstream_unavailable",
	"upstream_invalid_output",
	"store_unavailable",
	"interpretation_rejected",
	"synthesis_rejected",
	"internal_error",
	ErrorCodeStreamUnavailable,
}

func validErrorCode(code string) bool {
	for _, member := range errorCodes {
		if member == code {
			return true
		}
	}
	return false
}

func validateContextFabricEventErrorEnvelope(envelope ErrorEnvelope, requestID string) error {
	if envelope.SchemaVersion != ErrorSchema {
		return fmt.Errorf("investigation error event envelope schema_version must be %q", ErrorSchema)
	}
	if envelope.RequestID != requestID {
		return fmt.Errorf("investigation error event envelope request_id differs from the event's")
	}
	if !validErrorCode(envelope.Error.Code) {
		return fmt.Errorf("investigation error event code is not a member of the closed vocabulary")
	}
	if !stringLengthBetween(envelope.Error.Message, 1, 2000) {
		return fmt.Errorf("investigation error event message violates v1 bounds")
	}
	if envelope.Error.HTTPStatus < 400 || envelope.Error.HTTPStatus > 599 {
		return fmt.Errorf("investigation error event http_status must be between 400 and 599")
	}
	return nil
}

// MarshalContextFabricInvestigationEventData encodes the event's data payload:
// one line of compact JSON. HTML escaping is off, so a result body is carried
// byte for byte (encoding/json compacts an embedded json.RawMessage with the
// encoder's own escaping; with escaping on, a body produced without it would
// change on the way through).
func MarshalContextFabricInvestigationEventData(event ContextFabricInvestigationEvent) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(event); err != nil {
		return nil, fmt.Errorf("encode investigation event: %w", err)
	}
	data := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if bytes.ContainsAny(data, "\r\n") {
		return nil, fmt.Errorf("encoded investigation event spans more than one line")
	}
	return data, nil
}

// MarshalContextFabricInvestigationEventFrame encodes one complete SSE frame
// for the event: its id, event and data fields and the blank line that ends
// it.
func MarshalContextFabricInvestigationEventFrame(event ContextFabricInvestigationEvent) ([]byte, error) {
	data, err := MarshalContextFabricInvestigationEventData(event)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 0, len(data)+64)
	frame = append(frame, "id: "...)
	frame = strconv.AppendInt(frame, event.Seq, 10)
	frame = append(frame, "\nevent: "...)
	frame = append(frame, event.Type...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, data...)
	frame = append(frame, "\n\n"...)
	return frame, nil
}
