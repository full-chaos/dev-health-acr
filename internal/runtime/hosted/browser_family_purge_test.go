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
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
)

// CHAOS-12140: the browser family purge loop, without a database.

type fakeBrowserFamilyPurgeCall struct {
	grace time.Duration
	limit int
}

// fakeBrowserFamilyPurger stands in for *storagepostgres.BrowserRefreshStore.
type fakeBrowserFamilyPurger struct {
	mu             sync.Mutex
	calls          []fakeBrowserFamilyPurgeCall
	result         storagepostgres.BrowserFamilyPurgeResult
	err            error
	remaining      storagepostgres.BrowserFamilyPurgeRemaining
	remainingErr   error
	remainingCalls []fakeBrowserFamilyPurgeCall
}

func (f *fakeBrowserFamilyPurger) PurgeEndedFamilies(_ context.Context, _ time.Time, grace time.Duration, limit int) (storagepostgres.BrowserFamilyPurgeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeBrowserFamilyPurgeCall{grace: grace, limit: limit})
	return f.result, f.err
}

func (f *fakeBrowserFamilyPurger) CountEndedFamiliesRemaining(_ context.Context, _ time.Time, grace time.Duration, limit int) (storagepostgres.BrowserFamilyPurgeRemaining, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remainingCalls = append(f.remainingCalls, fakeBrowserFamilyPurgeCall{grace: grace, limit: limit})
	return f.remaining, f.remainingErr
}

func browserClassTestConfig() config.Config {
	cfg := oauthConfiguredTestConfig()
	cfg.OAuthBrowserClients = []string{"https://www.example.test/acr/client-metadata.json"}
	cfg.OAuthBrowserFamilyPurgeGrace = 30 * time.Hour
	return cfg
}

// Without a browser client the purge never runs, so a runtime role that was
// never granted DELETE on acr.client_credentials or acr.browser_token_families
// still starts -- the OAuth login alone does not turn it on.
func TestStartConfiguredBrowserFamilyPurge_withoutABrowserClientNeverPurges(t *testing.T) {
	for name, cfg := range map[string]config.Config{"nothing configured": {}, "OAuth without a browser client": oauthConfiguredTestConfig()} {
		purger := &fakeBrowserFamilyPurger{err: errors.New("permission denied for table client_credentials")}
		closeLoop, err := startConfiguredBrowserFamilyPurge(context.Background(), cfg, purger, nil)
		if err != nil || closeLoop == nil {
			t.Fatalf("%s: err = %v, closer present = %t; want no error and a no-op closer", name, err, closeLoop != nil)
		}
		if err := closeLoop(); err != nil {
			t.Fatalf("%s: no-op closer error = %v", name, err)
		}
		if len(purger.calls) != 0 {
			t.Fatalf("%s: purge ran %d time(s); want 0", name, len(purger.calls))
		}
	}
}

