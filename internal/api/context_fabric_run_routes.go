package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The attach and cancel routes of a leased run (CHAOS-11949, plan §2.7.9;
// the contract is internal/contracts/v1/context_fabric_run_lease.go). Both
// are the caller's own like the submission status read: the key includes the
// owner, so another principal's submission is not found by construction, and
// an unknown, a malformed id and a closed thread's turn answer the same 404.

// ContextFabricSubmissionEventsPath is the attach to a leased run's stream.
const ContextFabricSubmissionEventsPath = "/api/v1/context-fabric/investigations/submissions/{submission_id}/events"

// ContextFabricSubmissionCancelPath is the explicit cancel of a leased run.
const ContextFabricSubmissionCancelPath = "/api/v1/context-fabric/investigations/submissions/{submission_id}/cancel"

// ContextFabricRunAttachLogMessage and ContextFabricRunCancelLogMessage are
// the routes' decision lines.
const (
	ContextFabricRunAttachLogMessage = "context fabric investigation run attach"
	ContextFabricRunCancelLogMessage = "context fabric investigation run cancel"
)

// attachReadBatch bounds one log read of an attach.
const attachReadBatch = 64

// attachUnmarkedGracePolls is how many log reads an attach waits for the
// terminal of a run whose record is already final before it derives the
// terminal from the record (a run that ended between its record and its log).
const attachUnmarkedGracePolls = 4

func (a *App) logRunRoute(r *http.Request, message string, principal storage.Principal, outcome, reason string, err error, extra ...any) {
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"outcome", outcome,
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
	}
	if reason != "" {
		args = append(args, "reason", reason)
	}
	args = append(args, extra...)
	if err != nil {
		args = append(args, "error_class", submissionStoreErrorClass(err))
		a.logger.WarnContext(r.Context(), message, args...)
		return
	}
	a.logger.InfoContext(r.Context(), message, args...)
}

// runRouteRecord is the shared front of both routes: the deployment serves
// leased runs, the stores exist, and the id names a submission of the
// caller. ok is false when the response is written.
func (a *App) runRouteRecord(w http.ResponseWriter, r *http.Request, message string) (storage.Principal, contextfabric.SubmissionKey, contextfabric.RunStore, bool) {
	principal, authenticated := auth.PrincipalFromContext(r.Context())
	if !authenticated {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return storage.Principal{}, contextfabric.SubmissionKey{}, nil, false
	}
	notFound := func(reason string) {
		a.logRunRoute(r, message, principal, "not_found", reason, nil)
		writeError(w, r, http.StatusNotFound, "not_found", "Context Fabric investigation submission was not found", false, nil)
	}
	// With the run lease off the routes serve nothing: no run is leased.
	if !a.config.ContextFabricRunLeaseEnabled {
		notFound("run_lease_disabled")
		return principal, contextfabric.SubmissionKey{}, nil, false
	}
	runs := a.investigationRuns()
	if a.investigationSubmissions() == nil || runs == nil {
		a.logRunRoute(r, message, principal, "store_unavailable", "store_not_configured", nil)
		a.handleRuntimeUnavailable(w, r)
		return principal, contextfabric.SubmissionKey{}, nil, false
	}
	submissionID := r.PathValue("submission_id")
	if !contextfabric.ValidSubmissionID(submissionID) {
		notFound("malformed_id")
		return principal, contextfabric.SubmissionKey{}, nil, false
	}
	key, err := contextfabric.SubmissionKeyFor(principal, submissionID)
	if err != nil {
		notFound("owner_unknown")
		return principal, contextfabric.SubmissionKey{}, nil, false
	}
	return principal, key, runs, true
}

