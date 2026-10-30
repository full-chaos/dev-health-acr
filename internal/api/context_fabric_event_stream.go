package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The investigation event stream route writer (CHAOS-11911, plan of record
// §2.7.1-2.7.4, §2.7.6). The contract is internal/contracts/v1
// context_fabric_investigation_event.go; the engine side is
// internal/contextfabric/investigation_stage_events.go.
//
// One request, one stream:
//
//  1. `Accept: text/event-stream` exactly selects the stream. With
//     ACR_CONTEXT_FABRIC_EVENT_STREAM_ENABLED off the route answers 406
//     stream_unavailable before any admission work, and never falls back to
//     JSON. Any other Accept is the JSON response, unchanged.
//  2. Every check before the investigation runs (data stores, body, the model
//     gate) answers exactly as on the JSON path: its HTTP status and an
//     error.v1 body. The stream starts only when the investigation is
//     admitted: the 200, the stream headers and `accepted` are written
//     together, right before Investigate.
//  3. While Investigate runs, one pump goroutine owns the connection: it
//     writes a `stage` event per transition of the request's
//     InvestigationStageSink and a `heartbeat` after the heartbeat interval
//     with no other event.
//  4. When Investigate returns the pump drains and stops, and the handler --
//     now the only writer -- runs deliverInvestigationOutcome, the ONE delivery
//     the JSON path runs too, into a capture, and writes its outcome as the
//     one terminal: a 200 body becomes `result` byte for byte, an error body
//     becomes `error`. Nothing is written after it.
//
// Every frame write first moves the connection's write deadline to now plus
// investigationEventStreamWriteTimeout (http.ResponseController), so
// http.Server.WriteTimeout -- an absolute deadline for the whole response --
// cannot cut a long stream; a write that misses its own deadline still fails.
// Every wrapper between the server and the handler must Unwrap for this to
// reach the connection, and the route refuses to open a stream when it
// cannot set the deadline.
//
// Run lifecycle (INV-7, Phases 1-3): the connection owns the run. The run
// context is the request context, so a client that disconnects cancels the
// investigation; a failed frame write cancels it too. The moment the run
// context ends, one `context fabric investigation cancel effective` line
// records t_eff, the request id and the cause; the model runtime logs one
// `context fabric model attempt started` line per attempt, so "no attempt
// started after t_eff" is checkable from the logs alone.

// investigationEventStreamWriteTimeout is how long one frame write may take.
const investigationEventStreamWriteTimeout = 30 * time.Second

// The stream's byte caps (design "Ask Dev: browser calls ACR directly", §6;
// the browser counts the same way). Sizes are wire bytes, framing included.
const (
	// investigationEventFrameSlackBytes is what one frame may take beyond the
	// service's max serialized bytes: the event envelope and the framing.
	investigationEventFrameSlackBytes = 64 << 10
	// investigationEventStreamMaxBytes is the most the whole stream may take.
	investigationEventStreamMaxBytes = 8 << 20
)

// investigationEventStreamSettings is the stream's timing. NewApp sets the
// contract values; tests shorten them.
type investigationEventStreamSettings struct {
	// heartbeat is the silence after which the pump writes a heartbeat.
	heartbeat time.Duration
	// writeTimeout is the per-frame write deadline.
	writeTimeout time.Duration
	// reserveID reserves the result id of a stream whose request names no
	// submission (a submission's record reserves its own, CHAOS-11912).
	reserveID func() string
	// afterFunc watches the run context for the cancel effective line;
	// context.AfterFunc in production. Tests replace it to force each side of
	// the race between the watcher's start and its stop.
	afterFunc func(ctx context.Context, f func()) (stop func() bool)
	// frameMaxBytes is the most one frame may take: max serialized bytes plus
	// investigationEventFrameSlackBytes. A frame over it is not written.
	frameMaxBytes int64
	// totalMaxBytes is the most the stream may take. terminalReserve is the
	// part of it only the terminal may use (equal to frameMaxBytes, so any
	// terminal fits): non-terminal frames stop at totalMaxBytes minus it, and
	// the stream then ends with error{budget_refusal} inside the reserve.
	totalMaxBytes   int64
	terminalReserve int64
}

// withByteCaps sets the stream's byte caps for the service's max serialized
// bytes.
func (s investigationEventStreamSettings) withByteCaps(maxSerializedBytes int) investigationEventStreamSettings {
	s.frameMaxBytes = int64(maxSerializedBytes) + investigationEventFrameSlackBytes
	s.totalMaxBytes = investigationEventStreamMaxBytes
	s.terminalReserve = s.frameMaxBytes
	return s
}

// defaultInvestigationEventStreamSettings is the contract's timing. started_at
// and elapsed_ms use the wall clock, the clock the engine's stage sink stamps
// transitions with.
func defaultInvestigationEventStreamSettings(maxSerializedBytes int) investigationEventStreamSettings {
	return investigationEventStreamSettings{
		heartbeat:    contractsv1.ContextFabricInvestigationEventHeartbeatInterval,
		writeTimeout: investigationEventStreamWriteTimeout,
		reserveID:    contextfabric.NewInvestigationResultID,
		afterFunc:    context.AfterFunc,
	}.withByteCaps(maxSerializedBytes)
}

// wantsInvestigationEventStream reports whether the request asks for the
// stream: exactly one Accept value, exactly the stream media type.
func wantsInvestigationEventStream(r *http.Request) bool {
	values := r.Header.Values("Accept")
	return len(values) == 1 && strings.TrimSpace(values[0]) == contractsv1.ContextFabricInvestigationEventStreamMediaType
}

// writeInvestigationStreamUnavailable is the flag-off refusal of a stream
// request.
func (a *App) writeInvestigationStreamUnavailable(w http.ResponseWriter, r *http.Request) {
	a.logger.InfoContext(r.Context(), "context fabric investigation event stream refused",
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
		"reason", "stream_disabled",
	)
	writeError(w, r, contractsv1.ContextFabricStreamUnavailableHTTPStatus, contractsv1.ErrorCodeStreamUnavailable, contractsv1.ContextFabricStreamUnavailableMessage, false, nil)
}

// investigationOutcomeCapture is the ResponseWriter deliverInvestigationOutcome writes
// into on the stream path: it holds the status and body the JSON path would
// have sent, so the stream's terminal is built from exactly that.
type investigationOutcomeCapture struct {
	outer  http.ResponseWriter
	header http.Header
	status int
	body   bytes.Buffer
}

func newInvestigationOutcomeCapture(outer http.ResponseWriter) *investigationOutcomeCapture {
	return &investigationOutcomeCapture{outer: outer, header: http.Header{}}
}

func (c *investigationOutcomeCapture) Header() http.Header { return c.header }

func (c *investigationOutcomeCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *investigationOutcomeCapture) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(p)
}

