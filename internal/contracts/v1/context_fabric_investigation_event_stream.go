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
)

// ContextFabricInvestigationEventStreamCheck holds one stream to the grammar
// of context_fabric_investigation_event.v1. Feed it every event in order with
// Next, then call Finish. The producer (the route writer) and every consumer
// test use this one check, so the grammar is stated once:
//
//   - the first event is `accepted`, with seq 1; no later event is `accepted`;
//   - seq grows by exactly one per event, and request_id never changes;
//   - elapsed_ms never decreases;
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
// into its events. It is strict, because it reads ACR's own writer: a frame
// carries exactly the id, event and data fields (data may span lines, joined
// with a newline as SSE defines); a comment line (":" first) is skipped; the
// data is one event that decodes with no unknown field and validates; the
// SSE id equals seq and the SSE event equals type. maxFrameBytes bounds the
// bytes of one frame. A body that ends inside a frame is an error. It does
// not check the stream grammar; ValidateContextFabricInvestigationEventStream
// does.
func DecodeContextFabricInvestigationEventFrames(body io.Reader, maxFrameBytes int) ([]ContextFabricInvestigationEvent, error) {
	if maxFrameBytes <= 0 {
		return nil, fmt.Errorf("maxFrameBytes must be positive")
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 4096), maxFrameBytes+2)
	var (
		events     []ContextFabricInvestigationEvent
		frame      contextFabricEventFrame
		frameBytes int
	)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		frameBytes += len(line) + 1
		if frameBytes > maxFrameBytes {
			return nil, fmt.Errorf("frame %d exceeds %d bytes", len(events)+1, maxFrameBytes)
		}
		if line == "" {
			if frame.empty() {
				frameBytes = 0
				continue
			}
			event, err := frame.event()
			if err != nil {
				return nil, fmt.Errorf("frame %d: %w", len(events)+1, err)
			}
			events = append(events, event)
			frame, frameBytes = contextFabricEventFrame{}, 0
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if err := frame.add(line); err != nil {
			return nil, fmt.Errorf("frame %d: %w", len(events)+1, err)
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("frame %d exceeds %d bytes", len(events)+1, maxFrameBytes)
		}
		return nil, fmt.Errorf("read event stream: %w", err)
	}
	if !frame.empty() {
		return nil, fmt.Errorf("event stream ends inside frame %d", len(events)+1)
	}
	return events, nil
}

type contextFabricEventFrame struct {
	id, kind    *string
	data        []string
	sawDataLine bool
}

func (f contextFabricEventFrame) empty() bool {
	return f.id == nil && f.kind == nil && !f.sawDataLine
}

func (f *contextFabricEventFrame) add(line string) error {
	name, value, _ := strings.Cut(line, ":")
	value = strings.TrimPrefix(value, " ")
	switch name {
	case "id":
		if f.id != nil {
			return fmt.Errorf("frame carries id twice")
		}
		f.id = &value
	case "event":
		if f.kind != nil {
			return fmt.Errorf("frame carries event twice")
		}
		f.kind = &value
	case "data":
		f.data = append(f.data, value)
		f.sawDataLine = true
	default:
		return fmt.Errorf("frame carries field %q; an investigation event frame has id, event and data only", name)
	}
	return nil
}

func (f contextFabricEventFrame) event() (ContextFabricInvestigationEvent, error) {
	if f.id == nil || f.kind == nil || !f.sawDataLine {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("frame lacks id, event or data")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(strings.Join(f.data, "\n"))))
	decoder.DisallowUnknownFields()
	var event ContextFabricInvestigationEvent
	if err := decoder.Decode(&event); err != nil {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("decode data: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("data carries trailing content")
	}
	if err := event.Validate(); err != nil {
		return ContextFabricInvestigationEvent{}, err
	}
	if *f.kind != string(event.Type) {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("SSE event %q differs from data type %q", *f.kind, event.Type)
	}
	if *f.id != strconv.FormatInt(event.Seq, 10) {
		return ContextFabricInvestigationEvent{}, fmt.Errorf("SSE id %q differs from data seq %d", *f.id, event.Seq)
	}
	return event, nil
}
