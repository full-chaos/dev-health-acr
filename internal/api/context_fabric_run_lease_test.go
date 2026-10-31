package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-11949 (plan §2.7.9): a leased run on the memory store. Two Apps share
// one store: two replicas of one deployment. The Postgres twins of the
// lifecycle cases are in context_fabric_run_lease_pg_test.go.

func newLeaseHarness(t *testing.T, apps int) (*submissionHarness, *memoryinvestigation.Store, []*lockedBuffer) {
	t.Helper()
	harness, store := newMemorySubmissionHarness(t, apps)
	logs := enableRunLease(t, harness)
	return harness, store, logs
}

// recordOf reads the stored record of submissionID for tokenA's principal.
func recordOf(t *testing.T, harness *submissionHarness, app int, submissionID string) contextfabric.SubmissionRecord {
	t.Helper()
	recorder := harness.status(app, harness.tokenA, submissionID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status of %s = %d %s", submissionID, recorder.Code, recorder.Body.String())
	}
	var status contractsv1.ContextFabricSubmissionStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return contextfabric.SubmissionRecord{State: contextfabric.SubmissionState(status.State), ResultID: status.ResultID, ReservedResultID: status.ReservedResultID}
}

// joinedStream checks prefix (the events a first connection saw) followed by
// resumed (an attach's events) as one stream of the contract grammar.
func joinedStream(t *testing.T, prefix, resumed []liveFrame) []contractsv1.ContextFabricInvestigationEvent {
	t.Helper()
	var events []contractsv1.ContextFabricInvestigationEvent
	for _, frame := range append(append([]liveFrame(nil), prefix...), resumed...) {
		events = append(events, frame.event)
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(events); err != nil {
		t.Fatalf("the first connection's events and the attach's are not one stream: %v", err)
	}
	return events
}

// A leased run outlives its connection: the client disconnects after
// `accepted`, the run goes on (no cancel), and an attach on the OTHER replica
// with Last-Event-ID resumes after it, through the run's heartbeats, to the
// terminal; the result terminal is the stored result as the read by id
// serves it. One execution.
func TestALeasedRunOutlivesItsConnectionAndAttachesOnTheOtherReplica(t *testing.T) {
	harness, _, logs := newLeaseHarness(t, 2)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	harness.apps[0].eventStream.heartbeat = 40 * time.Millisecond
	first, second := harness.serveApp(0), harness.serveApp(1)

	stream, response := openLiveStream(t, harness.streamRequest(first, harness.tokenA, "submission-lease-0001"))
	if stream == nil {
		t.Fatalf("no stream: %d", response.StatusCode)
	}
	accepted, _ := stream.next(5 * time.Second)
	if accepted.event.Type != contractsv1.ContextFabricInvestigationEventAccepted {
		t.Fatalf("first event %+v", accepted.event)
	}
	waitEntered(t, harness.investigator.entered)
	stream.close()
	// The viewer is gone; the run is not: the writer says so, and no cancel
	// effective line is written while it runs on.
	logs[0].waitLine(t, "context fabric investigation event stream viewer detached", nil)
	if cancels := logs[0].lines("context fabric investigation cancel effective"); len(cancels) != 0 {
		t.Fatalf("the disconnect cancelled the leased run: %v", cancels)
	}

	attach, response := openLiveStream(t, harness.attachRequest(second, harness.tokenA, "submission-lease-0001", "1"))
	if attach == nil {
		status, code, body := attachRefusal(t, response)
		t.Fatalf("attach = %d %s %s", status, code, body)
	}
	beat, _ := attach.next(5 * time.Second)
	if beat.event.Type != contractsv1.ContextFabricInvestigationEventHeartbeat || beat.event.Seq != 2 || beat.event.RequestID != submissionTestRequestID {
		t.Fatalf("the attach resumed at %+v, want the run's heartbeat seq 2 with the run's request id", beat.event)
	}
	close(harness.investigator.release)
	resumed := append([]liveFrame{beat}, attach.rest(5*time.Second)...)
	events := joinedStream(t, []liveFrame{accepted}, resumed)
	terminal := events[len(events)-1]
	if terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("terminal %+v", terminal)
	}
	byID := harness.get(1, harness.tokenA, accepted.event.ReservedResultID)
	if byID.Code != http.StatusOK || !bytes.Equal(terminal.Result, bytes.TrimSpace(byID.Body.Bytes())) {
		t.Fatalf("the attach's result is not the read by id byte for byte\nattach: %s\nby id:  %s", terminal.Result, byID.Body.String())
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
	if record := recordOf(t, harness, 1, "submission-lease-0001"); record.State != contextfabric.SubmissionDone {
		t.Fatalf("record %+v, want done", record)
	}
}

// With the run lease off (the default) nothing changes: a disconnect
// cancels the run, the record carries no lease, and both routes answer 404.
func TestWithTheRunLeaseOffTheConnectionOwnsTheRun(t *testing.T) {
	harness, store, logs := newLeaseHarness(t, 1)
	harness.apps[0].config.ContextFabricRunLeaseEnabled = false
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	stream, _ := openLiveStream(t, harness.streamRequest(server, harness.tokenA, "submission-lease-off01"))
	if stream == nil {
		t.Fatal("no stream")
	}
	stream.next(5 * time.Second)
	waitEntered(t, harness.investigator.entered)
	stream.close()
	line := logs[0].waitLine(t, "context fabric investigation cancel effective", nil)
	if line["cause"] != "client_disconnected" {
		t.Fatalf("cancel line %v, want client_disconnected", line)
	}
	key, err := contextfabric.SubmissionKeyFor(harness.investigator.lastPrincipal.Load().(storage.Principal), "submission-lease-off01")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.GetSubmission(context.Background(), key, harness.clock.Now())
	if err != nil || record.Leased() {
		t.Fatalf("record %+v (%v), want one with no lease", record, err)
	}
	if recorder := harness.cancelRun(0, harness.tokenA, "submission-lease-off01"); recorder.Code != http.StatusNotFound {
		t.Fatalf("cancel with the lease off = %d", recorder.Code)
	}
	if _, response := openLiveStream(t, harness.attachRequest(server, harness.tokenA, "submission-lease-off01")); response.StatusCode != http.StatusNotFound {
		t.Fatalf("attach with the lease off = %d", response.StatusCode)
	}
}

// An explicit cancel on the OTHER replica stops the run: the holder sees
// t_req at its next poll, cancels the run's context and logs t_eff with
// t_req; no attempt starts after t_eff, even when the cancel lands in a
// retry backoff; t_eff - t_req is inside the stated bound; the run ends
// error{canceled} on the live stream and in the log; the record is
// abandoned; a repeat of the cancel is idempotent.
func TestACancelOnTheOtherReplicaStopsTheRun(t *testing.T) {
	harness, _, logs := newLeaseHarness(t, 2)
	investigator := newAttemptInvestigator(harness.investigator)
	harness.useInvestigator(investigator)
	first, second := harness.serveApp(0), harness.serveApp(1)
	stream, _ := openLiveStream(t, harness.streamRequest(first, harness.tokenA, "submission-cancel-01"))
	if stream == nil {
		t.Fatal("no stream")
	}
	accepted, _ := stream.next(5 * time.Second)
	waitEntered(t, investigator.entered)
	// Land the cancel inside a backoff: wait for the second attempt, then
	// for the attempt to fail.
	waitFor(t, func() bool { return len(investigator.startedAttempts()) >= 2 })
	time.Sleep(investigator.attempt + 10*time.Millisecond)

	recorder := harness.cancelRun(1, harness.tokenA, "submission-cancel-01")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("cancel = %d %s", recorder.Code, recorder.Body.String())
	}
	request := logs[1].waitLine(t, ContextFabricRunCancelLogMessage, func(line map[string]any) bool { return line["outcome"] == "requested" })
	if request["holder_local"] != false {
		t.Fatalf("the cancel on the other replica found the run local: %v", request)
	}
	// A second cancel while the first takes effect keeps the first t_req.
	if again := harness.cancelRun(1, harness.tokenA, "submission-cancel-01"); again.Code != http.StatusAccepted && again.Code != http.StatusNoContent {
		t.Fatalf("repeat cancel = %d", again.Code)
	}
	frames := stream.rest(5 * time.Second)
	events := joinedStream(t, []liveFrame{accepted}, frames)
	terminal := events[len(events)-1]
	if terminal.Type != contractsv1.ContextFabricInvestigationEventError || terminal.Error.Error.Code != contractsv1.ErrorCodeCanceled {
		t.Fatalf("terminal %+v, want error canceled", terminal)
	}

	effective := logs[0].waitLine(t, "context fabric investigation cancel effective", nil)
	if effective["cause"] != "cancel_requested" {
		t.Fatalf("cancel effective %v, want cause cancel_requested", effective)
	}
	tEff, err := time.Parse(time.RFC3339Nano, effective["t_eff"].(string))
	if err != nil {
		t.Fatal(err)
	}
	tReqText, _ := effective["t_req"].(string)
	if tReqText != request["t_req"] {
		t.Fatalf("the holder's t_req %q is not the cancel's %v", tReqText, request["t_req"])
	}
	// t_req is the record's (the harness clock here); the wall-clock bound is
	// TestTheCancelBoundIsMeasured.
	if bound := effective["t_eff_minus_t_req_ms"]; bound == nil {
		t.Fatalf("the cancel effective line carries no t_eff - t_req: %v", effective)
	}
	for _, started := range investigator.startedAttempts() {
		if started.After(tEff) {
			t.Fatalf("an attempt started at %s, after t_eff %s", started, tEff)
		}
	}
	if record := recordOf(t, harness, 1, "submission-cancel-01"); record.State != contextfabric.SubmissionAbandoned {
		t.Fatalf("record %+v, want abandoned", record)
	}
	if final := harness.cancelRun(1, harness.tokenA, "submission-cancel-01"); final.Code != http.StatusNoContent {
		t.Fatalf("cancel of a final run = %d, want 204", final.Code)
	}
	// The log holds the canceled terminal: an attach from the last event
	// gets it.
	attach, response := openLiveStream(t, harness.attachRequest(second, harness.tokenA, "submission-cancel-01", strconv.FormatInt(terminal.Seq-1, 10)))
	if attach == nil {
		status, code, body := attachRefusal(t, response)
		t.Fatalf("attach = %d %s %s", status, code, body)
	}
	replayed := attach.rest(5 * time.Second)
	if len(replayed) != 1 || replayed[0].event.Type != contractsv1.ContextFabricInvestigationEventError || replayed[0].event.Error.Error.Code != contractsv1.ErrorCodeCanceled {
		t.Fatalf("attach replayed %+v, want the canceled terminal", replayed)
	}
}

