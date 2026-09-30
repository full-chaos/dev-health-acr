package directread

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type registryFixture struct {
	mu     sync.Mutex
	status int
	body   []byte
	hits   atomic.Int32
	hdr    http.Header
	srv    *httptest.Server
}

func newRegistryFixture(t *testing.T) *registryFixture {
	t.Helper()
	f := &registryFixture{status: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		f.hdr = r.Header.Clone()
		status, body := f.status, f.body
		f.mu.Unlock()
		if r.URL.Path != "/registry" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *registryFixture) set(status int, body []byte) {
	f.mu.Lock()
	f.status, f.body = status, body
	f.mu.Unlock()
}

func servedBody(t *testing.T, cat *Catalogue, digest string, mutate func(map[string]string)) []byte {
	t.Helper()
	ops := cat.pinnedDocumentDigests()
	if mutate != nil {
		mutate(ops)
	}
	var doc registryServed
	doc.SchemaDigest = digest
	for name, d := range ops {
		doc.Operations = append(doc.Operations, struct {
			Operation      string `json:"operation"`
			DocumentDigest string `json:"document_digest"`
		}{name, d})
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newWatch(t *testing.T, f *registryFixture, now func() time.Time) (*RegistryWatch, *Catalogue, *bytes.Buffer) {
	t.Helper()
	cat, err := LoadCatalogue(EmbeddedCatalogueJSON())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	var mu sync.Mutex
	h := slog.NewTextHandler(&lockedWriter{&mu, &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})
	w, err := NewRegistryWatch(RegistryWatchConfig{Catalogue: cat, BaseURL: f.srv.URL, Logger: slog.New(h), Now: now, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return w, cat, &buf
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func warns(log string) []string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "level=WARN") {
			out = append(out, l)
		}
	}
	return out
}

const servedDrift = "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111"

func TestRegistryWatch_match_stamps_served_and_logs_info_with_both_digests(t *testing.T) {
	f := newRegistryFixture(t)
	w, cat, buf := newWatch(t, f, nil)
	f.set(200, servedBody(t, cat, cat.SchemaDigest(), nil))
	w.Start()
	w.Wait()
	if got := cat.StampedSchemaDigest(); got != cat.SchemaDigest() {
		t.Fatalf("stamp = %s", got)
	}
	if len(warns(buf.String())) != 0 {
		t.Fatalf("unexpected warn: %s", buf)
	}
	if !strings.Contains(buf.String(), "registry digest match") || strings.Count(buf.String(), cat.SchemaDigest()) < 2 {
		t.Fatalf("match line lacks both digests: %s", buf)
	}
	if h := f.hdr; h.Get("Authorization") != "" || h.Get(HeaderInternalOrgID) != "" || h.Get(HeaderInternalRole) != "" {
		t.Fatalf("registry fetch carried auth/identity headers: %v", h)
	}
}

func TestRegistryWatch_digest_drift_stamps_served_and_warns_once(t *testing.T) {
	f := newRegistryFixture(t)
	w, cat, buf := newWatch(t, f, nil)
	f.set(200, servedBody(t, cat, servedDrift, nil))
	w.Start()
	w.Wait()
	if got := cat.StampedSchemaDigest(); got != servedDrift {
		t.Fatalf("stamp = %s, want served %s", got, servedDrift)
	}
	ws := warns(buf.String())
	if len(ws) != 1 || !strings.Contains(ws[0], "registry digest drift") ||
		!strings.Contains(ws[0], cat.SchemaDigest()) || !strings.Contains(ws[0], servedDrift) ||
		!strings.Contains(ws[0], "pinned_ops=58") || !strings.Contains(ws[0], "served_ops=58") {
		t.Fatalf("warns = %q", ws)
	}
	// Same state again: no repeat line.
	w.mu.Lock()
	w.inflight = true
	w.mu.Unlock()
	w.refresh()
	if len(warns(buf.String())) != 1 {
		t.Fatalf("repeat warn: %s", buf)
	}
}

func TestRegistryWatch_per_operation_drift_one_warn_each(t *testing.T) {
	f := newRegistryFixture(t)
	w, cat, buf := newWatch(t, f, nil)
	f.set(200, servedBody(t, cat, cat.SchemaDigest(), func(m map[string]string) {
		m["busFactor"] = "deadbeef" // changed
		delete(m, "home")           // missing in served
		m["brandNewOp"] = "cafe"    // missing in pinned
	}))
	w.Start()
	w.Wait()
	ws := warns(buf.String())
	if len(ws) != 3 {
		t.Fatalf("want 3 per-op warns and no digest warn, got %d: %q", len(ws), ws)
	}
	joined := strings.Join(ws, "\n")
	for _, want := range []string{"operation=busFactor", "reason=changed", "operation=home", "reason=missing_in_served", "operation=brandNewOp", "reason=missing_in_pinned"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "registry digest drift") {
		t.Fatalf("digest warn on matching digest: %s", joined)
	}
}

func TestRegistryWatch_fetch_failures_fall_back_to_pinned_and_warn_on_first_failure(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		class  string
	}{
		{"http_500", 500, "boom", "http_status"},
		{"garbage", 200, "not json", "decode"},
		{"empty_digest", 200, `{"operations":[]}`, "decode"},
		{"oversize", 200, strings.Repeat("x", MaxRegistryResponseBytes+10), "too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRegistryFixture(t)
			w, cat, buf := newWatch(t, f, nil)
			f.set(tc.status, []byte(tc.body))
			w.Start()
			w.Wait()
			if got := cat.StampedSchemaDigest(); got != cat.SchemaDigest() {
				t.Fatalf("stamp = %s, want pinned", got)
			}
			ws := warns(buf.String())
			if len(ws) != 1 || !strings.Contains(ws[0], "registry check failed") || !strings.Contains(ws[0], "class="+tc.class) {
				t.Fatalf("warns = %q", ws)
			}
		})
	}
	t.Run("transport", func(t *testing.T) {
		f := newRegistryFixture(t)
		w, cat, buf := newWatch(t, f, nil)
		f.srv.Close()
		w.Start()
		w.Wait()
		if cat.StampedSchemaDigest() != cat.SchemaDigest() {
			t.Fatal("not pinned")
		}
		if ws := warns(buf.String()); len(ws) != 1 || !strings.Contains(ws[0], "class=transport") {
			t.Fatalf("warns = %q", ws)
		}
	})
}

func TestRegistryWatch_recovers_after_failure_and_falls_back_after_success_then_failure(t *testing.T) {
	f := newRegistryFixture(t)
	clock := time.Unix(1000, 0)
	var cm sync.Mutex
	now := func() time.Time { cm.Lock(); defer cm.Unlock(); return clock }
	adv := func(d time.Duration) { cm.Lock(); clock = clock.Add(d); cm.Unlock() }
	w, cat, buf := newWatch(t, f, now)
	f.set(500, nil)
	w.Start()
	w.Wait()
	f.set(200, servedBody(t, cat, servedDrift, nil))
	// Inside the TTL: no new fetch, still pinned.
	adv(time.Minute)
	if got := w.StampedDigest(); got != cat.SchemaDigest() || f.hits.Load() != 1 {
		t.Fatalf("within ttl: stamp=%s hits=%d", got, f.hits.Load())
	}
	// Past the TTL: one background refresh (single flight), then served.
	adv(RegistryWatchTTL)
	for i := 0; i < 5; i++ {
		w.StampedDigest()
	}
	w.Wait()
	if f.hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2 (single flight)", f.hits.Load())
	}
	if got := w.StampedDigest(); got != servedDrift {
		t.Fatalf("after recovery stamp = %s", got)
	}
	// Served, then the fetch fails: back to pinned with a Warn.
	f.set(503, nil)
	adv(RegistryWatchTTL + time.Second)
	w.StampedDigest()
	w.Wait()
	if got := w.StampedDigest(); got != cat.SchemaDigest() {
		t.Fatalf("after failure stamp = %s, want pinned", got)
	}
	if n := strings.Count(buf.String(), "registry check failed"); n != 2 {
		t.Fatalf("failure warns = %d (first failure + failure after success), want 2: %s", n, buf)
	}
}