// SetDenialCode passes writeError's denial classification to the real
// writer, so the access log and observability classify a streamed refusal
// the same as a JSON one.
func (c *investigationOutcomeCapture) SetDenialCode(code string) {
	if marker, ok := c.outer.(interface{ SetDenialCode(string) }); ok {
		marker.SetDenialCode(code)
	}
}

var (
	errInvestigationStreamTerminalWritten = errors.New("investigation event stream already carries its terminal event")
	errInvestigationStreamFrameOverCap    = errors.New("investigation event stream frame is over the frame cap")
	errInvestigationStreamBudgetExhausted = errors.New("investigation event stream reached its byte budget")
)

// investigationEventStream writes one request's frames. Every method that
// writes takes mu; the pump and the handler never write at the same time
// anyway (the handler writes the terminal only after the pump stopped), and
// the lock makes that a property of the type rather than of its caller.
type investigationEventStream struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	settings   investigationEventStreamSettings
	logger     *slog.Logger
	logCtx     context.Context
	requestID  string
	reserved   string
	startedAt  time.Time
	// onWriteFailure ends the run: nobody reads a stream that cannot be
	// written. onBudgetExhausted ends it too: a stream that reached its byte
	// budget has written its terminal.
	onWriteFailure    func()
	onBudgetExhausted func()

	mu          sync.Mutex
	check       contractsv1.ContextFabricInvestigationEventStreamCheck
	seq         int64
	lastElapsed int64
	open        contractsv1.ContextFabricInvestigationStage
	lastStarted contractsv1.ContextFabricInvestigationStage
	failed      error
	terminal    contractsv1.ContextFabricInvestigationEventType
	// written is the wire bytes written so far. budgetExhausted is set when a
	// non-terminal frame would have crossed the non-terminal budget and the
	// stream ended with error{budget_refusal}.
	written         int64
	budgetExhausted bool
}