// The measured bound: a cancel on the holder's own replica is effective at
// once; on another replica within the cancel poll. Both stay inside
// ContextFabricRunCancelEffectiveSeconds, measured on the wall clock from the
// cancel request to the cancel effective line.
func TestTheCancelBoundIsMeasured(t *testing.T) {
	for _, tc := range []struct {
		name     string
		canceler int
	}{{"same_replica", 0}, {"other_replica", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			harness, _, logs := newLeaseHarness(t, 2)
			for _, app := range harness.apps {
				app.runLease.cancelPoll = contextfabric.RunCancelPollInterval
			}
			investigator := newAttemptInvestigator(harness.investigator)
			harness.useInvestigator(investigator)
			stream, _ := openLiveStream(t, harness.streamRequest(harness.serveApp(0), harness.tokenA, "submission-bound-"+tc.name))
			if stream == nil {
				t.Fatal("no stream")
			}
			stream.next(5 * time.Second)
			waitEntered(t, investigator.entered)
			requested := time.Now()
			if recorder := harness.cancelRun(tc.canceler, harness.tokenA, "submission-bound-"+tc.name); recorder.Code != http.StatusAccepted {
				t.Fatalf("cancel = %d", recorder.Code)
			}
			effective := logs[0].waitLine(t, "context fabric investigation cancel effective", nil)
			tEff, err := time.Parse(time.RFC3339Nano, effective["t_eff"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if bound := tEff.Sub(requested); bound > contractsv1.ContextFabricRunCancelEffectiveSeconds*time.Second {
				t.Fatalf("t_eff - t_req = %s, over the %d s bound", bound, contractsv1.ContextFabricRunCancelEffectiveSeconds)
			}
			t.Logf("%s: t_eff - t_req = %s", tc.name, tEff.Sub(requested))
			stream.rest(5 * time.Second)
		})
	}
}

// A dead holder's lease is taken over: replica a holds the run and stops
// renewing (its store calls hang, as a stalled pod's do); once the lease has
// ended, replica b takes it over -- the record becomes abandoned with b as
// lease_holder and the log gets run_lost, once, though two takers race. When
// a wakes, its next check finds the run no longer its own and it cancels the
// run (cause lease_lost); its late terminal does not replace run_lost.
func TestADeadHoldersLeaseIsTakenOverOnce(t *testing.T) {
	harness, store, logs := newLeaseHarness(t, 3)
	stalled := &stallingRunStore{RunStore: store}
	harness.apps[0].runtime.InvestigationRuns = stalled
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	// A failure below must not leave the server waiting on a stalled holder.
	t.Cleanup(stalled.resume)
	stream, _ := openLiveStream(t, harness.streamRequest(server, harness.tokenA, "submission-takeover1"))
	if stream == nil {
		t.Fatal("no stream")
	}
	accepted, _ := stream.next(5 * time.Second)
	waitEntered(t, harness.investigator.entered)
	stalled.stall()
	// The lease ends: no renewal reached the record.
	harness.clock.Advance(contextfabric.RunLeaseTTL + time.Second)
	results := make(chan int, 2)
	for _, taker := range []int{1, 2} {
		go func(taker int) {
			count, err := harness.apps[taker].TakeOverLostRuns(context.Background())
			if err != nil {
				t.Error(err)
			}
			results <- count
		}(taker)
	}
	if total := <-results + <-results; total != 1 {
		t.Fatalf("%d takeovers, want exactly 1", total)
	}
	run := contextfabric.RunKey{OrgID: "org_1", ReservedResultID: accepted.event.ReservedResultID}
	terminal, err := store.RunTerminalOf(context.Background(), run)
	if err != nil || terminal == nil || terminal.Terminal.Error == nil || terminal.Terminal.Error.Error.Code != contractsv1.ErrorCodeRunLost {
		t.Fatalf("terminal %+v (%v), want run_lost", terminal, err)
	}
	if terminal.Terminal.Error.RequestID != submissionTestRequestID {
		t.Fatalf("the run_lost envelope names request %q, want the run's", terminal.Terminal.Error.RequestID)
	}
	if record := recordOf(t, harness, 1, "submission-takeover1"); record.State != contextfabric.SubmissionAbandoned {
		t.Fatalf("record %+v, want abandoned", record)
	}
	// An attach (on either taker's replica) ends with run_lost.
	attach, response := openLiveStream(t, harness.attachRequest(harness.serveApp(2), harness.tokenA, "submission-takeover1", "1"))
	if attach == nil {
		status, code, body := attachRefusal(t, response)
		t.Fatalf("attach = %d %s %s", status, code, body)
	}
	if frames := attach.rest(5 * time.Second); len(frames) != 1 || frames[0].event.Error == nil || frames[0].event.Error.Error.Code != contractsv1.ErrorCodeRunLost {
		t.Fatalf("attach %+v, want the run_lost terminal", frames)
	}
	// The stalled holder wakes: its check finds the run taken over, it
	// cancels its run, and the run's own end does not replace run_lost.
	stalled.resume()
	lost := logs[0].waitLine(t, ContextFabricRunLeaseLogMessage, func(line map[string]any) bool { return line["outcome"] == "lease_lost" })
	if lost["reason"] != "taken_over" {
		t.Fatalf("lease line %v, want taken_over", lost)
	}
	effective := logs[0].waitLine(t, "context fabric investigation cancel effective", nil)
	if effective["cause"] != "lease_lost" {
		t.Fatalf("cancel effective %v, want lease_lost", effective)
	}
	frames := stream.rest(5 * time.Second)
	if last := frames[len(frames)-1].event; last.Error == nil || last.Error.Error.Code != contractsv1.ErrorCodeRunLost {
		t.Fatalf("the old holder's own stream ended %+v, want run_lost", last)
	}
	again, err := store.RunTerminalOf(context.Background(), run)
	if err != nil || again.Seq != terminal.Seq || again.Terminal.Error.Error.Code != contractsv1.ErrorCodeRunLost {
		t.Fatalf("terminal after the old holder ended: %+v (%v), want the taker's", again, err)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// stallingRunStore is a run store whose lease checks hang while stalled: a
// holder that stopped (a paused or partitioned pod) without its process
// exiting.
type stallingRunStore struct {
	contextfabric.RunStore
	mu   sync.Mutex
	gate chan struct{}
}

func (s *stallingRunStore) stall() {
	s.mu.Lock()
	s.gate = make(chan struct{})
	s.mu.Unlock()
}

func (s *stallingRunStore) resume() {
	s.mu.Lock()
	if s.gate != nil {
		close(s.gate)
		s.gate = nil
	}
	s.mu.Unlock()
}

func (s *stallingRunStore) CheckRunLease(ctx context.Context, run contextfabric.RunKey, holder string, renewUntil, now time.Time) (contextfabric.RunLeaseCheck, error) {
	s.mu.Lock()
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return s.RunStore.CheckRunLease(ctx, run, holder, renewUntil, now)
}

// Every cursor the log cannot serve is 410 cursor_expired, before any
// stream: a fabricated cursor past the log, a malformed or repeated header,
// a cursor at the terminal, a run with no log; and once the run's log is
// past its cap, a cursor older than the kept window (and no cursor, whose
// `accepted` is gone), while the terminal is kept and an attach from the
// window's start still reaches it.
func TestAnAttachRefusesEveryCursorTheLogCannotServe(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	harness.apps[0].runLease.maxEvents = 4
	harness.apps[0].eventStream.heartbeat = 5 * time.Millisecond
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	stream, _ := openLiveStream(t, harness.streamRequest(server, harness.tokenA, "submission-cursor-01"))
	if stream == nil {
		t.Fatal("no stream")
	}
	var seen []liveFrame
	for len(seen) < 12 {
		frame, _ := stream.next(5 * time.Second)
		seen = append(seen, frame)
	}
	close(harness.investigator.release)
	seen = append(seen, stream.rest(5*time.Second)...)
	terminalSeq := seen[len(seen)-1].event.Seq
	refusal := func(values ...string) string {
		t.Helper()
		attach, response := openLiveStream(t, harness.attachRequest(server, harness.tokenA, "submission-cursor-01", values...))
		if attach != nil {
			t.Fatalf("Last-Event-ID %v opened a stream", values)
		}
		status, code, body := attachRefusal(t, response)
		if status != http.StatusGone || code != contractsv1.ErrorCodeCursorExpired {
			t.Fatalf("Last-Event-ID %v = %d %s %s, want 410 cursor_expired", values, status, code, body)
		}
		var envelope struct {
			Error struct {
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &envelope)
		reason, _ := envelope.Error.Details["reason"].(string)
		return reason
	}
	for _, tc := range []struct {
		values []string
		want   string
	}{
		{[]string{"99999999"}, "ahead"},
		{[]string{"abc"}, "malformed"},
		{[]string{"01"}, "malformed"},
		{[]string{"-1"}, "malformed"},
		{[]string{"1", "2"}, "malformed"},
		{[]string{strconv.FormatInt(terminalSeq, 10)}, "ended"},
		{[]string{"1"}, "expired"},
		{nil, "expired"},
	} {
		if got := refusal(tc.values...); got != tc.want {
			t.Errorf("Last-Event-ID %v refused for %q, want %q", tc.values, got, tc.want)
		}
	}
	// The window keeps the latest 4 non-terminal events and the terminal:
	// an attach from just before the window replays them and the terminal.
	attach, response := openLiveStream(t, harness.attachRequest(server, harness.tokenA, "submission-cursor-01", strconv.FormatInt(terminalSeq-5, 10)))
	if attach == nil {
		status, code, body := attachRefusal(t, response)
		t.Fatalf("attach at the window = %d %s %s", status, code, body)
	}
	frames := attach.rest(5 * time.Second)
	if len(frames) != 5 || frames[4].event.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("the window replayed %d events ending %+v, want 4 heartbeats and the result", len(frames), frames[len(frames)-1].event)
	}
	for index, frame := range frames[:4] {
		if !bytes.Equal(frame.data, seen[len(seen)-5+index].data) {
			t.Fatalf("replayed event %d is not the run's own bytes:\n%s\n%s", frame.event.Seq, frame.data, seen[len(seen)-5+index].data)
		}
	}
	// A connection-owned run (lease off at admission) has no log: 410.
	harness.apps[0].config.ContextFabricRunLeaseEnabled = false
	harness.investigator.mode.Store(int32(runSave))
	plain, _ := openLiveStream(t, harness.streamRequest(server, harness.tokenA, "submission-cursor-02"))
	plain.rest(5 * time.Second)
	harness.apps[0].config.ContextFabricRunLeaseEnabled = true
	attach, response = openLiveStream(t, harness.attachRequest(server, harness.tokenA, "submission-cursor-02"))
	if attach != nil {
		t.Fatal("an attach to a connection-owned run opened a stream")
	}
	if status, code, _ := attachRefusal(t, response); status != http.StatusGone || code != contractsv1.ErrorCodeCursorExpired {
		t.Fatalf("attach to a run with no log = %d %s", status, code)
	}
}

// Attach and cancel are the caller's own (INV-1): another principal of the
// same organization with the same grant, a malformed id and an unknown id
// answer the same 404, and the other principal's cancel changes nothing.
func TestAttachAndCancelAreTheCallersOwn(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	stream, _ := openLiveStream(t, harness.streamRequest(server, harness.tokenA, "submission-owner-001"))
	if stream == nil {
		t.Fatal("no stream")
	}
	stream.next(5 * time.Second)
	waitEntered(t, harness.investigator.entered)
	var bodies [][]byte
	for _, tc := range []struct{ token, id string }{
		{harness.tokenB, "submission-owner-001"},
		{harness.tokenA, "short"},
		{harness.tokenA, "submission-unknown-1"},
	} {
		recorder := harness.cancelRun(0, tc.token, tc.id)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("cancel %s as another = %d", tc.id, recorder.Code)
		}
		attach, response := openLiveStream(t, harness.attachRequest(server, tc.token, tc.id))
		if attach != nil {
			t.Fatalf("attach %s opened a stream", tc.id)
		}
		status, code, body := attachRefusal(t, response)
		if status != http.StatusNotFound || code != "not_found" {
			t.Fatalf("attach %s = %d %s", tc.id, status, code)
		}
		bodies = append(bodies, recorder.Body.Bytes(), body)
	}
	if len(harness.investigator.entered) != 0 || harness.investigator.executions.Load() != 1 {
		t.Fatal("a refused call ran something")
	}
	close(harness.investigator.release)
	frames := stream.rest(5 * time.Second)
	if last := frames[len(frames)-1].event; last.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("the other principal's cancel ended the run: %+v", last)
	}
	for index := 1; index < len(bodies); index++ {
		if index%2 == 1 && !bytes.Equal(bodies[index], bodies[1]) {
			t.Fatalf("attach refusals differ: the 404 is an existence oracle\n%s\n%s", bodies[index], bodies[1])
		}
	}
}

// The run lease's error bodies are the routes' own: the 410 of an attach,
// and the canceled and run_lost terminals as the run writes them.
// ACR_WRITE_RUN_LEASE_GOLDENS=1 rewrites them.
func TestRunLeaseGoldensAreTheRoutesOwnBodies(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	server := harness.serveApp(0)
	bodies := map[string][]byte{}
	harness.investigator.mode.Store(int32(runSave))
	plain, _ := openLiveStream(t, harness.streamRequest(server, harness.tokenA, "submission-golden-c1"))
	if plain == nil {
		t.Fatal("no stream")
	}
	plain.rest(5 * time.Second)
	_, response := openLiveStream(t, harness.attachRequest(server, harness.tokenA, "submission-golden-c1", "99999999"))
	_, _, body := attachRefusal(t, response)
	bodies["error_context_fabric_cursor_expired.v1.json"] = body

	investigator := newAttemptInvestigator(harness.investigator)
	harness.useInvestigator(investigator)
	// The mux takes the investigator when it is built.
	server = harness.serveApp(0)
	stream, _ := openLiveStream(t, harness.streamRequest(server, harness.tokenA, "submission-golden-c2"))
	if stream == nil {
		t.Fatal("no stream")
	}
	stream.next(5 * time.Second)
	waitEntered(t, investigator.entered)
	harness.cancelRun(0, harness.tokenA, "submission-golden-c2")
	frames := stream.rest(5 * time.Second)
	canceled, err := json.Marshal(frames[len(frames)-1].event.Error)
	if err != nil {
		t.Fatal(err)
	}
	bodies["error_context_fabric_canceled.v1.json"] = append(canceled, '\n')
	lost, err := json.Marshal(runLostEnvelope(submissionTestRequestID))
	if err != nil {
		t.Fatal(err)
	}
	bodies["error_context_fabric_run_lost.v1.json"] = append(lost, '\n')
	for name, body := range bodies {
		path := repopath.Path(t, "contracts", "examples", "v1", name)
		if os.Getenv("ACR_WRITE_RUN_LEASE_GOLDENS") == "1" {
			if err := os.WriteFile(filepath.Clean(path), body, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		golden, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (regenerate with ACR_WRITE_RUN_LEASE_GOLDENS=1)", name, err)
		}
		if !bytes.Equal(golden, body) {
			t.Fatalf("%s differs from the route's body:\n%s\nwant\n%s", name, body, golden)
		}
	}
}

// waitEntered waits for a run to start, and fails after 10 s.
func waitEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not start within 10 s")
	}
}

