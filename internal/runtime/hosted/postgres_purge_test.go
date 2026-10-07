package hosted

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
)

func TestRunPurgeTickLoop_invokesBoundedPurgeOnEachTick(t *testing.T) {
	// Given
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	var mu sync.Mutex
	var calls []int
	purge := func(_ context.Context, _ time.Time, limit int) (int, error) {
		mu.Lock()
		calls = append(calls, limit)
		mu.Unlock()
		return 0, nil
	}
	loopDone := make(chan struct{})

	// When
	go func() {
		defer close(loopDone)
		runPurgeTickLoop(ctx, tick, time.Now, purge, 7, packetPurgeFailureMessage, nil)
	}()
	tick <- time.Now()
	tick <- time.Now()
	cancel()
	<-loopDone

	// Then
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != 7 || calls[1] != 7 {
		t.Fatalf("purge calls = %#v, want two calls with bounded limit 7", calls)
	}
}

func TestRunPurgeTickLoop_returnsPromptlyWhenCancelled_noLeak(t *testing.T) {
	// Given
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	purge := func(context.Context, time.Time, int) (int, error) { return 0, nil }
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		runPurgeTickLoop(ctx, tick, time.Now, purge, 1, packetPurgeFailureMessage, nil)
	}()

	// When
	cancel()

	// Then
	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("purge tick loop leaked: did not return after cancellation")
	}
}

func TestRunPurgeTickLoop_toleratesPurgeFailureAndKeepsRunning(t *testing.T) {
	// Given
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	var mu sync.Mutex
	calls := 0
	purge := func(context.Context, time.Time, int) (int, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return 0, errors.New("transient purge failure: dsn=postgres://user:secret@host/db")
	}
	loopDone := make(chan struct{})

	// When
	go func() {
		defer close(loopDone)
		runPurgeTickLoop(ctx, tick, time.Now, purge, 1, packetPurgeFailureMessage, nil)
	}()
	tick <- time.Now()
	tick <- time.Now()
	cancel()
	<-loopDone

	// Then
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("purge calls = %d after transient failures, want 2 (loop keeps running)", calls)
	}
}

func TestRunPurgeTickLoop_notifiesObserverWithRedactedMessage_onPurgeFailure(t *testing.T) {
	// Given: purge fails with an error carrying sensitive detail (a DSN with
	// embedded credentials), which the observer must never see.
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	sensitiveErr := errors.New("dial failed: postgres://user:s3cr3t@db-host:5432/acr")
	purge := func(context.Context, time.Time, int) (int, error) { return 0, sensitiveErr }
	var mu sync.Mutex
	var notifications []string
	observe := func(_ context.Context, message string) {
		mu.Lock()
		notifications = append(notifications, message)
		mu.Unlock()
	}
	loopDone := make(chan struct{})

	// When
	go func() {
		defer close(loopDone)
		runPurgeTickLoop(ctx, tick, time.Now, purge, 1, packetPurgeFailureMessage, observe)
	}()
	tick <- time.Now()
	tick <- time.Now()
	cancel()
	<-loopDone

	// Then: exactly one notification per failed tick, always the fixed
	// redacted message, never the raw error or its sensitive detail.
	mu.Lock()
	defer mu.Unlock()
	if len(notifications) != 2 {
		t.Fatalf("notifications = %#v, want 2 (one per failed tick)", notifications)
	}
	for _, message := range notifications {
		if message != packetPurgeFailureMessage {
			t.Fatalf("notification message = %q, want constant %q", message, packetPurgeFailureMessage)
		}
		if strings.Contains(message, "s3cr3t") || strings.Contains(message, sensitiveErr.Error()) {
			t.Fatalf("notification message leaked raw error detail: %q", message)
		}
	}
}