// write puts one event on the wire. The caller holds s.mu.
func (s *investigationEventStream) write(event contractsv1.ContextFabricInvestigationEvent) error {
	if s.failed != nil {
		return s.failed
	}
	// After the budget terminal the pump may still offer a frame; it is the
	// budget's to refuse, not a second terminal.
	if s.budgetExhausted {
		return errInvestigationStreamBudgetExhausted
	}
	if s.terminal != "" {
		s.logger.ErrorContext(s.logCtx, "context fabric investigation event refused: the stream already carries its terminal event",
			"request_id", contextfabric.SanitizeLogAttr(s.requestID), "event_type", string(event.Type), "terminal", string(s.terminal),
			"failure_class", "event_stream_second_terminal")
		return errInvestigationStreamTerminalWritten
	}
	event.SchemaVersion = contractsv1.ContextFabricInvestigationEventSchema
	event.RequestID = s.requestID
	event.Seq = s.seq + 1
	// The contract's own stream check runs BEFORE the write, so this writer
	// can never put an event on the wire that the contract refuses. It runs on
	// a copy, committed only once the frame is written: a frame the byte caps
	// refuse never moves the check's state.
	check := s.check
	if err := check.Next(event); err != nil {
		s.logger.ErrorContext(s.logCtx, "context fabric investigation event refused by the stream contract",
			"request_id", contextfabric.SanitizeLogAttr(s.requestID), "seq", event.Seq, "event_type", string(event.Type),
			"failure_class", "event_stream_contract")
		return err
	}
	frame, err := contractsv1.MarshalContextFabricInvestigationEventFrame(event)
	if err != nil {
		return s.fail(err, event)
	}
	// The byte caps are measured on the frame itself, framing included.
	size := int64(len(frame))
	if size > s.settings.frameMaxBytes {
		s.logger.ErrorContext(s.logCtx, "context fabric investigation event refused: the frame is over the frame cap",
			"request_id", contextfabric.SanitizeLogAttr(s.requestID), "seq", event.Seq, "event_type", string(event.Type),
			"frame_bytes", size, "frame_max_bytes", s.settings.frameMaxBytes, "failure_class", "event_stream_frame_cap")
		return errInvestigationStreamFrameOverCap
	}
	if !event.Type.Terminal() && s.written+size > s.settings.totalMaxBytes-s.settings.terminalReserve {
		return s.exhaustBudget(event, size)
	}
	if event.Type.Terminal() && s.written+size > s.settings.totalMaxBytes {
		// Unreachable while the reserve is at least the frame cap; refused,
		// never written over the total.
		s.logger.ErrorContext(s.logCtx, "context fabric investigation event refused: the terminal is over the stream total",
			"request_id", contextfabric.SanitizeLogAttr(s.requestID), "seq", event.Seq, "event_type", string(event.Type),
			"written_bytes", s.written, "frame_bytes", size, "total_max_bytes", s.settings.totalMaxBytes, "failure_class", "event_stream_byte_budget")
		return errInvestigationStreamBudgetExhausted
	}
	if err := s.controller.SetWriteDeadline(time.Now().Add(s.settings.writeTimeout)); err != nil {
		return s.fail(err, event)
	}
	if _, err := s.w.Write(frame); err != nil {
		return s.fail(err, event)
	}
	if err := s.controller.Flush(); err != nil {
		return s.fail(err, event)
	}
	s.check = check
	s.seq = event.Seq
	s.written += size
	if event.ElapsedMS != nil {
		s.lastElapsed = *event.ElapsedMS
	}
	if event.Type.Terminal() {
		s.terminal = event.Type
	}
	return nil
}

