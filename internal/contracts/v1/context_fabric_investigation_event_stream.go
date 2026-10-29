package v1

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ContextFabricInvestigationEventStreamCheck holds one stream to the grammar
// of context_fabric_investigation_event.v1. Feed it every event in order with
// Next, then call Finish. The producer (the route writer) and every consumer
// test use this one check, so the grammar is stated once:
//
//   - the first event is `accepted`, with seq 1; no later event is `accepted`;
//   - seq grows by exactly one per event, and request_id never changes;
//   - elapsed_ms never decreases, and no two consecutive elapsed_ms values
//     (accepted counting as 0) are more than the heartbeat interval plus its
//     slack apart: a writer that let the stream go silent fails here. The
//     bound is on silence only: a heartbeat sooner than the interval is
//     valid, so a consumer accepts a heartbeat at any time;
//   - a stage starts only when no stage is open and only after every stage
//     already started (the fixed order, skips allowed, no repeats); it
//     completes only while it is the open stage;
//   - a heartbeat names the open stage, or no stage when none is open;
//   - `result` is written with no stage open;
//   - `error` repeats the reserved_result_id of `accepted` and names the last
//     stage started, or no stage when none started;
//   - exactly one terminal, and nothing after it.
//
// Finish fails a stream that ended without a terminal: that stream was
// interrupted, which a consumer must never read as answered or as failed.
type ContextFabricInvestigationEventStreamCheck struct {
	count            int
	requestID        string
	reservedResultID string
	lastSeq          int64
	lastElapsedMS    int64
	lastStarted      ContextFabricInvestigationStage
	open             ContextFabricInvestigationStage
	terminal         bool
}

// contextFabricEventMaxSilenceMS is the largest elapsed_ms step the stream
// check accepts.
const contextFabricEventMaxSilenceMS = int64((ContextFabricInvestigationEventHeartbeatInterval + ContextFabricInvestigationEventHeartbeatSlack) / time.Millisecond)

// ErrContextFabricInvestigationStreamInterrupted is Finish's error for a
// stream that ended before its terminal event.
var ErrContextFabricInvestigationStreamInterrupted = errors.New("investigation event stream ended without a terminal event")

// Next checks one event against the event schema and against its place in
// the stream.
func (c *ContextFabricInvestigationEventStreamCheck) Next(event ContextFabricInvestigationEvent) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("event %d: %w", c.count+1, err)
	}
	if c.terminal {
		return fmt.Errorf("event %d (%s): an event follows the terminal event", c.count+1, event.Type)
	}
	if c.count == 0 {
		if event.Type != ContextFabricInvestigationEventAccepted {
			return fmt.Errorf("event 1 is %s; a stream starts with accepted", event.Type)
		}
		if event.Seq != 1 {
			return fmt.Errorf("event 1 has seq %d; a stream starts at seq 1", event.Seq)
		}
		c.requestID = event.RequestID
		c.reservedResultID = event.ReservedResultID
	} else {
		if event.Type == ContextFabricInvestigationEventAccepted {
			return fmt.Errorf("event %d: a stream carries accepted once", c.count+1)
		}
		if event.Seq != c.lastSeq+1 {
			return fmt.Errorf("event %d has seq %d after seq %d; seq grows by one", c.count+1, event.Seq, c.lastSeq)
		}
		if event.RequestID != c.requestID {
			return fmt.Errorf("event %d changes request_id", c.count+1)
		}
	}
	if event.ElapsedMS != nil {
		if *event.ElapsedMS < c.lastElapsedMS {
			return fmt.Errorf("event %d: elapsed_ms %d is below the earlier %d", c.count+1, *event.ElapsedMS, c.lastElapsedMS)
		}
		if gap := *event.ElapsedMS - c.lastElapsedMS; gap > contextFabricEventMaxSilenceMS {
			return fmt.Errorf("event %d: %d ms passed since the previous elapsed_ms with no event; a heartbeat is due after %s (slack %s)", c.count+1, gap, ContextFabricInvestigationEventHeartbeatInterval, ContextFabricInvestigationEventHeartbeatSlack)
		}
		c.lastElapsedMS = *event.ElapsedMS
	}
	switch event.Type {
	case ContextFabricInvestigationEventStage:
		if err := c.stage(event); err != nil {
			return err
		}
	case ContextFabricInvestigationEventHeartbeat:
		if event.Stage != c.open {
			return fmt.Errorf("event %d: heartbeat names stage %q while the open stage is %q", c.count+1, event.Stage, c.open)
		}
	case ContextFabricInvestigationEventResult:
		if c.open != "" {
			return fmt.Errorf("event %d: result while stage %s is open", c.count+1, c.open)
		}
		c.terminal = true
	case ContextFabricInvestigationEventError:
		if event.ReservedResultID != c.reservedResultID {
			return fmt.Errorf("event %d: error reserved_result_id differs from accepted", c.count+1)
		}
		if event.Stage != c.lastStarted {
			return fmt.Errorf("event %d: error names stage %q, the last stage started is %q", c.count+1, event.Stage, c.lastStarted)
		}
		c.terminal = true
	}
	c.count++
	c.lastSeq = event.Seq
	return nil
}