// The holder renews its lease every renew interval, so a run that lives
// longer than one lease term is never taken over while its holder lives;
// once the holder stops, the run is taken over one term after its last
// renewal.
func TestTheHolderRenewsItsLease(t *testing.T) {
	harness, store, _ := newLeaseHarness(t, 2)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	stream, _ := openLiveStream(t, harness.streamRequest(server, harness.tokenA, "submission-renew-001"))
	if stream == nil {
		t.Fatal("no stream")
	}
	accepted, _ := stream.next(5 * time.Second)
	waitEntered(t, harness.investigator.entered)
	run := contextfabric.RunKey{OrgID: "org_1", ReservedResultID: accepted.event.ReservedResultID}
	leaseOf := func() time.Time {
		page, err := store.ReadRunEvents(context.Background(), run, 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		return page.Record.LeaseExpiresAt
	}
	// Four renew intervals: each one renews, so the lease always ends a
	// term after the clock.
	for step := 0; step < 4; step++ {
		harness.clock.Advance(contextfabric.RunLeaseRenewInterval)
		want := harness.clock.Now().Add(contextfabric.RunLeaseTTL)
		waitFor(t, func() bool { return !leaseOf().Before(want) })
		if taken, err := harness.apps[1].TakeOverLostRuns(context.Background()); err != nil || taken != 0 {
			t.Fatalf("step %d: %d runs taken over (%v) while the holder renews", step, taken, err)
		}
	}
	close(harness.investigator.release)
	if frames := stream.rest(5 * time.Second); frames[len(frames)-1].event.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("the renewed run ended %+v", frames[len(frames)-1].event)
	}
}