// exhaustBudget ends a stream whose next non-terminal frame would cross the
// non-terminal budget: that frame is not written, error{budget_refusal} is
// written inside the terminal reserve, and the run is cancelled. The caller
// holds s.mu.
func (s *investigationEventStream) exhaustBudget(event contractsv1.ContextFabricInvestigationEvent, size int64) error {
	s.logger.WarnContext(s.logCtx, "context fabric investigation event stream reached its byte budget",
		"request_id", contextfabric.SanitizeLogAttr(s.requestID), "seq", event.Seq, "event_type", string(event.Type),
		"written_bytes", s.written, "frame_bytes", size, "non_terminal_max_bytes", s.settings.totalMaxBytes-s.settings.terminalReserve,
		"failure_class", "event_stream_byte_budget")
	envelope := s.budgetRefusalEnvelope("stream_byte_budget")
	err := s.write(contractsv1.ContextFabricInvestigationEvent{Type: contractsv1.ContextFabricInvestigationEventError, ReservedResultID: s.reserved, Stage: s.lastStarted, Error: &envelope})
	s.budgetExhausted = true
	if s.onBudgetExhausted != nil {
		s.onBudgetExhausted()
	}
	if err != nil {
		return err
	}
	return errInvestigationStreamBudgetExhausted
}

// errInvestigationStreamPanicked is the stream's failure after a panic on
// the pump goroutine.
var errInvestigationStreamPanicked = errors.New("investigation event stream: a panic on the pump goroutine")

// failAfterPanic marks the stream failed after a panic on the pump goroutine
// and ends the run, as a failed write does. Nothing more is written. (When a
// write had already failed, the run is cancelled again: cancelling is
// idempotent.)
func (s *investigationEventStream) failAfterPanic() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = errInvestigationStreamPanicked
	if s.onWriteFailure != nil {
		s.onWriteFailure()
	}
}

func (s *investigationEventStream) fail(err error, event contractsv1.ContextFabricInvestigationEvent) error {
	s.failed = err
	s.logger.WarnContext(s.logCtx, "context fabric investigation event stream write failed",
		"request_id", contextfabric.SanitizeLogAttr(s.requestID), "seq", event.Seq, "event_type", string(event.Type),
		"write_error", classifyWriteError(err))
	if s.onWriteFailure != nil {
		s.onWriteFailure()
	}
	return err
}

// elapsed is milliseconds since started_at, never below the last value
// written: a transition stamped before a heartbeat that was written first
// carries the heartbeat's elapsed_ms rather than going back.
func (s *investigationEventStream) elapsed(at time.Time) *int64 {
	ms := at.Sub(s.startedAt).Milliseconds()
	if ms < s.lastElapsed {
		ms = s.lastElapsed
	}
	return &ms
}

// accepted writes the first event. submissionID and threadID are the
// request's X-ACR-Submission-ID and X-ACR-Thread-ID, "" when it sends none.
func (s *investigationEventStream) accepted(budgetSeconds int64, submissionID, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	started := s.startedAt
	return s.write(contractsv1.ContextFabricInvestigationEvent{
		Type: contractsv1.ContextFabricInvestigationEventAccepted, ReservedResultID: s.reserved, SubmissionID: submissionID, ThreadID: threadID, StartedAt: &started, BudgetSeconds: budgetSeconds,
	})
}