func (c *ContextFabricInvestigationEventStreamCheck) stage(event ContextFabricInvestigationEvent) error {
	switch event.State {
	case ContextFabricStageStarted:
		if c.open != "" {
			return fmt.Errorf("event %d: stage %s starts while stage %s is open", c.count+1, event.Stage, c.open)
		}
		if c.lastStarted != "" && event.Stage.Order() <= c.lastStarted.Order() {
			return fmt.Errorf("event %d: stage %s starts after stage %s; stages run in one order and do not repeat", c.count+1, event.Stage, c.lastStarted)
		}
		c.open = event.Stage
		c.lastStarted = event.Stage
	case ContextFabricStageCompleted:
		if c.open != event.Stage {
			return fmt.Errorf("event %d: stage %s completes while the open stage is %q", c.count+1, event.Stage, c.open)
		}
		c.open = ""
	}
	return nil
}

// Finish reports whether the stream ended correctly: at least one event and
// a terminal event last.
func (c *ContextFabricInvestigationEventStreamCheck) Finish() error {
	if c.count == 0 {
		return fmt.Errorf("investigation event stream is empty")
	}
	if !c.terminal {
		return ErrContextFabricInvestigationStreamInterrupted
	}
	return nil
}

// ValidateContextFabricInvestigationEventStream checks a complete stream.
func ValidateContextFabricInvestigationEventStream(events []ContextFabricInvestigationEvent) error {
	var check ContextFabricInvestigationEventStreamCheck
	for _, event := range events {
		if err := check.Next(event); err != nil {
			return err
		}
	}
	return check.Finish()
}

// DecodeContextFabricInvestigationEventFrames reads a text/event-stream body
// written by MarshalContextFabricInvestigationEventFrame back into its events.
// It accepts exactly the writer's bytes, frame by frame: `id: <seq>`,
// `event: <type>` and `data: <the writer's encoding of the event>`, each line
// ending in "\n", then a blank line -- no comment line, no carriage return, no
// other field, no field spelled differently (SSE allows all of those; ACR's
// writer writes none of them, and this reader exists to hold that writer to
// its own form). The data must pass DecodeContextFabricInvestigationEventData,
// so each frame is the writer's frame for its event. maxFrameBytes
// bounds one frame. A body that ends inside a frame is an error. It does not
// check the stream grammar; ValidateContextFabricInvestigationEventStream does.
func DecodeContextFabricInvestigationEventFrames(body io.Reader, maxFrameBytes int) ([]ContextFabricInvestigationEvent, error) {
	if maxFrameBytes <= 0 {
		return nil, fmt.Errorf("maxFrameBytes must be positive")
	}
	reader := bufio.NewReader(body)
	var events []ContextFabricInvestigationEvent
	for {
		frame, err := readContextFabricEventFrame(reader, maxFrameBytes)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", len(events)+1, err)
		}
		if frame == nil {
			return events, nil
		}
		event, err := decodeContextFabricEventFrame(frame)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", len(events)+1, err)
		}
		events = append(events, event)
	}
}