// A turn of a deleted thread is closed for attach and cancel too (design
// §6 T6): both answer 404 like an unknown id, and the cancel writes nothing.
func TestAttachAndCancelOfAClosedThreadsTurnAreNotFound(t *testing.T) {
	harness, store, _ := newLeaseHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	request := harness.streamRequest(server, harness.tokenA, "submission-closed-01")
	request.Header.Set(contractsv1.ContextFabricThreadIDHeader, "thread-closed-0001")
	stream, _ := openLiveStream(t, request)
	if stream == nil {
		t.Fatal("no stream")
	}
	accepted, _ := stream.next(5 * time.Second)
	waitEntered(t, harness.investigator.entered)
	if deleted := harness.threadCall(http.MethodDelete, harness.tokenA, threadPath("thread-closed-0001")); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete thread = %d", deleted.Code)
	}
	if recorder := harness.cancelRun(0, harness.tokenA, "submission-closed-01"); recorder.Code != http.StatusNotFound {
		t.Fatalf("cancel of a closed thread's turn = %d, want 404", recorder.Code)
	}
	_, response := openLiveStream(t, harness.attachRequest(server, harness.tokenA, "submission-closed-01"))
	if status, code, _ := attachRefusal(t, response); status != http.StatusNotFound || code != "not_found" {
		t.Fatalf("attach of a closed thread's turn = %d %s, want 404", status, code)
	}
	page, err := store.ReadRunEvents(context.Background(), contextfabric.RunKey{OrgID: "org_1", ReservedResultID: accepted.event.ReservedResultID}, 0, 1)
	if err != nil || !page.Record.CancelRequestedAt.IsZero() {
		t.Fatalf("the refused cancel wrote t_req %v (%v)", page.Record.CancelRequestedAt, err)
	}
	close(harness.investigator.release)
	stream.rest(5 * time.Second)
}