func TestRegistryWatch_no_watch_stamps_pinned(t *testing.T) {
	cat, err := LoadCatalogue(EmbeddedCatalogueJSON())
	if err != nil {
		t.Fatal(err)
	}
	if cat.StampedSchemaDigest() != cat.SchemaDigest() {
		t.Fatal("no watch must stamp pinned")
	}
}

func TestRegistryWatch_invalid_base_url(t *testing.T) {
	cat, _ := LoadCatalogue(EmbeddedCatalogueJSON())
	for _, u := range []string{"", "ftp://x", "http://u:p@h", "http://h?q=1"} {
		if _, err := NewRegistryWatch(RegistryWatchConfig{Catalogue: cat, BaseURL: u}); err == nil {
			t.Fatalf("accepted %q", u)
		}
	}
}

// Golden fixture: the live prod query-api GET /registry document captured
// 2026-09-30 00:21Z at ops cb758a29 (HTTP 200, 6916 bytes). The real served
// format must decode and match the re-pinned catalogue with zero drift.
func TestRegistryWatch_live_prod_registry_golden_matches_pin(t *testing.T) {
	golden, err := os.ReadFile("testdata/query_registry_cb758a29.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newRegistryFixture(t)
	f.set(200, golden)
	w, cat, buf := newWatch(t, f, nil)
	w.Start()
	w.Wait()
	const want = "sha256:dd83956f18b52a3acf89e73e1f25b0dcf25df706d90eca49c5b66f8b8994f779"
	if got := cat.StampedSchemaDigest(); got != want || cat.SchemaDigest() != want {
		t.Fatalf("stamp %s pinned %s, want %s", got, cat.SchemaDigest(), want)
	}
	if ws := warns(buf.String()); len(ws) != 0 {
		t.Fatalf("drift against the live document: %q", ws)
	}
	if !strings.Contains(buf.String(), "registry digest match") || !strings.Contains(buf.String(), "operations=58") {
		t.Fatalf("no match line with 58 ops: %s", buf)
	}
}
