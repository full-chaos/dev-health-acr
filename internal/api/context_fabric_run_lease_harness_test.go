package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The CHAOS-11949 harness: the CHAOS-11912 submission harness with the run
// lease on in every App (one "replica" each, its own holder id), short
// timings, and a live SSE client that reads frame by frame.

// lockedBuffer is a log sink the App's goroutines and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Len is the bytes written so far, for a test that reads only what a later call
// logs.
func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// Reset drops what was logged so far.
func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// lines are the JSON log lines with msg message.
func (b *lockedBuffer) lines(message string) []map[string]any {
	var lines []map[string]any
	for _, raw := range strings.Split(b.String(), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == message {
			lines = append(lines, line)
		}
	}
	return lines
}

// waitLine waits for the first line with msg message that match accepts.
func (b *lockedBuffer) waitLine(t *testing.T, message string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	var found map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, line := range b.lines(message) {
			if match == nil || match(line) {
				found = line
				return found
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q line within 10 s:\n%s", message, b.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// enableRunLease turns the run lease on in every App of harness, over the
// run store of each App's own submission store, and gives each App a log
// the test can read while it runs.
func enableRunLease(t *testing.T, harness *submissionHarness) []*lockedBuffer {
	t.Helper()
	var logs []*lockedBuffer
	for index, app := range harness.apps {
		buffer := &lockedBuffer{}
		app.logger = slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
		logs = append(logs, buffer)
		runs, ok := app.runtime.InvestigationSubmissions.(contextfabric.RunStore)
		if !ok {
			t.Fatalf("app %d's submission store is not a run store", index)
		}
		app.config.ContextFabricEventStreamEnabled = true
		app.config.ContextFabricRunLeaseEnabled = true
		app.runtime.InvestigationRuns = runs
		app.runLease.holder = fmt.Sprintf("replica-%c-0000", 'a'+index)
		app.runLease.cancelPoll = 20 * time.Millisecond
		app.runLease.attachPoll = 10 * time.Millisecond
		app.runLease.checkTimeout = time.Second
		app.eventStream.heartbeat = time.Hour
	}
	return logs
}

// serveApp starts a real server over app index: a stream needs a writer
// that takes a write deadline.
func (h *submissionHarness) serveApp(index int) *httptest.Server {
	h.t.Helper()
	server := httptest.NewServer(h.apps[index].Handler())
	h.t.Cleanup(server.Close)
	return server
}

// liveStream is a client reading one SSE response frame by frame.
type liveStream struct {
	t        *testing.T
	response *http.Response
	cancel   context.CancelFunc
	frames   chan liveFrame
	mu       sync.Mutex
	raw      bytes.Buffer
	comments int
}

// liveFrame is one SSE frame: an event (data verbatim) or the end.
type liveFrame struct {
	event contractsv1.ContextFabricInvestigationEvent
	data  []byte
	err   error
}

// openLiveStream sends request (Accept: text/event-stream set here) and
// starts reading when the answer is a stream; otherwise the response is
// returned with no reader.
func openLiveStream(t *testing.T, request *http.Request) (*liveStream, *http.Response) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request = request.WithContext(ctx)
	request.Header.Set("Accept", contractsv1.ContextFabricInvestigationEventStreamMediaType)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != contractsv1.ContextFabricInvestigationEventStreamMediaType {
		cancel()
		return nil, response
	}
	stream := &liveStream{t: t, response: response, cancel: cancel, frames: make(chan liveFrame, 256)}
	t.Cleanup(stream.close)
	go stream.read()
	return stream, response
}

func (s *liveStream) read() {
	defer close(s.frames)
	reader := bufio.NewReader(s.response.Body)
	var frame []byte
	for {
		line, err := reader.ReadBytes('\n')
		s.mu.Lock()
		s.raw.Write(line)
		s.mu.Unlock()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.frames <- liveFrame{err: err}
			}
			return
		}
		if !bytes.Equal(line, []byte("\n")) {
			frame = append(frame, line...)
			continue
		}
		if bytes.HasPrefix(frame, []byte(":")) {
			s.mu.Lock()
			s.comments++
			s.mu.Unlock()
			frame = nil
			continue
		}
		events, decodeErr := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(append(frame, '\n')), 8<<20)
		if decodeErr != nil || len(events) != 1 {
			s.frames <- liveFrame{err: fmt.Errorf("frame %q: %v", frame, decodeErr)}
			return
		}
		lines := strings.SplitN(string(frame), "\n", 4)
		data := []byte(strings.TrimPrefix(lines[2], "data: "))
		s.frames <- liveFrame{event: events[0], data: data}
		frame = nil
	}
}

