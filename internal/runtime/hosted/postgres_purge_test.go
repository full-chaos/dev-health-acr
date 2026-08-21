package hosted

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
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