// ContextFabricSubmissionCancelHandler cancels the caller's own leased run:
// 202 for a running one (the first request records t_req), 204 for a final
// one (nothing to cancel), 404 for anything that is not the caller's open
// submission, 409 submission_in_progress for a running submission that has
// no lease (admitted before the lease was on; nothing here can stop it).
func (a *App) ContextFabricSubmissionCancelHandler() http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, key, runs, ok := a.runRouteRecord(w, r, ContextFabricRunCancelLogMessage)
		if !ok {
			return
		}
		record, err := runs.RequestRunCancel(r.Context(), key, a.now())
		switch {
		case errors.Is(err, contextfabric.ErrSubmissionNotFound) || err == nil && record.ThreadClosed != "":
			a.logRunRoute(r, ContextFabricRunCancelLogMessage, principal, "not_found", "", nil)
			writeError(w, r, http.StatusNotFound, "not_found", "Context Fabric investigation submission was not found", false, nil)
			return
		case err != nil:
			a.logRunRoute(r, ContextFabricRunCancelLogMessage, principal, "store_unavailable", "", err)
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation submissions are temporarily unavailable", true, nil)
			return
		}
		switch {
		case record.State != contextfabric.SubmissionRunning:
			a.logRunRoute(r, ContextFabricRunCancelLogMessage, principal, "already_final", "", nil, "state", string(record.State))
			w.WriteHeader(http.StatusNoContent)
		case !record.Leased():
			a.logRunRoute(r, ContextFabricRunCancelLogMessage, principal, "not_leased", "", nil, "state", string(record.State))
			w.Header().Set("Retry-After", strconv.Itoa(contractsv1.ContextFabricSubmissionInProgressRetryAfterSeconds))
			writeError(w, r, http.StatusConflict, contractsv1.ErrorCodeSubmissionInProgress, "The submission is running and cannot be canceled", true, map[string]any{"reason": "not_leased"})
		default:
			// The holder's own replica cancels at once; another replica's
			// holder sees t_req at its next poll.
			local := a.runs.cancel(contextfabric.RunKeyOf(record), record.CancelRequestedAt)
			a.logRunRoute(r, ContextFabricRunCancelLogMessage, principal, "requested", "", nil,
				"reserved_result_id", contextfabric.SanitizeLogAttr(record.ReservedResultID),
				"t_req", record.CancelRequestedAt.UTC().Format(time.RFC3339Nano),
				"holder_local", local, "holder", contextfabric.SanitizeLogAttr(record.LeaseHolder))
			a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_submission_cancel", "context_fabric_investigation_submission", record.ReservedResultID, "success", map[string]any{"state": string(record.State)})
			w.WriteHeader(http.StatusAccepted)
		}
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

// attachCursor reads Last-Event-ID: absent is 0; one well-formed value is its
// seq; anything else (repeated, malformed, beyond the largest exact integer)
// is not ok.
func attachCursor(r *http.Request) (int64, bool) {
	values := r.Header.Values(contractsv1.ContextFabricLastEventIDHeader)
	switch len(values) {
	case 0:
		return 0, true
	case 1:
		return contractsv1.ParseContextFabricLastEventID(values[0])
	}
	return 0, false
}

// attachCursorRefusal decides whether page can serve the stream after
// cursor. "" serves it; otherwise the closed reason of the 410.
func attachCursorRefusal(cursor int64, page contextfabric.RunEventPage) string {
	switch {
	case len(page.Events) > 0:
		if page.Events[0].Seq != cursor+1 {
			// The log no longer holds the event after the cursor: the
			// window moved past it (or past `accepted`, for no cursor).
			return "expired"
		}
		return ""
	case page.LastSeq == 0:
		if cursor == 0 && page.Record.State == contextfabric.SubmissionRunning {
			return ""
		}
		if cursor == 0 {
			return "not_logged"
		}
		return "ahead"
	case cursor > page.LastSeq:
		return "ahead"
	case page.TerminalSeq != 0 && cursor >= page.TerminalSeq:
		return "ended"
	}
	return ""
}