// next is the next event, or a failure after timeout. ok is false at the
// end of the stream.
func (s *liveStream) next(timeout time.Duration) (liveFrame, bool) {
	s.t.Helper()
	select {
	case frame, open := <-s.frames:
		if !open {
			return liveFrame{}, false
		}
		if frame.err != nil {
			s.t.Fatalf("stream read: %v", frame.err)
		}
		return frame, true
	case <-time.After(timeout):
		s.t.Fatalf("no frame within %s; read so far:\n%s", timeout, s.rawString())
		return liveFrame{}, false
	}
}

// rest reads every event until the end of the stream.
func (s *liveStream) rest(timeout time.Duration) []liveFrame {
	s.t.Helper()
	var frames []liveFrame
	for {
		frame, ok := s.next(timeout)
		if !ok {
			return frames
		}
		frames = append(frames, frame)
	}
}

func (s *liveStream) rawString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raw.String()
}

// close disconnects the client.
func (s *liveStream) close() {
	s.cancel()
	_ = s.response.Body.Close()
}

// streamRequest is a create-investigation request for the stream.
func (h *submissionHarness) streamRequest(server *httptest.Server, token, submissionID string) *http.Request {
	h.t.Helper()
	body := investigationRequestBody()
	body.Question = submissionQuestion
	encoded, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, submissionID)
	return request
}

// attachRequest is an attach to submissionID, with Last-Event-ID values.
func (h *submissionHarness) attachRequest(server *httptest.Server, token, submissionID string, lastEventIDs ...string) *http.Request {
	h.t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+strings.Replace(ContextFabricSubmissionEventsPath, "{submission_id}", submissionID, 1), nil)
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("X-Request-ID", "req_00000000000000000000000000011949")
	for _, value := range lastEventIDs {
		request.Header.Add(contractsv1.ContextFabricLastEventIDHeader, value)
	}
	return request
}

// cancelRun posts the cancel of submissionID on app index, as token.
func (h *submissionHarness) cancelRun(app int, token, submissionID string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(http.MethodPost, strings.Replace(ContextFabricSubmissionCancelPath, "{submission_id}", submissionID, 1), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("X-Request-ID", "req_00000000000000000000000000011950")
	recorder := httptest.NewRecorder()
	h.apps[app].Handler().ServeHTTP(recorder, request)
	return recorder
}

// attachRefusal is the non-stream answer to an attach: its status and code.
func attachRefusal(t *testing.T, response *http.Response) (int, string, []byte) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope ErrorEnvelopeForTest
	_ = json.Unmarshal(body, &envelope)
	return response.StatusCode, envelope.Error.Code, body
}

// attemptInvestigator stands in for the model runtime's attempt loop
// (genkitruntime withRetry): before each attempt it checks the run context
// and records the attempt's start; an attempt fails retryably and the loop
// waits out a backoff that ends at a cancel. It never calls a model once the
// run is cancelled -- the property the genkitruntime proof
// (TestNoModelAttemptStartsAfterTheRunIsCancelledInTheBackoff) holds for the
// real loop; here it carries the cancel from another replica to the run.
// When release is closed, the next attempt succeeds and the run saves like
// the counting investigator.
type attemptInvestigator struct {
	inner    *countingSubmissionInvestigator
	attempt  time.Duration
	backoff  time.Duration
	release  chan struct{}
	entered  chan struct{}
	mu       sync.Mutex
	attempts []time.Time
}

func newAttemptInvestigator(inner *countingSubmissionInvestigator) *attemptInvestigator {
	return &attemptInvestigator{inner: inner, attempt: 30 * time.Millisecond, backoff: 120 * time.Millisecond, release: make(chan struct{}), entered: make(chan struct{}, 16)}
}

func (f *attemptInvestigator) Investigate(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	f.entered <- struct{}{}
	for {
		if err := ctx.Err(); err != nil {
			return contextfabric.InvestigationResult{}, err
		}
		f.mu.Lock()
		f.attempts = append(f.attempts, time.Now())
		f.mu.Unlock()
		select {
		case <-f.release:
			f.inner.mode.Store(int32(runSave))
			return f.inner.Investigate(ctx, principal, request)
		default:
		}
		// The attempt, then a retryable failure and its backoff; both end
		// at a cancel.
		for _, wait := range []time.Duration{f.attempt, f.backoff} {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return contextfabric.InvestigationResult{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
}

func (f *attemptInvestigator) startedAttempts() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.attempts...)
}

// useInvestigator makes every App of harness run investigator.
func (h *submissionHarness) useInvestigator(investigator contextfabric.Investigator) {
	for _, app := range h.apps {
		app.runtime.Investigator = investigator
	}
}