func TestStartPacketPurgeLoop_performsInitialBoundedPurgeBeforeStartingTicker(t *testing.T) {
	// Given
	var mu sync.Mutex
	var calls []int
	purge := func(_ context.Context, _ time.Time, limit int) (int, error) {
		mu.Lock()
		calls = append(calls, limit)
		mu.Unlock()
		return 0, nil
	}

	// When
	closeLoop, err := startPacketPurgeLoop(context.Background(), purge, nil, nil)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := closeLoop(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != defaultPacketPurgeBatchLimit {
		t.Fatalf("initial purge calls = %#v, want one bounded call", calls)
	}
}

func TestRunPurgeTickLoop_notifiesOnceForEachIndependentFailure(t *testing.T) {
	// Given: the same failure-then-recover pattern startPacketPurgeLoop's
	// ticker would drive across multiple ticks over time.
	purge := func(context.Context, time.Time, int) (int, error) {
		return 0, errors.New("connection refused: postgres://user:secret@host/db")
	}
	notified := make(chan string, 4)
	observe := func(_ context.Context, message string) { notified <- message }
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	loopDone := make(chan struct{})

	// When
	go func() {
		defer close(loopDone)
		runPurgeTickLoop(ctx, tick, time.Now, purge, defaultPacketPurgeBatchLimit, packetPurgeFailureMessage, observe)
	}()
	tick <- time.Now()
	cancel()
	<-loopDone

	// Then
	select {
	case message := <-notified:
		if message != packetPurgeFailureMessage {
			t.Fatalf("notified message = %q, want %q", message, packetPurgeFailureMessage)
		}
	default:
		t.Fatal("observer was never notified of the ticked purge failure")
	}
}

func TestStartPacketPurgeLoop_propagatesInitialPurgeFailureAsDeleteProof(t *testing.T) {
	// Given
	wantErr := errors.New("permission denied for table acr.context_packet_snapshots")
	purge := func(context.Context, time.Time, int) (int, error) { return 0, wantErr }

	// When
	closeLoop, err := startPacketPurgeLoop(context.Background(), purge, nil, nil)

	// Then
	if !errors.Is(err, wantErr) || closeLoop != nil {
		t.Fatalf("startPacketPurgeLoop() error = %v, closer present = %t; want error %v and no closer", err, closeLoop != nil, wantErr)
	}
}

func TestStartPacketPurgeLoop_closeJoinsBackgroundGoroutine_noLeak(t *testing.T) {
	// Given
	purge := func(context.Context, time.Time, int) (int, error) { return 0, nil }
	closeLoop, err := startPacketPurgeLoop(context.Background(), purge, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// When
	closeDone := make(chan error, 1)
	go func() { closeDone <- closeLoop() }()

	// Then
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close() = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close() did not join the purge goroutine promptly")
	}
	if secondErr := closeLoop(); secondErr != nil {
		t.Fatalf("second close() = %v, want nil (idempotent join)", secondErr)
	}
}

func TestPacketPurgeSlogObserver_nilLoggerYieldsNilObserver(t *testing.T) {
	// Given / When / Then: a nil logger must not panic when adapted, and
	// must yield a nil observer so the tick loop's nil-check skips it.
	if observe := packetPurgeSlogObserver(nil); observe != nil {
		t.Fatal("packetPurgeSlogObserver(nil) = non-nil observer, want nil")
	}
}

func TestPacketPurgeSlogObserver_logsOnlyTheFixedRedactedMessage(t *testing.T) {
	// Given
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	observe := packetPurgeSlogObserver(logger)

	// When
	observe(context.Background(), packetPurgeFailureMessage)

	// Then
	output := buf.String()
	if !strings.Contains(output, packetPurgeFailureMessage) {
		t.Fatalf("log output = %q, want it to contain %q", output, packetPurgeFailureMessage)
	}
}

// fakeOAuthPurger records every PurgeExpired call and answers a scripted
// result, standing in for *storagepostgres.OAuthStore.
type fakeOAuthPurger struct {
	mu     sync.Mutex
	calls  []fakeOAuthPurgeCall
	result storagepostgres.OAuthPurgeResult
	err    error

	// remaining and remainingErr script CountPurgeRemaining; remainingCalls
	// records each probe, so a test can prove a failed purge is not probed.
	remaining      storagepostgres.OAuthPurgeRemaining
	remainingErr   error
	remainingCalls []fakeOAuthPurgeCall
}

type fakeOAuthPurgeCall struct {
	requestGrace, clientIdle time.Duration
	limit                    int
}

func (f *fakeOAuthPurger) PurgeExpired(_ context.Context, _ time.Time, requestGrace, clientIdle time.Duration, limit int) (storagepostgres.OAuthPurgeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeOAuthPurgeCall{requestGrace: requestGrace, clientIdle: clientIdle, limit: limit})
	return f.result, f.err
}