// readContextFabricEventFrame reads up to and including the blank line that
// ends one frame. It returns nil, nil at a clean end of the body.
func readContextFabricEventFrame(reader *bufio.Reader, maxFrameBytes int) ([]byte, error) {
	var frame []byte
	for {
		line, err := reader.ReadSlice('\n')
		if bytes.IndexByte(line, '\r') >= 0 {
			return nil, fmt.Errorf("frame carries a carriage return; the writer ends every line with \\n")
		}
		frame = append(frame, line...)
		if len(frame) > maxFrameBytes {
			return nil, fmt.Errorf("exceeds %d bytes", maxFrameBytes)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(frame) == 0 {
				return nil, nil
			}
			return nil, fmt.Errorf("event stream ends inside the frame")
		}
		if err != nil {
			return nil, fmt.Errorf("read event stream: %w", err)
		}
		if bytes.Equal(line, []byte("\n")) {
			return frame, nil
		}
	}
}

func decodeContextFabricEventFrame(frame []byte) (ContextFabricInvestigationEvent, error) {
	lines := strings.Split(strings.TrimSuffix(string(frame), "\n\n"), "\n")
	prefixes := []string{"id: ", "event: ", "data: "}
	if len(lines) != len(prefixes) {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("frame has %d lines; the writer writes exactly `id: <seq>`, `event: <type>` and `data: <event>`", len(lines))
	}
	values := make([]string, len(lines))
	for index, prefix := range prefixes {
		value, found := strings.CutPrefix(lines[index], prefix)
		if !found {
			return ContextFabricInvestigationEvent{}, fmt.Errorf("frame line %d is not %q followed by its value", index+1, prefix)
		}
		values[index] = value
	}
	event, err := DecodeContextFabricInvestigationEventData([]byte(values[2]))
	if err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	if values[1] != string(event.Type) {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("SSE event %q differs from data type %q", values[1], event.Type)
	}
	if values[0] != strconv.FormatInt(event.Seq, 10) {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("SSE id %q differs from data seq %d", values[0], event.Seq)
	}
	// Three fixed prefixes, the writer's own id, type and data bytes, "\n"
	// line ends and one blank line: the frame is the writer's frame.
	return event, nil
}

// ParseContextFabricInvestigationEventData is the consumer decode of one
// event's data: it accepts every JSON spelling of a valid event (key order,
// whitespace and escapes are free) and refuses what the schema refuses:
//
//   - one JSON value, no unknown key, no key given twice at the top level;
//   - no key the event does not carry: a type-dependent field present with an
//     empty, zero or null value decodes to the Go zero value, which the type
//     reads as ABSENT, so a field the schema refuses would otherwise disappear;
//   - the error envelope, when present, names every key error.v1 requires and
//     none of them as null, and its details, when present, is an object (a
//     missing retryable would otherwise decode as false);
//   - Validate;
//   - a result body validated in full by the production validator of its own
//     schema_version (the response v1 or v2, or the answer projection), as the
//     schema validates it against the response schemas. The writer's Validate
//     checks only the body's identity, so the stream never refuses an answer
//     the JSON path serves; a consumer holds the body to its own contract.
//
// The result body's own wire presence (a key its schema requires, present as
// null or missing, deep inside the answer) is the same question for this body
// as for the JSON response body, and stays with that body's consumers
// (internal/sidecar's presence check); this function adds nothing to it.
func ParseContextFabricInvestigationEventData(data []byte) (ContextFabricInvestigationEvent, error) {
	if err := rejectDuplicateTopLevelKeys(data); err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	var event ContextFabricInvestigationEvent
	if err := decodeContextFabricEventStrict(data, &event); err != nil {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("decode data: %w", err)
	}
	if err := checkContextFabricEventEnvelopePresence(data); err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	if err := event.Validate(); err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	written, err := MarshalContextFabricInvestigationEventData(event)
	if err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	payloadKeys, err := topLevelKeys(data)
	if err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	writtenKeys, err := topLevelKeys(written)
	if err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	for key := range payloadKeys {
		if !writtenKeys[key] {
			return ContextFabricInvestigationEvent{}, fmt.Errorf("data carries %s with an empty, zero or null value; the writer omits an absent field", key)
		}
	}
	if len(event.Result) > 0 {
		if err := validateContextFabricEventResultBodyInFull(event.Result); err != nil {
			return ContextFabricInvestigationEvent{}, err
		}
	}
	return event, nil
}