// transition writes one engine transition, holding the stream to the
// contract's grammar the way the sink does: a stage never goes back, a stage
// still open when a later one starts is completed first, and only the open
// stage completes. The sink already keeps that order; the writer keeps it too,
// so a transition the sink dropped cannot break the stream.
func (s *investigationEventStream) transition(transition contextfabric.InvestigationStageTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stage := func(name contractsv1.ContextFabricInvestigationStage, state contractsv1.ContextFabricInvestigationStageState) error {
		return s.write(contractsv1.ContextFabricInvestigationEvent{
			Type: contractsv1.ContextFabricInvestigationEventStage, Stage: name, State: state, ElapsedMS: s.elapsed(transition.At),
		})
	}
	switch transition.State {
	case contractsv1.ContextFabricStageStarted:
		if s.lastStarted != "" && transition.Stage.Order() <= s.lastStarted.Order() {
			return nil
		}
		if s.open != "" {
			if err := stage(s.open, contractsv1.ContextFabricStageCompleted); err != nil {
				return err
			}
			s.open = ""
		}
		if err := stage(transition.Stage, contractsv1.ContextFabricStageStarted); err != nil {
			return err
		}
		s.open, s.lastStarted = transition.Stage, transition.Stage
	case contractsv1.ContextFabricStageCompleted:
		if s.open == "" || s.open != transition.Stage {
			return nil
		}
		if err := stage(transition.Stage, contractsv1.ContextFabricStageCompleted); err != nil {
			return err
		}
		s.open = ""
	}
	return nil
}

func (s *investigationEventStream) heartbeat() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(contractsv1.ContextFabricInvestigationEvent{
		Type: contractsv1.ContextFabricInvestigationEventHeartbeat, Stage: s.open, ElapsedMS: s.elapsed(time.Now()),
	})
}

// pump writes the run's progress until done is closed, then drains what the
// sink still holds and returns.
func (s *investigationEventStream) pump(transitions <-chan contextfabric.InvestigationStageTransition, done <-chan struct{}) {
	timer := time.NewTimer(s.settings.heartbeat)
	defer timer.Stop()
	for {
		select {
		case transition := <-transitions:
			_ = s.transition(transition)
			timer.Reset(s.settings.heartbeat)
		case <-timer.C:
			_ = s.heartbeat()
			timer.Reset(s.settings.heartbeat)
		case <-done:
			for {
				select {
				case transition := <-transitions:
					_ = s.transition(transition)
				default:
					return
				}
			}
		}
	}
}

// finish writes the terminal from what deliverInvestigationOutcome produced. Nothing
// captured means the request was cancelled and nobody reads the stream: no
// terminal, which a consumer reads as interrupted. An outcome the contract
// check refuses (a route defect, already logged by write) is replaced by an
// internal_error terminal, never by a silent end. It reports the terminal
// written, "" for none.
func (s *investigationEventStream) finish(outcome *investigationOutcomeCapture) contractsv1.ContextFabricInvestigationEventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A stream that reached its byte budget already carries its terminal,
	// error{budget_refusal}; the run's own outcome is not streamed.
	if s.budgetExhausted {
		return s.terminal
	}
	if outcome.status == 0 {
		return ""
	}
	event := contractsv1.ContextFabricInvestigationEvent{Type: contractsv1.ContextFabricInvestigationEventResult, Result: outcome.body.Bytes()}
	if outcome.status != http.StatusOK {
		var envelope contractsv1.ErrorEnvelope
		decoder := json.NewDecoder(bytes.NewReader(outcome.body.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err != nil {
			s.logger.ErrorContext(s.logCtx, "context fabric investigation error body could not be decoded for the stream",
				"request_id", contextfabric.SanitizeLogAttr(s.requestID), "http_status", outcome.status, "failure_class", "event_stream_error_body")
			envelope = s.internalErrorEnvelope()
		}
		event = contractsv1.ContextFabricInvestigationEvent{Type: contractsv1.ContextFabricInvestigationEventError, ReservedResultID: s.reserved, Stage: s.lastStarted, Error: &envelope}
	}
	err := s.write(event)
	if err == nil {
		return event.Type
	}
	if s.failed != nil || errors.Is(err, errInvestigationStreamTerminalWritten) {
		return ""
	}
	// An outcome over the frame cap did not fit the stream: budget_refusal,
	// the code the JSON path answers an answer over its bound with. Any other
	// refusal is a route defect: internal_error.
	fallback, served := s.internalErrorEnvelope(), "internal_error"
	if errors.Is(err, errInvestigationStreamFrameOverCap) {
		fallback, served = s.budgetRefusalEnvelope("stream_frame_cap"), "budget_refusal"
	}
	s.logger.ErrorContext(s.logCtx, "context fabric investigation outcome could not be streamed",
		"request_id", contextfabric.SanitizeLogAttr(s.requestID), "http_status", outcome.status, "outcome_type", string(event.Type),
		"served_code", served, "failure_class", "event_stream_outcome")
	if s.write(contractsv1.ContextFabricInvestigationEvent{Type: contractsv1.ContextFabricInvestigationEventError, ReservedResultID: s.reserved, Stage: s.lastStarted, Error: &fallback}) != nil {
		return ""
	}
	return contractsv1.ContextFabricInvestigationEventError
}

// abort ends a stream whose handler panicked: an internal_error terminal,
// unless the stream already carries a terminal or can no longer be written.
func (s *investigationEventStream) abort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal != "" || s.failed != nil {
		return
	}
	envelope := s.internalErrorEnvelope()
	_ = s.write(contractsv1.ContextFabricInvestigationEvent{Type: contractsv1.ContextFabricInvestigationEventError, ReservedResultID: s.reserved, Stage: s.lastStarted, Error: &envelope})
}