func (f *fakeOAuthPurger) CountPurgeRemaining(_ context.Context, _ time.Time, requestGrace, clientIdle time.Duration, limit int) (storagepostgres.OAuthPurgeRemaining, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remainingCalls = append(f.remainingCalls, fakeOAuthPurgeCall{requestGrace: requestGrace, clientIdle: clientIdle, limit: limit})
	return f.remaining, f.remainingErr
}

func oauthConfiguredTestConfig() config.Config {
	return config.Config{
		OAuthIssuer: "https://acr.example.test", OAuthResources: []string{"https://mcp.example.test/mcp"},
		OAuthConsentURL:        "https://www.example.test/acr/authorize",
		OAuthRequestPurgeGrace: 45 * 24 * time.Hour, OAuthClientIdleTTL: 36 * time.Hour,
	}
}

// CHAOS-6191: a deployment without the OAuth login never runs the purge, so a
// runtime role that was never granted DELETE on the OAuth tables still starts.
func TestStartConfiguredOAuthPurge_unconfiguredNeverPurges(t *testing.T) {
	// Given
	purger := &fakeOAuthPurger{err: errors.New("permission denied for table acr.oauth_clients")}

	// When
	closeLoop, err := startConfiguredOAuthPurge(context.Background(), config.Config{}, purger, nil)

	// Then
	if err != nil || closeLoop == nil {
		t.Fatalf("unconfigured: err = %v, closer present = %t; want no error and a no-op closer", err, closeLoop != nil)
	}
	if closeErr := closeLoop(); closeErr != nil {
		t.Fatalf("no-op closer error = %v", closeErr)
	}
	purger.mu.Lock()
	defer purger.mu.Unlock()
	if len(purger.calls) != 0 {
		t.Fatalf("purge ran %d time(s) with OAuth unconfigured; want 0", len(purger.calls))
	}
}

// Configured, the initial purge runs before startup returns with the
// configured windows and the bounded batch, and a failure (a missing DELETE
// grant) fails startup instead of being retried silently.
func TestStartConfiguredOAuthPurge_configuredPurgesAtStartupWithConfigWindows(t *testing.T) {
	// Given
	cfg := oauthConfiguredTestConfig()
	purger := &fakeOAuthPurger{}

	// When
	closeLoop, err := startConfiguredOAuthPurge(context.Background(), cfg, purger, nil)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLoop() })
	purger.mu.Lock()
	defer purger.mu.Unlock()
	want := fakeOAuthPurgeCall{requestGrace: 45 * 24 * time.Hour, clientIdle: 36 * time.Hour, limit: defaultOAuthPurgeBatchLimit}
	if len(purger.calls) != 1 || purger.calls[0] != want {
		t.Fatalf("startup purge calls = %#v, want exactly [%#v]", purger.calls, want)
	}
}

func TestStartConfiguredOAuthPurge_missingDeleteGrantFailsStartup(t *testing.T) {
	// Given
	wantErr := errors.New("permission denied for table acr.oauth_clients")
	purger := &fakeOAuthPurger{err: wantErr}

	// When
	closeLoop, err := startConfiguredOAuthPurge(context.Background(), oauthConfiguredTestConfig(), purger, nil)

	// Then
	if !errors.Is(err, wantErr) || closeLoop != nil {
		t.Fatalf("error = %v, closer present = %t; want %v and no closer", err, closeLoop != nil, wantErr)
	}
}