// DecodeContextFabricInvestigationEventData is ParseContextFabricInvestigationEventData
// plus the writer's form: the data must be, byte for byte,
// MarshalContextFabricInvestigationEventData of the event it decodes to. Key
// order, whitespace, escapes, number spelling and a key given twice anywhere
// are therefore all fixed. It is what ACR's own stream reader uses; a generic
// consumer uses Parse.
func DecodeContextFabricInvestigationEventData(data []byte) (ContextFabricInvestigationEvent, error) {
	event, err := ParseContextFabricInvestigationEventData(data)
	if err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	written, err := MarshalContextFabricInvestigationEventData(event)
	if err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	if !bytes.Equal(written, data) {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("data is not the writer's encoding of its event (compact JSON, the writer's key order and spelling)")
	}
	return event, nil
}

func validateContextFabricEventResultBodyInFull(body json.RawMessage) error {
	var head struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return fmt.Errorf("investigation result event body must be a JSON object: %w", err)
	}
	switch head.SchemaVersion {
	case ContextFabricInvestigationResultSchema, ContextFabricInvestigationResultSchemaV2:
		var response ContextFabricInvestigationResponse
		if err := decodeContextFabricEventStrict(body, &response); err != nil {
			return fmt.Errorf("investigation result event body: %w", err)
		}
		if head.SchemaVersion == ContextFabricInvestigationResultSchema {
			if err := response.Validate(); err != nil {
				return fmt.Errorf("investigation result event body: %w", err)
			}
			return nil
		}
		if err := response.ContextFabricInvestigationResult.ValidateV2(); err != nil {
			return fmt.Errorf("investigation result event body: %w", err)
		}
		if response.SynthesisInput != nil {
			if err := response.SynthesisInput.Validate(); err != nil {
				return fmt.Errorf("investigation result event body: %w", err)
			}
		}
		return nil
	case ContextFabricAnswerProjectionSchema:
		var projection ContextFabricAnswerProjection
		if err := decodeContextFabricEventStrict(body, &projection); err != nil {
			return fmt.Errorf("investigation result event body: %w", err)
		}
		if err := projection.Validate(); err != nil {
			return fmt.Errorf("investigation result event body: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("investigation result event body schema_version is not a create-investigation response")
	}
}

func decodeContextFabricEventStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing content after the JSON value")
	}
	return nil
}

// checkContextFabricEventEnvelopePresence holds an error event's envelope to
// error.v1's required keys: encoding/json reads a missing or null key as its
// Go zero value, and for retryable that zero (false) is a valid value.
func checkContextFabricEventEnvelopePresence(data []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	raw, present := top["error"]
	if !present {
		return nil
	}
	envelope, err := requiredJSONObject(raw, "error", "schema_version", "request_id", "error")
	if err != nil {
		return err
	}
	detail, err := requiredJSONObject(envelope["error"], "error.error", "code", "message", "http_status", "retryable")
	if err != nil {
		return err
	}
	if details, present := detail["details"]; present {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(details, &object); err != nil || object == nil {
			return fmt.Errorf("data carries error.error.details that is not an object")
		}
	}
	return nil
}

func requiredJSONObject(raw json.RawMessage, path string, keys ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("data carries %s that is not an object", path)
	}
	for _, key := range keys {
		value, present := object[key]
		if !present || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("data carries %s without %s", path, key)
		}
	}
	return object, nil
}

// rejectDuplicateTopLevelKeys refuses an object that names a key twice:
// encoding/json keeps the last one silently, so the two sides of a parity
// check could read different values from one payload.
func rejectDuplicateTopLevelKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("decode data: an event is a JSON object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode data: %w", err)
		}
		key, _ := token.(string)
		if seen[key] {
			return fmt.Errorf("data names %s twice", key)
		}
		seen[key] = true
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			return fmt.Errorf("decode data: %w", err)
		}
	}
	return nil
}

func topLevelKeys(object []byte) (map[string]bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(object, &fields); err != nil {
		return nil, fmt.Errorf("decode data: %w", err)
	}
	keys := make(map[string]bool, len(fields))
	for key := range fields {
		keys[key] = true
	}
	return keys, nil
}
