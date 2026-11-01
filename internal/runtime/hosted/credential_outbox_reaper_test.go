package hosted

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

type fakeOutboxReaper struct {
	calls   atomic.Int32
	result  storagepostgres.ReapResult
	err     error
	limits  chan int
	cutoffs chan time.Time
}

func newFakeOutboxReaper() *fakeOutboxReaper {
	return &fakeOutboxReaper{limits: make(chan int, 8), cutoffs: make(chan time.Time, 8)}
}

func (f *fakeOutboxReaper) ReapPublished(_ context.Context, olderThan time.Time, limit int) (storagepostgres.ReapResult, error) {
	f.calls.Add(1)
	select {
	case f.limits <- limit:
	default:
	}
	select {
	case f.cutoffs <- olderThan:
	default:
	}
	return f.result, f.err
}

func TestTheOutboxReaperMakesNoCallAtStartAndTicksWithTheSevenDayCutoff(t *testing.T) {
	reaper := newFakeOutboxReaper()
	stop := startCredentialOutboxReaper(context.Background(), reaper, nil, nil, time.Hour)
	if got := reaper.calls.Load(); got != 0 {
		t.Fatalf("the reaper ticked at start (%d): start must make no call", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	reaper = newFakeOutboxReaper()
	before := time.Now()
	stop = startCredentialOutboxReaper(context.Background(), reaper, nil, nil, 10*time.Millisecond)
	var limit int
	select {
	case limit = <-reaper.limits:
	case <-time.After(3 * time.Second):
		t.Fatal("no tick")
	}
	if limit != 100 {
		t.Errorf("batch limit = %d, want 100", limit)
	}
	cutoff := <-reaper.cutoffs
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if want := before.Add(-7 * 24 * time.Hour); cutoff.Before(want.Add(-time.Second)) || cutoff.After(time.Now().Add(-7*24*time.Hour).Add(time.Second)) {
		t.Errorf("cutoff = %v, want about %v: retention is seven days", cutoff, want)
	}
	calls := reaper.calls.Load()
	time.Sleep(50 * time.Millisecond)
	if reaper.calls.Load() != calls {
		t.Fatal("a tick ran after stop returned")
	}
}

func TestAFailingReapTickIsReportedWithTheFixedMessageAndNeverStops(t *testing.T) {
	reaper := newFakeOutboxReaper()
	reaper.err = errors.New("dial tcp 10.0.0.1:5432: password=hunter2")
	messages := make(chan string, 8)
	stop := startCredentialOutboxReaper(context.Background(), reaper, nil, func(_ context.Context, message string, _ ...slog.Attr) {
		select {
		case messages <- message:
		default:
		}
	}, 10*time.Millisecond)
	defer func() { _ = stop() }()
	for i := 0; i < 2; i++ {
		select {
		case message := <-messages:
			if message != credentialOutboxReapFailureMessage || strings.Contains(message, "hunter2") {
				t.Fatalf("message = %q", message)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the failure was not reported")
		}
	}
}

func TestTheReaperLogsCountsWithFixedTextAndNoIdentifier(t *testing.T) {
	buffer := &syncBuffer{}
	logger := newSyncLogger(buffer)
	reaper := newFakeOutboxReaper()
	reaper.result = storagepostgres.ReapResult{Aggregates: 100, Rows: 230}
	stop := startCredentialOutboxReaper(context.Background(), reaper, logger, nil, 10*time.Millisecond)
	waitFirstReap(t, reaper)
	deadline := time.After(5 * time.Second)
	for !strings.Contains(buffer.String(), credentialOutboxReapFullMessage) {
		select {
		case <-deadline:
			t.Fatalf("lines missing: %s", buffer.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	out := buffer.String()
	for _, want := range []string{"credential outbox reap", "aggregates=100", "rows=230", "source=reaper", credentialOutboxReapFullMessage} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}
	for _, banned := range []string{"cred_", "acr_credential", "Bearer", "token"} {
		if strings.Contains(out, banned) {
			t.Errorf("log carries %q: %s", banned, out)
		}
	}
}

func TestAShortBatchLogsCountsButNoFullLine(t *testing.T) {
	buffer := &syncBuffer{}
	reaper := newFakeOutboxReaper()
	reaper.result = storagepostgres.ReapResult{Aggregates: 3, Rows: 7}
	stop := startCredentialOutboxReaper(context.Background(), reaper, newSyncLogger(buffer), nil, 10*time.Millisecond)
	waitFirstReap(t, reaper)
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(buffer.String(), "aggregates=3"); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("counts line missing: %s", buffer.String())
		}
	}
	_ = stop()
	if strings.Contains(buffer.String(), credentialOutboxReapFullMessage) {
		t.Fatalf("a batch under its limit logs no full line: %s", buffer.String())
	}
}

func TestAnIdleReapTickLogsNothing(t *testing.T) {
	buffer := &syncBuffer{}
	reaper := newFakeOutboxReaper()
	stop := startCredentialOutboxReaper(context.Background(), reaper, newSyncLogger(buffer), nil, 10*time.Millisecond)
	waitFirstReap(t, reaper)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "" {
		t.Fatalf("an idle tick logged: %s", got)
	}
}

func TestTheReaperIsOffUnlessThePublisherIsConfigured(t *testing.T) {
	if got := buildCredentialOutboxReaper(config.Config{}, newFakeOutboxReaper()); got != nil {
		t.Fatal("the reaper runs with ACR_AUTH_PROJECTION_URL unset: rows are only published while the publisher runs")
	}
	cfg := config.Config{AuthProjection: config.AuthProjectionConfig{URL: "https://auth.example.test"}}
	if got := buildCredentialOutboxReaper(cfg, newFakeOutboxReaper()); got == nil {
		t.Fatal("the reaper does not run with the publisher configured")
	}
}

func TestTheReaperRetentionIsSevenDaysAndTheTickIsHourly(t *testing.T) {
	if credentialOutboxRetention != 7*24*time.Hour || defaultCredentialOutboxReapInterval != time.Hour || defaultCredentialOutboxReapBatchLimit != 100 {
		t.Fatalf("retention %v interval %v batch %d", credentialOutboxRetention, defaultCredentialOutboxReapInterval, defaultCredentialOutboxReapBatchLimit)
	}
}

func newSyncLogger(buffer *syncBuffer) *slog.Logger {
	return slog.New(telemetryscrub.Handler(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

func waitFirstReap(t *testing.T, reaper *fakeOutboxReaper) {
	t.Helper()
	select {
	case <-reaper.limits:
	case <-time.After(3 * time.Second):
		t.Fatal("the reaper never ticked")
	}
}