// Non-positive windows (a Config built without config.Load) are refused by the
// store's own input check at the initial purge, so they fail startup rather
// than deleting everything; the check runs before any statement.
func TestStartConfiguredOAuthPurge_zeroWindowsFailStartupBeforeAnyDelete(t *testing.T) {
	// Given
	database := sql.OpenDB(idleConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := storagepostgres.NewOAuthStore(database)
	if err != nil {
		t.Fatal(err)
	}
	cfg := oauthConfiguredTestConfig()
	cfg.OAuthRequestPurgeGrace, cfg.OAuthClientIdleTTL = 0, 0

	// When
	closeLoop, err := startConfiguredOAuthPurge(context.Background(), cfg, store, nil)

	// Then
	if err == nil || closeLoop != nil {
		t.Fatalf("zero windows: err = %v, closer present = %t; want a startup error", err, closeLoop != nil)
	}
}

func TestOAuthPurgeFunc_logsOneCountLineOnEveryTick(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	purger := &fakeOAuthPurger{}
	purge := oauthPurgeFunc(purger, 2*time.Hour, time.Hour, logger)
	oneLine := func(want ...string) {
		t.Helper()
		line := strings.TrimSpace(logs.String())
		if strings.Count(line, "\n") != 0 || !strings.Contains(line, `msg="oauth purge"`) {
			t.Fatalf("want exactly one oauth purge line, got %q", line)
		}
		for _, fragment := range want {
			if !strings.Contains(line, fragment) {
				t.Fatalf("line %q lacks %q", line, fragment)
			}
		}
		logs.Reset()
	}

	// An empty tick is the loop's heartbeat: one line, both counts zero.
	if total, err := purge(context.Background(), time.Now(), 5); total != 0 || err != nil {
		t.Fatalf("empty tick: total=%d err=%v", total, err)
	}
	oneLine("requests=0", "clients=0", "device_authorizations=0", "requests_remaining=0", "clients_remaining=0", "device_authorizations_remaining=0")

	// A tick that deleted rows logs the two counts.
	purger.result = storagepostgres.OAuthPurgeResult{Requests: 3, Clients: 2, DeviceAuthorizations: 4}
	if total, err := purge(context.Background(), time.Now(), 5); total != 9 || err != nil {
		t.Fatalf("delete tick: total=%d err=%v", total, err)
	}
	oneLine("requests=3", "clients=2", "device_authorizations=4", "requests_remaining=0", "clients_remaining=0", "device_authorizations_remaining=0")

	// A tick that deleted requests and then failed reports the failure and still
	// logs what was deleted; the database just failed, so it is not probed and
	// the line carries no remaining (an unmeasured count is absent, never zero).
	wantErr := errors.New("client statement failed")
	purger.result, purger.err = storagepostgres.OAuthPurgeResult{Requests: 1}, wantErr
	probes := len(purger.remainingCalls)
	total, err := purge(context.Background(), time.Now(), 5)
	if total != 1 || !errors.Is(err, wantErr) {
		t.Fatalf("partial-failure tick: total=%d err=%v", total, err)
	}
	oneLine("requests=1", "clients=0")
	if strings.Contains(logs.String(), "remaining") || len(purger.remainingCalls) != probes {
		t.Fatalf("a failed purge was probed or logged remaining: calls %d -> %d, logs %q", probes, len(purger.remainingCalls), logs.String())
	}
}

// CHAOS-7249: a tick that skipped every eligible row must not read like a tick
// that found nothing. Both delete zero rows; only the remaining counts differ,
// and the line carries them.
func TestOAuthPurgeFunc_skippedEverythingIsVisibleNextToEmpty(t *testing.T) {
	tick := func(remaining storagepostgres.OAuthPurgeRemaining) string {
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
		purger := &fakeOAuthPurger{remaining: remaining}
		if _, err := oauthPurgeFunc(purger, 2*time.Hour, time.Hour, logger)(context.Background(), time.Now(), 500); err != nil {
			t.Fatal(err)
		}
		want := fakeOAuthPurgeCall{requestGrace: 2 * time.Hour, clientIdle: time.Hour, limit: 500}
		if len(purger.remainingCalls) != 1 || purger.remainingCalls[0] != want {
			t.Fatalf("probe calls = %#v, want exactly [%#v] (the purge's own windows and batch limit)", purger.remainingCalls, want)
		}
		return strings.TrimSpace(logs.String())
	}

	empty := tick(storagepostgres.OAuthPurgeRemaining{})
	skipped := tick(storagepostgres.OAuthPurgeRemaining{Requests: 4, Clients: 2, DeviceAuthorizations: 3})

	for _, line := range []string{empty, skipped} {
		if !strings.Contains(line, "requests=0 clients=0 device_authorizations=0") {
			t.Fatalf("both ticks deleted nothing, line = %q", line)
		}
	}
	if !strings.Contains(empty, "requests_remaining=0 clients_remaining=0 device_authorizations_remaining=0") {
		t.Fatalf("empty tick line = %q, want remaining 0/0/0", empty)
	}
	if !strings.Contains(skipped, "requests_remaining=4 clients_remaining=2 device_authorizations_remaining=3") {
		t.Fatalf("skipped-everything tick line = %q, want remaining 4/2/3", skipped)
	}
}

// A probe that fails fails the tick through the tick loop's redacted warning
// (the error is returned, not swallowed), and the line still reports what the
// purge deleted, without a remaining the probe never produced.
func TestOAuthPurgeFunc_failedProbeFailsTheTick(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	wantErr := errors.New("count statement failed")
	purger := &fakeOAuthPurger{result: storagepostgres.OAuthPurgeResult{Requests: 2, Clients: 1, DeviceAuthorizations: 1}, remainingErr: wantErr}

	total, err := oauthPurgeFunc(purger, 2*time.Hour, time.Hour, logger)(context.Background(), time.Now(), 5)

	if total != 4 || !errors.Is(err, wantErr) {
		t.Fatalf("total=%d err=%v; want 4 deleted and the probe error", total, err)
	}
	line := strings.TrimSpace(logs.String())
	if !strings.Contains(line, "requests=2 clients=1 device_authorizations=1") || strings.Contains(line, "remaining") {
		t.Fatalf("line = %q, want the deleted counts and no remaining", line)
	}
}

type stubDeviceCredentialRevoker struct {
	mu     sync.Mutex
	limits []int
	result int
	err    error
}

func (s *stubDeviceCredentialRevoker) RevokeUnacknowledged(_ context.Context, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits = append(s.limits, limit)
	return s.result, s.err
}

func TestStartDeviceCredentialSweep_runsABoundedSweepAtStartupAndLogsOnlyTheCount(t *testing.T) {
	revoker := &stubDeviceCredentialRevoker{result: 2}
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	closeLoop, err := startDeviceCredentialSweep(context.Background(), revoker, logger, nil)

	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLoop() })
	revoker.mu.Lock()
	defer revoker.mu.Unlock()
	if len(revoker.limits) != 1 || revoker.limits[0] != defaultDeviceCredentialSweepBatchLimit {
		t.Fatalf("sweep limits = %v, want one bounded call", revoker.limits)
	}
	if !strings.Contains(logs.String(), "revoked=2") {
		t.Fatalf("log = %q, want the revoked count", logs.String())
	}
	for _, field := range []string{"oauth_step=credential_revoke", "source=sweep"} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("log = %q, want %s so one query on oauth_step finds every revoke", logs.String(), field)
		}
	}
}

func TestStartDeviceCredentialSweep_failsStartupWhenTheFirstSweepFails(t *testing.T) {
	revoker := &stubDeviceCredentialRevoker{err: errors.New("permission denied")}

	closeLoop, err := startDeviceCredentialSweep(context.Background(), revoker, nil, nil)

	if err == nil || closeLoop != nil {
		t.Fatalf("startDeviceCredentialSweep() = (%v, %v), want a startup failure", closeLoop != nil, err)
	}
}

func TestDeviceCredentialSweepIntervalIsInsideTheAckWindow(t *testing.T) {
	if defaultDeviceCredentialSweepInterval*2 > storage.DeviceCredentialAckWindow {
		t.Fatalf("sweep interval %v is too long for ack window %v", defaultDeviceCredentialSweepInterval, storage.DeviceCredentialAckWindow)
	}
}
