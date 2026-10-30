package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// CHAOS-11949 integration (plan §2.7.9): the run lease, the event log, attach
// and cancel on one real PostgreSQL with one connection pool per App -- the
// replicas of one deployment. Every case asserts stored rows, not only
// statuses.

// runLeaseAdminDSNEnv points the Postgres cases at an existing server (a
// fresh database per test) instead of a container: the same cases, for a
// host without a container runtime. Unset, the cases start a container as
// every other Postgres case does (ACR_CONTAINER_TESTS=1).
const runLeaseAdminDSNEnv = "ACR_TEST_POSTGRES_ADMIN_DSN"

func runLeasePostgres(t *testing.T) (string, *sql.DB) {
	t.Helper()
	adminDSN := os.Getenv(runLeaseAdminDSNEnv)
	if adminDSN == "" {
		return submissionPostgres(t)
	}
	server := openSubmissionPool(t, adminDSN)
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	name := "acr_run_lease_" + hex.EncodeToString(buf)
	if _, err := server.ExecContext(context.Background(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	dsn := parsed.String()
	admin := openSubmissionPool(t, dsn)
	runner, err := migrations.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Apply(context.Background(), admin); err != nil {
		t.Fatal(fmt.Errorf("apply production migrations: %w", err))
	}
	return dsn, admin
}

// newPostgresLeaseHarness is apps Apps over apps pools, the run lease on.
func newPostgresLeaseHarness(t *testing.T, dsn string, apps int) (*submissionHarness, []*lockedBuffer) {
	t.Helper()
	var stores []submissionTestStore
	for index := 0; index < apps; index++ {
		store, err := pginvestigation.NewStore(openSubmissionPool(t, dsn))
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
	}
	harness := newSubmissionHarness(t, stores...)
	return harness, enableRunLease(t, harness)
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// The log is the run's, not the replica's: a client sees `accepted` and two
// heartbeats from replica a and disconnects; the run finishes; replica a
// goes away; a replica that took no part (its own pool) replays the whole
// stream from the log -- every event byte for byte, with the run's seq and
// request id -- and from any cursor, and ends it with the stored result.
func TestTheRunLogSurvivesItsReplicaOnPostgres(t *testing.T) {
	dsn, admin := runLeasePostgres(t)
	harness, _ := newPostgresLeaseHarness(t, dsn, 3)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	harness.apps[0].eventStream.heartbeat = 30 * time.Millisecond
	first := harness.serveApp(0)
	stream, _ := openLiveStream(t, harness.streamRequest(first, harness.tokenA, "submission-pglog-001"))
	if stream == nil {
		t.Fatal("no stream")
	}
	var seen []liveFrame
	for len(seen) < 3 {
		frame, _ := stream.next(5 * time.Second)
		seen = append(seen, frame)
	}
	waitEntered(t, harness.investigator.entered)
	stream.close()
	close(harness.investigator.release)
	reserved := seen[0].event.ReservedResultID
	waitFor(t, func() bool {
		return countRows(t, admin, `SELECT count(*) FROM acr.context_fabric_investigation_run_events WHERE reserved_result_id = $1 AND terminal_kind IS NOT NULL`, reserved) == 1
	})
	first.Close()

	fresh := harness.serveApp(2)
	for _, cursor := range []string{"", "2"} {
		var values []string
		if cursor != "" {
			values = []string{cursor}
		}
		attach, response := openLiveStream(t, harness.attachRequest(fresh, harness.tokenA, "submission-pglog-001", values...))
		if attach == nil {
			status, code, body := attachRefusal(t, response)
			t.Fatalf("attach from %q = %d %s %s", cursor, status, code, body)
		}
		frames := attach.rest(5 * time.Second)
		skip, _ := strconv.Atoi(cursor)
		prefix := seen[:skip]
		events := joinedStream(t, prefix, frames)
		for index, frame := range seen[skip:] {
			if !bytes.Equal(frames[index].data, frame.data) {
				t.Fatalf("cursor %q: event %d replayed as\n%s\nthe run wrote\n%s", cursor, frame.event.Seq, frames[index].data, frame.data)
			}
		}
		if terminal := events[len(events)-1]; terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
			t.Fatalf("cursor %q: terminal %+v", cursor, terminal)
		}
	}
	// The log holds no result content: its terminal is the result id.
	var kind, resultID string
	var data sql.NullString
	if err := admin.QueryRowContext(context.Background(), `SELECT terminal_kind, terminal_result_id, data FROM acr.context_fabric_investigation_run_events WHERE reserved_result_id = $1 AND terminal_kind IS NOT NULL`, reserved).Scan(&kind, &resultID, &data); err != nil {
		t.Fatal(err)
	}
	if kind != string(contextfabric.RunTerminalResult) || resultID != reserved || data.Valid {
		t.Fatalf("terminal row kind %q result %q data %v, want a result marker with no data", kind, resultID, data)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// A cancel on replica b stops the run replica a holds, through the record
// alone: no attempt starts after t_eff, the cancel lands in a backoff, t_eff
// - t_req is measured inside the bound with the production poll, and the log
// and the record show canceled / abandoned.
func TestACancelOnOneReplicaStopsTheOthersRunOnPostgres(t *testing.T) {
	dsn, admin := runLeasePostgres(t)
	harness, logs := newPostgresLeaseHarness(t, dsn, 2)
	for _, app := range harness.apps {
		app.runLease.cancelPoll = contextfabric.RunCancelPollInterval
	}
	investigator := newAttemptInvestigator(harness.investigator)
	harness.useInvestigator(investigator)
	stream, _ := openLiveStream(t, harness.streamRequest(harness.serveApp(0), harness.tokenA, "submission-pgcan-001"))
	if stream == nil {
		t.Fatal("no stream")
	}
	accepted, _ := stream.next(5 * time.Second)
	waitEntered(t, investigator.entered)
	waitFor(t, func() bool { return len(investigator.startedAttempts()) >= 2 })
	time.Sleep(investigator.attempt + 10*time.Millisecond)
	requested := time.Now()
	if recorder := harness.cancelRun(1, harness.tokenA, "submission-pgcan-001"); recorder.Code != http.StatusAccepted {
		t.Fatalf("cancel = %d %s", recorder.Code, recorder.Body.String())
	}
	if again := harness.cancelRun(1, harness.tokenA, "submission-pgcan-001"); again.Code != http.StatusAccepted && again.Code != http.StatusNoContent {
		t.Fatalf("repeat cancel = %d", again.Code)
	}
	frames := stream.rest(10 * time.Second)
	events := joinedStream(t, []liveFrame{accepted}, frames)
	if terminal := events[len(events)-1]; terminal.Error == nil || terminal.Error.Error.Code != contractsv1.ErrorCodeCanceled {
		t.Fatalf("terminal %+v, want canceled", terminal)
	}
	effective := logs[0].waitLine(t, "context fabric investigation cancel effective", nil)
	if effective["cause"] != "cancel_requested" {
		t.Fatalf("cancel effective %v", effective)
	}
	tEff, err := time.Parse(time.RFC3339Nano, effective["t_eff"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if bound := tEff.Sub(requested); bound > contractsv1.ContextFabricRunCancelEffectiveSeconds*time.Second {
		t.Fatalf("t_eff - t_req = %s, over the bound", bound)
	} else {
		t.Logf("t_eff - t_req = %s (other replica, production poll %s)", bound, contextfabric.RunCancelPollInterval)
	}
	for _, started := range investigator.startedAttempts() {
		if started.After(tEff) {
			t.Fatalf("an attempt started at %s, after t_eff %s", started, tEff)
		}
	}
	reserved := accepted.event.ReservedResultID
	var state string
	var cancelAt sql.NullTime
	if err := admin.QueryRowContext(context.Background(), `SELECT state, cancel_requested_at FROM acr.context_fabric_investigation_submissions WHERE reserved_result_id = $1`, reserved).Scan(&state, &cancelAt); err != nil {
		t.Fatal(err)
	}
	if state != "abandoned" || !cancelAt.Valid {
		t.Fatalf("record state %q cancel_requested_at %v, want abandoned with t_req", state, cancelAt)
	}
	if count := countRows(t, admin, `SELECT count(*) FROM acr.context_fabric_investigation_run_events WHERE reserved_result_id = $1 AND terminal_kind = 'error' AND terminal_error::jsonb #>> '{error,code}' = 'canceled'`, reserved); count != 1 {
		t.Fatalf("%d canceled terminals in the log, want 1", count)
	}
	if final := harness.cancelRun(0, harness.tokenA, "submission-pgcan-001"); final.Code != http.StatusNoContent {
		t.Fatalf("cancel of the final run = %d, want 204", final.Code)
	}
}

// Runs whose holder stopped are taken over once each, though two replicas
// with their own pools take over at the same moment: every record ends
// abandoned with one taker as its lease_holder and exactly one run_lost
// terminal. The stalled holder then finds its run taken and cancels it.
func TestLostRunsAreTakenOverOnceAcrossReplicasOnPostgres(t *testing.T) {
	dsn, admin := runLeasePostgres(t)
	harness, logs := newPostgresLeaseHarness(t, dsn, 3)
	holderStore, ok := harness.apps[0].runtime.InvestigationRuns.(contextfabric.RunStore)
	if !ok {
		t.Fatal("no run store")
	}
	stalled := &stallingRunStore{RunStore: holderStore}
	harness.apps[0].runtime.InvestigationRuns = stalled
	harness.investigator.mode.Store(int32(runBlockThenSave))
	harness.apps[0].config.AskDevLimits = contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 16, MaxRunningPerOrg: 16}
	server := harness.serveApp(0)
	// A failure below must not leave the server waiting on a stalled holder.
	t.Cleanup(stalled.resume)
	const runs = 6
	var streams []*liveStream
	var reserved []string
	for index := 0; index < runs; index++ {
		stream, response := openLiveStream(t, harness.streamRequest(server, harness.tokenA, fmt.Sprintf("submission-pgtake-%02d", index)))
		if stream == nil {
			status, code, body := attachRefusal(t, response)
			t.Fatalf("run %d = %d %s %s", index, status, code, body)
		}
		accepted, _ := stream.next(5 * time.Second)
		waitEntered(t, harness.investigator.entered)
		streams = append(streams, stream)
		reserved = append(reserved, accepted.event.ReservedResultID)
	}
	stalled.stall()
	harness.clock.Advance(contextfabric.RunLeaseTTL + time.Second)
	var wg sync.WaitGroup
	counts := make([]int, 3)
	for _, taker := range []int{1, 2} {
		wg.Add(1)
		go func(taker int) {
			defer wg.Done()
			count, err := harness.apps[taker].TakeOverLostRuns(context.Background())
			if err != nil {
				t.Error(err)
			}
			counts[taker] = count
		}(taker)
	}
	wg.Wait()
	if total := counts[1] + counts[2]; total != runs {
		t.Fatalf("takeovers %v, want %d in total", counts, runs)
	}
	holders := map[string]bool{harness.apps[1].runLease.holder: true, harness.apps[2].runLease.holder: true}
	for _, id := range reserved {
		var state, holder string
		if err := admin.QueryRowContext(context.Background(), `SELECT state, lease_holder FROM acr.context_fabric_investigation_submissions WHERE reserved_result_id = $1`, id).Scan(&state, &holder); err != nil {
			t.Fatal(err)
		}
		if state != "abandoned" || !holders[holder] {
			t.Fatalf("run %s: state %q holder %q, want abandoned and taken by a replica", id, state, holder)
		}
		if count := countRows(t, admin, `SELECT count(*) FROM acr.context_fabric_investigation_run_events WHERE reserved_result_id = $1 AND terminal_kind IS NOT NULL AND terminal_error::jsonb #>> '{error,code}' = 'run_lost'`, id); count != 1 {
			t.Fatalf("run %s: %d run_lost terminals, want 1", id, count)
		}
	}
	stalled.resume()
	for index, stream := range streams {
		frames := stream.rest(10 * time.Second)
		if last := frames[len(frames)-1].event; last.Error == nil || last.Error.Error.Code != contractsv1.ErrorCodeRunLost {
			t.Fatalf("run %d: the old holder's stream ended %+v, want run_lost", index, last)
		}
	}
	if lost := len(logs[0].lines(ContextFabricRunLeaseLogMessage)); lost < runs {
		t.Fatalf("%d lease lines on the old holder, want one lease_lost per run", lost)
	}
	if count := countRows(t, admin, `SELECT count(*) FROM acr.context_fabric_investigation_run_events WHERE terminal_kind IS NOT NULL`); count != runs {
		t.Fatalf("%d terminals in the log after the old holder ended, want %d", count, runs)
	}
	if got := harness.investigator.executions.Load(); got != runs {
		t.Fatalf("executions = %d, want %d", got, runs)
	}
}

// The log is bounded and covered by retention: a run past the cap keeps the
// latest cap events and its terminal; an attach older than the window is
// 410; removing the submission record (what the retention job and the
// organization purge do) removes its log.
func TestTheRunLogIsBoundedAndGoesWithItsRecordOnPostgres(t *testing.T) {
	dsn, admin := runLeasePostgres(t)
	harness, _ := newPostgresLeaseHarness(t, dsn, 2)
	harness.apps[0].runLease.maxEvents = 4
	harness.apps[0].eventStream.heartbeat = 5 * time.Millisecond
	harness.investigator.mode.Store(int32(runBlockThenSave))
	stream, _ := openLiveStream(t, harness.streamRequest(harness.serveApp(0), harness.tokenA, "submission-pgcap-001"))
	if stream == nil {
		t.Fatal("no stream")
	}
	var seen []liveFrame
	for len(seen) < 10 {
		frame, _ := stream.next(5 * time.Second)
		seen = append(seen, frame)
	}
	close(harness.investigator.release)
	seen = append(seen, stream.rest(5*time.Second)...)
	reserved := seen[0].event.ReservedResultID
	if rows := countRows(t, admin, `SELECT count(*) FROM acr.context_fabric_investigation_run_events WHERE reserved_result_id = $1`, reserved); rows != 5 {
		t.Fatalf("%d log rows, want the latest 4 events and the terminal", rows)
	}
	other := harness.serveApp(1)
	_, response := openLiveStream(t, harness.attachRequest(other, harness.tokenA, "submission-pgcap-001", "1"))
	if status, code, _ := attachRefusal(t, response); status != http.StatusGone || code != contractsv1.ErrorCodeCursorExpired {
		t.Fatalf("attach older than the window = %d %s", status, code)
	}
	terminalSeq := seen[len(seen)-1].event.Seq
	attach, response := openLiveStream(t, harness.attachRequest(other, harness.tokenA, "submission-pgcap-001", strconv.FormatInt(terminalSeq-5, 10)))
	if attach == nil {
		status, code, body := attachRefusal(t, response)
		t.Fatalf("attach at the window = %d %s %s", status, code, body)
	}
	if frames := attach.rest(5 * time.Second); len(frames) != 5 || frames[4].event.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("the window replayed %d frames", len(frames))
	}
	if _, err := admin.ExecContext(context.Background(), `DELETE FROM acr.context_fabric_investigation_submissions WHERE reserved_result_id = $1`, reserved); err != nil {
		t.Fatal(err)
	}
	if rows := countRows(t, admin, `SELECT count(*) FROM acr.context_fabric_investigation_run_events WHERE reserved_result_id = $1`, reserved); rows != 0 {
		t.Fatalf("%d log rows outlived their record", rows)
	}
	if !strings.HasPrefix(reserved, "result_") {
		t.Fatalf("reserved id %q", reserved)
	}
}