// With a browser client (a metadata-document client, or the development
// client alone) the initial purge runs before startup returns, with the
// configured grace and the bounded batch.
func TestStartConfiguredBrowserFamilyPurge_browserClientPurgesAtStartupWithTheGrace(t *testing.T) {
	dev := oauthConfiguredTestConfig()
	dev.OAuthDevBrowserRedirectURIs = []string{"http://127.0.0.1:3000/acr/callback"}
	dev.OAuthBrowserFamilyPurgeGrace = 2 * time.Hour
	for name, tc := range map[string]struct {
		cfg   config.Config
		grace time.Duration
	}{
		"metadata-document client": {browserClassTestConfig(), 30 * time.Hour},
		"development client alone": {dev, 2 * time.Hour},
	} {
		purger := &fakeBrowserFamilyPurger{}
		closeLoop, err := startConfiguredBrowserFamilyPurge(context.Background(), tc.cfg, purger, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		purger.mu.Lock()
		calls := append([]fakeBrowserFamilyPurgeCall(nil), purger.calls...)
		purger.mu.Unlock()
		if err := closeLoop(); err != nil {
			t.Fatal(err)
		}
		want := fakeBrowserFamilyPurgeCall{grace: tc.grace, limit: defaultBrowserFamilyPurgeBatchLimit}
		if len(calls) != 1 || calls[0] != want {
			t.Fatalf("%s: startup purge calls = %#v, want exactly [%#v]", name, calls, want)
		}
	}
}

func TestStartConfiguredBrowserFamilyPurge_missingDeleteGrantFailsStartup(t *testing.T) {
	wantErr := errors.New("permission denied for table browser_token_families")
	purger := &fakeBrowserFamilyPurger{err: wantErr}
	closeLoop, err := startConfiguredBrowserFamilyPurge(context.Background(), browserClassTestConfig(), purger, nil)
	if !errors.Is(err, wantErr) || closeLoop != nil {
		t.Fatalf("error = %v, closer present = %t; want %v and no closer", err, closeLoop != nil, wantErr)
	}
}

// A zero grace (a Config built without config.Load) is refused by the store
// before any statement, so it fails startup instead of purging every ended
// family at once.
func TestStartConfiguredBrowserFamilyPurge_zeroGraceFailsStartupBeforeAnyDelete(t *testing.T) {
	database := sql.OpenDB(idleConnector{})
	t.Cleanup(func() { _ = database.Close() })
	audit, err := storagepostgres.NewAuditStore(database)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storagepostgres.NewBrowserRefreshStore(database, audit)
	if err != nil {
		t.Fatal(err)
	}
	cfg := browserClassTestConfig()
	cfg.OAuthBrowserFamilyPurgeGrace = 0
	closeLoop, err := startConfiguredBrowserFamilyPurge(context.Background(), cfg, store, nil)
	if err == nil || closeLoop != nil {
		t.Fatalf("zero grace: err = %v, closer present = %t; want a startup error", err, closeLoop != nil)
	}
}

// Every tick logs exactly one count line, zeros included (the heartbeat); a
// tick that skipped rows differs from an empty one only by its remaining
// counts; a failed purge logs what it deleted and is not probed.
func TestBrowserFamilyPurgeFunc_logsOneCountLineOnEveryTick(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	purger := &fakeBrowserFamilyPurger{}
	purge := browserFamilyPurgeFunc(purger, 3*time.Hour, logger)
	oneLine := func(want ...string) {
		t.Helper()
		line := strings.TrimSpace(logs.String())
		if strings.Count(line, "\n") != 0 || !strings.Contains(line, `msg="browser family purge"`) || !strings.Contains(line, "level=INFO") {
			t.Fatalf("want exactly one Info browser family purge line, got %q", line)
		}
		for _, fragment := range want {
			if !strings.Contains(line, fragment) {
				t.Fatalf("line %q lacks %q", line, fragment)
			}
		}
		logs.Reset()
	}

	if total, err := purge(context.Background(), time.Now(), 7); total != 0 || err != nil {
		t.Fatalf("empty tick: total=%d err=%v", total, err)
	}
	oneLine("member_rows=0 families=0 member_rows_remaining=0 families_remaining=0")

	purger.remaining = storagepostgres.BrowserFamilyPurgeRemaining{Credentials: 4, Families: 2}
	if total, err := purge(context.Background(), time.Now(), 7); total != 0 || err != nil {
		t.Fatalf("skipped tick: total=%d err=%v", total, err)
	}
	oneLine("member_rows=0 families=0 member_rows_remaining=4 families_remaining=2")

	purger.result, purger.remaining = storagepostgres.BrowserFamilyPurgeResult{Credentials: 5, Families: 3}, storagepostgres.BrowserFamilyPurgeRemaining{}
	if total, err := purge(context.Background(), time.Now(), 7); total != 8 || err != nil {
		t.Fatalf("delete tick: total=%d err=%v", total, err)
	}
	oneLine("member_rows=5 families=3 member_rows_remaining=0 families_remaining=0")
	want := fakeBrowserFamilyPurgeCall{grace: 3 * time.Hour, limit: 7}
	for _, call := range append(append([]fakeBrowserFamilyPurgeCall(nil), purger.calls...), purger.remainingCalls...) {
		if call != want {
			t.Fatalf("call %#v, want the purge's own grace and batch limit %#v", call, want)
		}
	}

	wantErr := errors.New("family statement failed")
	purger.result, purger.err = storagepostgres.BrowserFamilyPurgeResult{Credentials: 2}, wantErr
	probes := len(purger.remainingCalls)
	total, err := purge(context.Background(), time.Now(), 7)
	if total != 2 || !errors.Is(err, wantErr) {
		t.Fatalf("partial-failure tick: total=%d err=%v", total, err)
	}
	line := logs.String()
	oneLine("member_rows=2 families=0")
	if strings.Contains(line, "remaining") || len(purger.remainingCalls) != probes {
		t.Fatalf("a failed purge was probed or logged remaining: probes %d -> %d, line %q", probes, len(purger.remainingCalls), line)
	}

	purger.result, purger.err, purger.remainingErr = storagepostgres.BrowserFamilyPurgeResult{Families: 1}, nil, errors.New("count failed")
	if total, err := purge(context.Background(), time.Now(), 7); total != 1 || err == nil {
		t.Fatalf("failed probe: total=%d err=%v; want 1 and the probe error", total, err)
	}
	line = logs.String()
	oneLine("member_rows=0 families=1")
	if strings.Contains(line, "remaining") {
		t.Fatalf("failed probe logged remaining: %q", line)
	}
}