// A JSON create (no stream) with a submission is not leased: the lease is
// the stream's (plan §2.7.9), and the JSON response stays connection-owned.
func TestAJSONSubmissionIsNotLeased(t *testing.T) {
	harness, store, _ := newLeaseHarness(t, 1)
	servedResultID(t, harness.post(0, harness.tokenA, "submission-json-0001", submissionQuestion))
	key, err := contextfabric.SubmissionKeyFor(harness.investigator.lastPrincipal.Load().(storage.Principal), "submission-json-0001")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.GetSubmission(context.Background(), key, harness.clock.Now())
	if err != nil || record.Leased() {
		t.Fatalf("record %+v (%v), want no lease", record, err)
	}
}

// A running JSON submission is connection-owned (leases are the stream's):
// cancel cannot stop it and says so -- 409 submission_in_progress with
// details.reason not_leased and Retry-After -- and attach has no log to
// serve -- 410 cursor_expired with details.reason not_logged. Neither
// changes the run, which finishes with its answer.
func TestARunningJSONSubmissionCanBeNeitherCanceledNorAttached(t *testing.T) {
	harness, store, _ := newLeaseHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	finished := make(chan int, 1)
	go func() { finished <- harness.post(0, harness.tokenA, "submission-json-run01", submissionQuestion).Code }()
	waitEntered(t, harness.investigator.entered)

	recorder := harness.cancelRun(0, harness.tokenA, "submission-json-run01")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("cancel of a running JSON submission = %d %s, want 409", recorder.Code, recorder.Body.String())
	}
	var refusal struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict || refusal.Error.Code != contractsv1.ErrorCodeSubmissionInProgress || refusal.Error.Details["reason"] != "not_leased" || recorder.Header().Get("Retry-After") != "3" {
		t.Fatalf("cancel of a running JSON submission = %d %s (Retry-After %q), want 409 submission_in_progress not_leased", recorder.Code, recorder.Body.String(), recorder.Header().Get("Retry-After"))
	}

	attach, response := openLiveStream(t, harness.attachRequest(harness.serveApp(0), harness.tokenA, "submission-json-run01"))
	if attach != nil {
		t.Fatal("an attach to a running JSON submission opened a stream")
	}
	status, code, body := attachRefusal(t, response)
	var expired struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &expired)
	if status != http.StatusGone || code != contractsv1.ErrorCodeCursorExpired || expired.Error.Details["reason"] != "not_logged" {
		t.Fatalf("attach to a running JSON submission = %d %s, want 410 cursor_expired not_logged", status, body)
	}

	key, err := contextfabric.SubmissionKeyFor(harness.investigator.lastPrincipal.Load().(storage.Principal), "submission-json-run01")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.GetSubmission(context.Background(), key, harness.clock.Now())
	if err != nil || record.Leased() || !record.CancelRequestedAt.IsZero() || record.State != contextfabric.SubmissionRunning {
		t.Fatalf("record %+v (%v), want running, no lease and no t_req", record, err)
	}
	close(harness.investigator.release)
	select {
	case code := <-finished:
		if code != http.StatusOK {
			t.Fatalf("the JSON run answered %d, want its answer", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the JSON run did not finish")
	}
}