// ContextFabricSubmissionEventsHandler attaches to the caller's own leased
// run: the run's events after Last-Event-ID, from the log, then the new ones
// as the run writes them, then its terminal.
func (a *App) ContextFabricSubmissionEventsHandler() http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, key, runs, ok := a.runRouteRecord(w, r, ContextFabricRunAttachLogMessage)
		if !ok {
			return
		}
		record, err := a.investigationSubmissions().GetSubmission(r.Context(), key, a.now())
		switch {
		case errors.Is(err, contextfabric.ErrSubmissionNotFound) || err == nil && record.ThreadClosed != "":
			a.logRunRoute(r, ContextFabricRunAttachLogMessage, principal, "not_found", "", nil)
			writeError(w, r, http.StatusNotFound, "not_found", "Context Fabric investigation submission was not found", false, nil)
			return
		case err != nil:
			a.logRunRoute(r, ContextFabricRunAttachLogMessage, principal, "store_unavailable", "", err)
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation submissions are temporarily unavailable", true, nil)
			return
		}
		if !wantsInvestigationEventStream(r) {
			a.logRunRoute(r, ContextFabricRunAttachLogMessage, principal, "refused", "not_event_stream", nil)
			writeError(w, r, http.StatusBadRequest, "invalid_request", "An attach serves text/event-stream only", false, nil)
			return
		}
		cursorExpired := func(reason string, cursor int64) {
			a.logRunRoute(r, ContextFabricRunAttachLogMessage, principal, "cursor_expired", reason, nil, "cursor", cursor, "state", string(record.State))
			writeError(w, r, contractsv1.ContextFabricCursorExpiredStatus, contractsv1.ErrorCodeCursorExpired, contractsv1.ContextFabricCursorExpiredMessage, false, map[string]any{"reason": reason})
		}
		cursor, valid := attachCursor(r)
		if !valid {
			cursorExpired("malformed", 0)
			return
		}
		if !record.Leased() {
			cursorExpired("not_logged", cursor)
			return
		}
		run := contextfabric.RunKeyOf(record)
		page, err := runs.ReadRunEvents(r.Context(), run, cursor, attachReadBatch)
		if err != nil {
			a.logRunRoute(r, ContextFabricRunAttachLogMessage, principal, "store_unavailable", "", err)
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation submissions are temporarily unavailable", true, nil)
			return
		}
		if reason := attachCursorRefusal(cursor, page); reason != "" {
			cursorExpired(reason, cursor)
			return
		}
		if !a.investigationEventStreamWritable(w, r) {
			return
		}
		a.logRunRoute(r, ContextFabricRunAttachLogMessage, principal, "attached", "", nil,
			"cursor", cursor, "state", string(record.State), "reserved_result_id", contextfabric.SanitizeLogAttr(record.ReservedResultID),
			"run_request_id", contextfabric.SanitizeLogAttr(record.RunRequestID))
		a.serveRunAttach(w, r, principal, runs, record, run, cursor, page)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

// attachWriter writes an attach's frames, each under its write deadline.
type attachWriter struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
	frames     int64
	bytes      int64
}

func (s *attachWriter) write(frame []byte) error {
	if err := s.controller.SetWriteDeadline(time.Now().Add(s.timeout)); err != nil {
		return err
	}
	if _, err := s.w.Write(frame); err != nil {
		return err
	}
	if err := s.controller.Flush(); err != nil {
		return err
	}
	s.bytes += int64(len(frame))
	return nil
}

var attachKeepalive = []byte(": keepalive\n\n")