// budgetRefusalEnvelope is the terminal of a stream that ran out of bytes: the
// frame cap (stream_frame_cap) or the non-terminal budget (stream_byte_budget).
func (s *investigationEventStream) budgetRefusalEnvelope(reason string) contractsv1.ErrorEnvelope {
	return contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, RequestID: s.requestID, Error: contractsv1.ErrorDetail{
		Code: "budget_refusal", Message: "The Context Fabric investigation event stream exceeded its byte budget", HTTPStatus: http.StatusRequestEntityTooLarge,
		Details: map[string]any{"reason": reason, "frame_max_bytes": s.settings.frameMaxBytes, "stream_max_bytes": s.settings.totalMaxBytes},
	}}
}

func (s *investigationEventStream) internalErrorEnvelope() contractsv1.ErrorEnvelope {
	return contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, RequestID: s.requestID, Error: contractsv1.ErrorDetail{
		Code: "internal_error", Message: "The Context Fabric investigation outcome could not be streamed", HTTPStatus: http.StatusInternalServerError,
	}}
}

// investigationBudgetSeconds is the run budget the run context leaves at
// start, in whole seconds rounded up. The deadline is a wall-clock instant, so
// it is measured against the wall clock, never the stream's clock.
func investigationBudgetSeconds(ctx context.Context, wallStart time.Time) int64 {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 1
	}
	return max(1, int64(math.Ceil(deadline.Sub(wallStart).Seconds())))
}

// investigationEventStreamWritable proves, before any admission work, that
// the connection takes a write deadline: a writer that cannot would leave the
// stream to http.Server.WriteTimeout, which cuts it. It answers the request
// (500 internal_error) when it cannot.
func (a *App) investigationEventStreamWritable(w http.ResponseWriter, r *http.Request) bool {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(a.eventStream.writeTimeout)); err != nil {
		a.logger.ErrorContext(r.Context(), "context fabric investigation event stream cannot set a write deadline",
			"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "failure_class", "event_stream_writer_unsupported")
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The investigation event stream could not be opened", false, nil)
		return false
	}
	return true
}

