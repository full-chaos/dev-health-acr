package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"
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
//   - `heartbeat` after ContextFabricInvestigationEventHeartbeatInterval with
//     no other event, so no hop between ACR and the browser is silent for
//     longer than its idle limit (INV-3); with the scheduling slack, no two
//     consecutive elapsed_ms values (accepted counting as 0) are more than
//     11 s apart. The bound is on silence: a heartbeat sooner than the
//     interval is valid, and a consumer accepts one at any time.
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

// ContextFabricInvestigationEventHeartbeatSlack is the scheduling allowance on
// top of the heartbeat interval. A writer's timer fires at or after its
// deadline, never before, so the contract bound is interval + slack: between
// `accepted` (elapsed 0) and each later event that carries elapsed_ms, and
// between two such events, at most 11 s pass. The stream check enforces it.
// The silence before a terminal is not measurable from the stream (a terminal
// carries no elapsed_ms), so the consumer's own stall watchdog covers it.
const ContextFabricInvestigationEventHeartbeatSlack = time.Second

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
	// StartedAt (accepted) is when the run started, a date-time; ElapsedMS
	// counts from it. Any date-time the schema accepts is valid here too.
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
	// it travels on one data line unchanged. Its request_id names the
	// request that produced the answer: this request, or on answer reuse the
	// earlier one, since a reused answer is served exactly as stored
	// (AC-3782-2), on the stream as in the JSON body.
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
// A result body is checked in full: one compact JSON object of a known
// schema_version with a result_id, then the production validator of that
// schema_version (the response v1 or v2, or the answer projection), as the
// schema checks it against the response schemas. So the writer, which calls
// Validate before it encodes, cannot put a body on the wire that the schema
// refuses; a route whose JSON 200 body fails its own contract gets a
// refusal here, not a stream that carries it.
//
// An error envelope's details are checked against error.v1's typed keys
// (reason, field, limit, deadline); other keys are free, as error.v1 allows.
//
// Every string the event carries -- each field, the error envelope, every key
// and value of its details, and the raw result body -- must be valid UTF-8:
// JSON and SSE are UTF-8, and encoding/json would replace an invalid byte with
// U+FFFD (or, inside a json.RawMessage, write it to the wire as it is), so the
// event written would not be the event validated.
func (e ContextFabricInvestigationEvent) Validate() error {
	if path, ok := everyStringIsUTF8(reflect.ValueOf(e), "event"); !ok {
		return fmt.Errorf("investigation event %s is not valid UTF-8", path)
	}
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
		if err := validateContextFabricEventResultBody(e.Result); err != nil {
			return err
		}
		if err := validateContextFabricEventResultBodyInFull(e.Result); err != nil {
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

// validateContextFabricEventResultBody checks how a result body travels: one
// compact JSON value, so it is carried unchanged.
// validateContextFabricEventResultBodyInFull then holds it to its
// schema_version -- the one list of the bodies the JSON 200 response of the
// route can be -- and to that version's own contract (an object, its
// result_id and request_id bounds included).
func validateContextFabricEventResultBody(body json.RawMessage) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return fmt.Errorf("investigation result event body is not one JSON value: %w", err)
	}
	if !bytes.Equal(compact.Bytes(), body) {
		return fmt.Errorf("investigation result event body must be compact JSON, so it travels unchanged")
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
	ErrorCodeSubmissionMismatch,
	ErrorCodeSubmissionInProgress,
	ErrorCodeSubmissionUnsaved,
	ErrorCodeSubmissionAbandoned,
	ErrorCodeSubmissionRequired,
	ErrorCodeConcurrencyLimited,
	ErrorCodeAllowanceExhausted,
	ErrorCodeThreadClosed,
	ErrorCodeAccessDenied,
	ErrorCodeNoRepository,
	ErrorCodeBudgetExhausted,
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
	return validateContextFabricEventErrorDetails(envelope.Error.Details)
}

var (
	errorDetailReasonPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
	errorDetailFieldPattern  = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)
)

// validateContextFabricEventErrorDetails holds details to error.v1's typed
// keys. A key error.v1 does not name is free (additionalProperties: true). A
// typed key present with a value its type refuses -- null included -- is
// refused, as the schema refuses it: a value that is not a string reads as
// "", which neither pattern (one or more characters) nor any enum member
// admits. The patterns admit ASCII only, so the byte length is the schema's
// character length.
func validateContextFabricEventErrorDetails(details map[string]any) error {
	patterned := func(key string, pattern *regexp.Regexp) error {
		value, present := details[key]
		if !present {
			return nil
		}
		text, _ := value.(string)
		if len(text) > 64 || !pattern.MatchString(text) {
			return fmt.Errorf("investigation error event details.%s must be a string of at most 64 characters matching %s", key, pattern)
		}
		return nil
	}
	enumerated := func(key string, members ...string) error {
		value, present := details[key]
		if !present {
			return nil
		}
		text, _ := value.(string)
		if !slices.Contains(members, text) {
			return fmt.Errorf("investigation error event details.%s must be one of %v", key, members)
		}
		return nil
	}
	if err := patterned("reason", errorDetailReasonPattern); err != nil {
		return err
	}
	if err := patterned("field", errorDetailFieldPattern); err != nil {
		return err
	}
	if err := enumerated("limit", "request_budget", "capabilities_budget"); err != nil {
		return err
	}
	return enumerated("deadline", "model", "request")
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

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// everyStringIsUTF8 reports whether every string reachable from value is valid
// UTF-8: strings, map keys, the elements of slices and arrays, the exported
// fields of structs (unexported fields are not encoded), through pointers and
// interfaces, and a json.RawMessage as raw bytes. It returns the path of the
// first string that is not, for the error text.
func everyStringIsUTF8(value reflect.Value, path string) (string, bool) {
	if value.Type() == rawMessageType {
		return path, utf8.Valid(value.Bytes())
	}
	switch value.Kind() {
	case reflect.String:
		return path, utf8.ValidString(value.String())
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return path, true
		}
		return everyStringIsUTF8(value.Elem(), path)
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if _, ok := everyStringIsUTF8(iter.Key(), path+" key"); !ok {
				return path + " key", false
			}
			if at, ok := everyStringIsUTF8(iter.Value(), fmt.Sprintf("%s.%v", path, iter.Key())); !ok {
				return at, false
			}
		}
		return path, true
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if at, ok := everyStringIsUTF8(value.Index(index), fmt.Sprintf("%s[%d]", path, index)); !ok {
				return at, false
			}
		}
		return path, true
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if !field.IsExported() {
				continue
			}
			if at, ok := everyStringIsUTF8(value.Field(index), path+"."+field.Name); !ok {
				return at, false
			}
		}
		return path, true
	default:
		return path, true
	}
}