// serveRunAttach is the attach stream after its 200.
func (a *App) serveRunAttach(w http.ResponseWriter, r *http.Request, principal storage.Principal, runs contextfabric.RunStore, record contextfabric.SubmissionRecord, run contextfabric.RunKey, cursor int64, page contextfabric.RunEventPage) {
	header := w.Header()
	header.Set("Content-Type", contractsv1.ContextFabricInvestigationEventStreamMediaType)
	header.Set("Cache-Control", "no-store, no-transform")
	header.Set("X-Accel-Buffering", "no")
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	writer := &attachWriter{w: w, controller: http.NewResponseController(w), timeout: a.eventStream.writeTimeout}
	ticker := time.NewTicker(a.runLease.attachPoll)
	defer ticker.Stop()
	last, lastWrite, unmarkedPolls := cursor, time.Now(), 0
	end := func(outcome, reason string, err error) {
		a.logRunRoute(r, ContextFabricRunAttachLogMessage, principal, outcome, reason, err,
			"cursor", cursor, "last_seq", last, "frames", writer.frames, "bytes_written", writer.bytes)
	}
	for {
		for _, event := range page.Events {
			if event.Seq != last+1 {
				// The window moved past this reader: end without a terminal;
				// a reattach from `last` is answered 410.
				end("interrupted", "window_passed", nil)
				return
			}
			if event.Terminal != nil {
				a.writeAttachTerminal(writer, r, principal, record, event.Seq, event.LastStageStarted, *event.Terminal, end)
				return
			}
			frame, err := attachEventFrame(event, record.RunRequestID)
			if err != nil {
				a.logger.ErrorContext(r.Context(), "context fabric investigation run event could not be replayed",
					"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "seq", event.Seq, "failure_class", "event_log_row")
				end("interrupted", "invalid_row", nil)
				return
			}
			if err := writer.write(frame); err != nil {
				end("detached", classifyWriteError(err), nil)
				return
			}
			writer.frames++
			last, lastWrite = event.Seq, time.Now()
		}
		if len(page.Events) < attachReadBatch {
			now := a.now()
			switch current := page.Record; {
			case current.State != contextfabric.SubmissionRunning && page.TerminalSeq == 0:
				// The record is final and the log has no terminal yet: the
				// holder writes it right after the record, so wait a few reads,
				// then take the terminal from the record.
				unmarkedPolls++
				if unmarkedPolls > attachUnmarkedGracePolls {
					a.writeAttachTerminal(writer, r, principal, current, last+1, page.LastStageStarted, derivedRunTerminal(current), end)
					return
				}
			case current.State == contextfabric.SubmissionRunning && current.Leased() && !current.LeaseExpiresAt.After(now):
				// The holder stopped renewing: take the run over (it ends as
				// run_lost; the next read carries the terminal).
				_, _ = a.takeOverLostRuns(context.WithoutCancel(r.Context()), runs, &run)
			}
			if time.Since(lastWrite) >= a.runLease.keepalive {
				if err := writer.write(attachKeepalive); err != nil {
					end("detached", classifyWriteError(err), nil)
					return
				}
				lastWrite = time.Now()
			}
			select {
			case <-r.Context().Done():
				end("ended", "request_done", nil)
				return
			case <-ticker.C:
			}
		}
		next, err := runs.ReadRunEvents(r.Context(), run, last, attachReadBatch)
		if err != nil {
			if r.Context().Err() != nil {
				end("ended", "request_done", nil)
				return
			}
			a.logRunRoute(r, ContextFabricRunAttachLogMessage, principal, "read_failed", "", err, "last_seq", last)
			page.Events = nil
			continue
		}
		page = next
	}
}

// derivedRunTerminal is the terminal of a final record whose log holds none.
func derivedRunTerminal(record contextfabric.SubmissionRecord) contextfabric.RunTerminal {
	switch record.State {
	case contextfabric.SubmissionDone:
		return contextfabric.RunTerminal{Kind: contextfabric.RunTerminalResult, ResultID: record.ResultID}
	case contextfabric.SubmissionDoneUnsaved:
		return contextfabric.RunTerminal{Kind: contextfabric.RunTerminalUnsaved}
	}
	envelope := runLostEnvelope(record.RunRequestID)
	return contextfabric.RunTerminal{Kind: contextfabric.RunTerminalError, Error: &envelope}
}

// attachEventFrame is the SSE frame of a logged non-terminal event: its data
// verbatim, after the row is checked to be that event.
func attachEventFrame(event contextfabric.RunEvent, requestID string) ([]byte, error) {
	var decoded contractsv1.ContextFabricInvestigationEvent
	decoder := json.NewDecoder(bytes.NewReader(event.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if err := decoded.Validate(); err != nil {
		return nil, err
	}
	if decoded.Seq != event.Seq || decoded.Type != event.Type || decoded.Type.Terminal() || decoded.RequestID != requestID {
		return nil, errors.New("context fabric run event row does not match its event")
	}
	frame := make([]byte, 0, len(event.Data)+64)
	frame = append(frame, "id: "...)
	frame = strconv.AppendInt(frame, event.Seq, 10)
	frame = append(frame, "\nevent: "...)
	frame = append(frame, string(event.Type)...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, event.Data...)
	frame = append(frame, "\n\n"...)
	return frame, nil
}

// writeAttachTerminal writes the attach's terminal from the run's marker:
// a result is read by id now, through the gate on the attach's surface; an
// unsaved answer is error{submission_unsaved}; an error is the run's own
// envelope.
func (a *App) writeAttachTerminal(writer *attachWriter, r *http.Request, principal storage.Principal, record contextfabric.SubmissionRecord, seq int64, lastStage contractsv1.ContextFabricInvestigationStage, terminal contextfabric.RunTerminal, end func(outcome, reason string, err error)) {
	requestID := record.RunRequestID
	errorEvent := func(envelope contractsv1.ErrorEnvelope) contractsv1.ContextFabricInvestigationEvent {
		envelope.RequestID = requestID
		return contractsv1.ContextFabricInvestigationEvent{Type: contractsv1.ContextFabricInvestigationEventError, ReservedResultID: record.ReservedResultID, Stage: lastStage, Error: &envelope}
	}
	var event contractsv1.ContextFabricInvestigationEvent
	switch terminal.Kind {
	case contextfabric.RunTerminalResult:
		results := a.investigationResults()
		capture := newInvestigationOutcomeCapture(writer.w)
		if results == nil {
			writeError(capture, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric investigation results are not available", true, nil)
		} else {
			a.serveStoredInvestigationResultOn(capture, r, principal, results, a.storedResultGate(), terminal.ResultID, "context_fabric_investigation_attach_result", contextfabric.StoredResultSurfaceSubmissionAttach)
		}
		if capture.status == http.StatusOK {
			event = contractsv1.ContextFabricInvestigationEvent{Type: contractsv1.ContextFabricInvestigationEventResult, Result: bytes.TrimSpace(capture.body.Bytes())}
			break
		}
		var envelope contractsv1.ErrorEnvelope
		if err := json.Unmarshal(capture.body.Bytes(), &envelope); err != nil {
			envelope = contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, Error: contractsv1.ErrorDetail{Code: "internal_error", Message: "The investigation result could not be served", HTTPStatus: http.StatusInternalServerError}}
		}
		event = errorEvent(envelope)
	case contextfabric.RunTerminalUnsaved:
		event = errorEvent(contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, Error: contractsv1.ErrorDetail{
			Code: contractsv1.ErrorCodeSubmissionUnsaved, Message: "The submission was answered but its answer was not saved", HTTPStatus: http.StatusConflict,
		}})
	default:
		envelope := contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, Error: contractsv1.ErrorDetail{Code: "internal_error", Message: "The investigation ended with an error", HTTPStatus: http.StatusInternalServerError}}
		if terminal.Error != nil {
			envelope = *terminal.Error
		}
		event = errorEvent(envelope)
	}
	event.SchemaVersion = contractsv1.ContextFabricInvestigationEventSchema
	event.RequestID = requestID
	event.Seq = seq
	frame, err := contractsv1.MarshalContextFabricInvestigationEventFrame(event)
	if err == nil && int64(len(frame)) > a.eventStream.frameMaxBytes {
		err = errInvestigationStreamFrameOverCap
	}
	if err != nil {
		a.logger.ErrorContext(r.Context(), "context fabric investigation run terminal could not be attached",
			"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "seq", seq, "terminal_kind", string(terminal.Kind), "failure_class", "attach_terminal")
		fallback := errorEvent(contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, Error: contractsv1.ErrorDetail{
			Code: "budget_refusal", Message: "The Context Fabric investigation event stream exceeded its byte budget", HTTPStatus: http.StatusRequestEntityTooLarge,
			Details: map[string]any{"reason": "stream_frame_cap"},
		}})
		fallback.SchemaVersion, fallback.RequestID, fallback.Seq = contractsv1.ContextFabricInvestigationEventSchema, requestID, seq
		if frame, err = contractsv1.MarshalContextFabricInvestigationEventFrame(fallback); err != nil {
			end("interrupted", "terminal_invalid", nil)
			return
		}
		event = fallback
	}
	if err := writer.write(frame); err != nil {
		end("detached", classifyWriteError(err), nil)
		return
	}
	writer.frames++
	end("terminal", string(event.Type), nil)
}