// serveInvestigationEventStream runs an admitted investigation as an event
// stream and reports the status delivered, for the latency metric.
// investigateCtx already carries the reserved result id (the submission's, or
// one reserved for this stream). The outcome goes through
// deliverInvestigationOutcome, the one delivery the JSON path uses too.
func (a *App) serveInvestigationEventStream(w http.ResponseWriter, r *http.Request, investigator contextfabric.Investigator, principal storage.Principal, request contextfabric.InvestigationRequest, submission *admittedInvestigationSubmission, submissionID, threadID, reserved string, investigateCtx context.Context, collectedSynthesisInput func() *contractsv1.ContextFabricSynthesisInput) (delivered string) {
	requestID := RequestID(r.Context())
	controller := http.NewResponseController(w)
	runCtx, cancelRun := context.WithCancel(investigateCtx)
	defer cancelRun()
	stream := &investigationEventStream{
		w: w, controller: controller, settings: a.eventStream, logger: a.logger, logCtx: context.WithoutCancel(r.Context()),
		requestID: requestID, reserved: reserved, startedAt: time.Now(), onWriteFailure: cancelRun, onBudgetExhausted: cancelRun,
	}
	sink := contextfabric.NewInvestigationStageSink(contextfabric.InvestigationStageSinkCapacity)
	runCtx = contextfabric.WithInvestigationStageSink(runCtx, sink)
	logCancel := func() {
		a.logInvestigationCancelEffective(stream.logCtx, r.Context(), stream, requestID, reserved)
	}
	cancelLogged := make(chan struct{})
	stopCancelWatch := a.eventStream.afterFunc(runCtx, func() {
		defer close(cancelLogged)
		logCancel()
	})
	// endCancelWatch stops watching the run context. A cancel that already
	// happened is logged exactly once before it returns: by the watcher, which
	// it waits for, or here when the stop won the race with the watcher's
	// start (a cancel closes the run's Done channel before it starts the
	// watcher, so the run can return first).
	cancelWatchEnded := false
	endCancelWatch := func() {
		if cancelWatchEnded {
			return
		}
		cancelWatchEnded = true
		if !stopCancelWatch() {
			<-cancelLogged
			return
		}
		if runCtx.Err() != nil {
			logCancel()
		}
	}
	header := w.Header()
	header.Set("Content-Type", contractsv1.ContextFabricInvestigationEventStreamMediaType)
	header.Set("Cache-Control", "no-store, no-transform")
	header.Set("X-Accel-Buffering", "no")
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	// From the 200 on, the response IS the stream. A panic anywhere below
	// (outside the investigator, which investigateRecovered already turns
	// into an error) must end the stream with an internal_error terminal: if
	// it reached the recovery middleware, that would write a JSON error body
	// into the open stream.
	// outcomeDelivered is set when the run's outcome reaches the shared
	// delivery, which decides the submission's record (done or abandoned).
	// Every exit before it -- a first frame that cannot be written, a panic
	// -- abandons the record: no run will finish it, and a retry of the key
	// must not read "in progress" for a run that does not exist.
	outcomeDelivered := false
	abandonUndelivered := func() {
		if !outcomeDelivered {
			outcomeDelivered = true
			a.abandonInvestigationSubmission(r, principal, submission)
		}
	}
	pumpStarted, pumpStopped := false, false
	done, pumped := make(chan struct{}), make(chan struct{})
	stopPump := func() {
		if pumpStarted && !pumpStopped {
			pumpStopped = true
			close(done)
			<-pumped
		}
	}
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		stopPump()
		a.logger.ErrorContext(stream.logCtx, "request panic recovered",
			"request_id", contextfabric.SanitizeLogAttr(requestID), "status", http.StatusOK, "failure_class", "event_stream_panic")
		cancelRun()
		endCancelWatch()
		stream.abort()
		abandonUndelivered()
		delivered = "error"
	}()
	budget := investigationBudgetSeconds(runCtx, time.Now())
	if err := stream.accepted(budget, submissionID, threadID); err != nil {
		endCancelWatch()
		abandonUndelivered()
		return "error"
	}
	a.logger.InfoContext(r.Context(), "context fabric investigation event stream opened",
		"request_id", contextfabric.SanitizeLogAttr(requestID), "reserved_result_id", contextfabric.SanitizeLogAttr(reserved), "budget_seconds", budget)

	pumpStarted = true
	go func() {
		defer close(pumped)
		// The pump runs on its own goroutine, where the handler's recovery
		// cannot see a panic: it is recovered here, or it would crash the
		// process. The writer that panicked is not trusted again, so the
		// stream is marked failed and the run cancelled; the response ends
		// without a terminal, which a consumer reads as interrupted.
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.ErrorContext(stream.logCtx, "request panic recovered",
					"request_id", contextfabric.SanitizeLogAttr(requestID), "status", http.StatusOK, "failure_class", "event_stream_panic")
				stream.failAfterPanic()
			}
		}()
		stream.pump(sink.Transitions(), done)
	}()
	result, err := investigateRecovered(runCtx, investigator, principal, request)
	stopPump()
	endCancelWatch()
	a.recordInvestigationEventsDropped(stream.logCtx, requestID, sink.DroppedByStage())

	outcome := newInvestigationOutcomeCapture(w)
	outcomeDelivered = true
	delivered = a.deliverInvestigationOutcome(outcome, r, principal, request, submission, result, err, collectedSynthesisInput)
	terminal := stream.finish(outcome)
	stream.mu.Lock()
	frames, writeFailed, written, budgetExhausted := stream.seq, stream.failed != nil, stream.written, stream.budgetExhausted
	stream.mu.Unlock()
	a.logger.InfoContext(r.Context(), "context fabric investigation event stream closed",
		"request_id", contextfabric.SanitizeLogAttr(requestID), "frames", frames, "terminal", string(terminal),
		"outcome_status", outcome.status, "events_dropped", sink.Dropped(), "write_failed", writeFailed,
		"bytes_written", written, "budget_exhausted", budgetExhausted)
	if terminal == "" {
		return "error"
	}
	return delivered
}

// logInvestigationCancelEffective is the cancel_effective line: the moment
// the run context ended (t_eff), with what ended it.
func (a *App) logInvestigationCancelEffective(logCtx, requestCtx context.Context, stream *investigationEventStream, requestID, reserved string) {
	// t_eff first: the lock below can be held by a frame write for as long as
	// the connection takes (up to the write deadline), and t_eff is the moment
	// the run context ended, not the moment the line could be written.
	tEff := time.Now().UTC()
	stream.mu.Lock()
	writeFailed, budgetExhausted := stream.failed != nil, stream.budgetExhausted
	stream.mu.Unlock()
	cause := "run_cancelled"
	switch {
	case writeFailed:
		cause = "write_failed"
	case budgetExhausted:
		cause = "stream_byte_budget"
	case errors.Is(requestCtx.Err(), context.DeadlineExceeded):
		cause = "deadline"
	case errors.Is(requestCtx.Err(), context.Canceled):
		cause = "client_disconnected"
	}
	a.logger.InfoContext(logCtx, "context fabric investigation cancel effective",
		"request_id", contextfabric.SanitizeLogAttr(requestID), "reserved_result_id", contextfabric.SanitizeLogAttr(reserved),
		"cause", cause, "t_eff", tEff.Format(time.RFC3339Nano))
}

// recordInvestigationEventsDropped counts and logs the stage transitions the
// request's sink did not deliver, one line per stage. It runs after the run,
// on the handler's goroutine: the sink runs no route code while it drops.
func (a *App) recordInvestigationEventsDropped(ctx context.Context, requestID string, dropped map[contractsv1.ContextFabricInvestigationStage]int64) {
	for _, stage := range contractsv1.ContextFabricInvestigationStages() {
		count := dropped[stage]
		if count <= 0 {
			continue
		}
		a.metrics.InvestigationEventsDropped(ctx, string(stage), count)
		a.logger.WarnContext(ctx, "context fabric investigation events dropped",
			"request_id", contextfabric.SanitizeLogAttr(requestID), "stage", string(stage), "count", count,
			"failure_class", "event_stream_sink_full")
	}
}
