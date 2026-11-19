package directread

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"maps"
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
		!strings.Contains(ws[0], "pinned_ops=67") || !strings.Contains(ws[0], "served_ops=67") {
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
// 2026-09-30 00:21Z at ops cb758a29 (HTTP 200, 6916 bytes). Against the
// catalogue pinned at ops 5c9a3d32 it must decode and show exactly the drift a
// re-vendor clears: the schema digest, the document digests that changed, and
// the operation the old build did not serve.
func TestRegistryWatch_older_prod_registry_golden_drifts_from_the_new_pin(t *testing.T) {
	golden, err := os.ReadFile("testdata/query_registry_16dc07c9.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newRegistryFixture(t)
	f.set(200, golden)
	w, _, buf := newWatch(t, f, nil)
	w.Start()
	w.Wait()
	out := buf.String()
	if !strings.Contains(out, "registry digest drift") {
		t.Fatalf("no schema digest drift line: %s", out)
	}
	changed := []string{"aiAttributedPrs", "aiAttributionOverview", "aiGovernanceSummary", "compoundingRisk", "dataHealthIdentity", "testopsRisk", "aiImpactSummary", "aiOpportunities", "aiWorkflowDrilldown", "capacityForecast", "home", "hotspots", "improveOpportunities", "investmentBreakdown", "investmentFull", "operatingReview", "reviewEdges", "testOpsCoverage", "testOpsPipeline", "throughputForecast"}
	for _, op := range changed {
		if !strings.Contains(out, "operation="+op+" ") || !strings.Contains(out, "reason=changed") {
			t.Errorf("no changed drift line for %s: %s", op, out)
		}
	}
	if !strings.Contains(out, "operation=capacityCompletionDistribution ") || !strings.Contains(out, "reason=missing_in_served") {
		t.Errorf("no missing_in_served line for the new operation: %s", out)
	}
	for _, op := range []string{"coverageBaselines", "coverageScopeBaseline", "investmentBreakdownRepository", "investmentBreakdownTeam", "investmentEvidenceQuality", "sourceHealth", "teamWorkItemBreakdown", "testopsJobFailures", "testopsTestWindow"} {
		if !strings.Contains(out, "operation="+op+" ") {
			t.Errorf("no missing_in_served line for %s: %s", op, out)
		}
	}
	if !strings.Contains(out, "operation=testOpsTest ") || !strings.Contains(out, "reason=missing_in_pinned") {
		t.Errorf("no missing_in_pinned line for the retired testOpsTest: %s", out)
	}
	if got := strings.Count(out, "registry operation drift"); got != len(changed)+11 {
		t.Errorf("%d operation drift lines, want %d: %s", got, len(changed)+11, out)
	}
}

// Fixture: the GET /registry body of ops b0f621e5 (the query-api prod serves), taken from the
// production handler constructor (newQueryHandler, as buildQueryRoute builds it, with the schema
// digest digest.Schema(schemav1.SDL)): HTTP 200. The catalogue pinned from that commit must match
// it with zero drift.
func TestRegistryWatch_registry_of_the_vendored_commit_matches_pin(t *testing.T) {
	body, err := os.ReadFile("testdata/query_registry_b0f621e5.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	w, cat, buf := newWatch(t, f, nil)
	w.Start()
	w.Wait()
	const want = "sha256:c3340e3a18800084eb8eccee7c7bb916153cc5ef6a6c4ae07ce35e85d0c8a4d7"
	if got := cat.StampedSchemaDigest(); got != want || cat.SchemaDigest() != want {
		t.Fatalf("stamp %s pinned %s, want %s", got, cat.SchemaDigest(), want)
	}
	if ws := warns(buf.String()); len(ws) != 0 {
		t.Fatalf("drift against the vendored commit's registry: %q", ws)
	}
	if !strings.Contains(buf.String(), "registry digest match") || !strings.Contains(buf.String(), "operations=67") {
		t.Fatalf("no match line with 67 ops: %s", buf)
	}
}

// driftReasons maps each "registry operation drift" line of log to its reason.
func driftReasons(t *testing.T, log string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, l := range strings.Split(log, "\n") {
		if !strings.Contains(l, "registry operation drift") {
			continue
		}
		var op, reason string
		for _, f := range strings.Fields(l) {
			if v, ok := strings.CutPrefix(f, "operation="); ok {
				op = v
			}
			if v, ok := strings.CutPrefix(f, "reason="); ok {
				reason = v
			}
		}
		if _, dup := out[op]; dup || op == "" || reason == "" {
			t.Fatalf("drift line without one operation and reason: %s", l)
		}
		out[op] = reason
	}
	return out
}

// The eight operations whose registry row moved between ops 0b763a85 and ops f67b568a: three
// scoped documents selecting sizedUnitCount, three testops documents selecting filterEmptyReason,
// testopsTestWindow registered and testOpsTest retired.
func registryDrift0b763a85ToF67b568a(fromOldPin bool) map[string]string {
	retired, added := "missing_in_served", "missing_in_pinned"
	if !fromOldPin {
		retired, added = added, retired
	}
	return map[string]string{
		"investmentBreakdownRepository": "changed",
		"investmentBreakdownTeam":       "changed",
		"teamWorkItemBreakdown":         "changed",
		"testOpsCoverage":               "changed",
		"testOpsPipeline":               "changed",
		"testopsJobFailures":            "changed",
		"testOpsTest":                   retired,
		"testopsTestWindow":             added,
	}
}

// The prod fact this pin clears: the catalogue pinned at ops 0b763a85 against the registry a
// query-api built from ops f67b568a serves logs one schema digest drift warning and the eight
// operation drift lines, and stamps the served digest.
func TestRegistryWatch_previous_0b763a85_pin_against_the_f67b568a_registry_shows_digest_drift_and_eight_operations(t *testing.T) {
	oldPin, err := os.ReadFile("testdata/operations_0b763a85.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("testdata/query_registry_f67b568a.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalogue(oldPin)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.SchemaDigest(); got != "sha256:6d40791c0e2a28c2e0284a3330d1b30ddc3e9b24d397d9ceb237c398d4cd106c" {
		t.Fatalf("old pin digest %s", got)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	var buf bytes.Buffer
	var mu sync.Mutex
	h := slog.NewTextHandler(&lockedWriter{&mu, &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})
	w, err := NewRegistryWatch(RegistryWatchConfig{Catalogue: cat, BaseURL: f.srv.URL, Logger: slog.New(h), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	w.Wait()
	out := buf.String()
	ws := warns(out)
	if len(ws) != 9 || !strings.Contains(ws[0], "registry digest drift") || !strings.Contains(ws[0], "served_digest=sha256:76218f99d03ba1ca80c91c8d45c1ecf38570f2c139d27593415734c096fb6ec7") || !strings.Contains(ws[0], "drifted_ops=8") {
		t.Fatalf("want the digest drift line (served f67b568a, drifted_ops=8) + 8 operation drift lines: %q", ws)
	}
	if got, want := driftReasons(t, out), registryDrift0b763a85ToF67b568a(true); !maps.Equal(got, want) {
		t.Fatalf("drift %v, want %v", got, want)
	}
	if got := cat.StampedSchemaDigest(); got != "sha256:76218f99d03ba1ca80c91c8d45c1ecf38570f2c139d27593415734c096fb6ec7" {
		t.Fatalf("stamped %s, want the served digest", got)
	}
}

// The same eight operations seen from the other side: the registry ops 0b763a85 served against
// the catalogue pinned now.
func TestRegistryWatch_0b763a85_served_registry_against_the_new_pin_shows_digest_drift_and_eight_operations(t *testing.T) {
	body, err := os.ReadFile("testdata/query_registry_0b763a85.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	w, _, buf := newWatch(t, f, nil)
	w.Start()
	w.Wait()
	out := buf.String()
	if ws := warns(out); len(ws) != 12 || !strings.Contains(ws[0], "registry digest drift") {
		t.Fatalf("want digest drift + 11 operation drift warnings: %q", ws)
	}
	want := registryDrift0b763a85ToF67b568a(false)
	maps.Copy(want, registryDriftF67b568aToB0f621e5())
	if got := driftReasons(t, out); !maps.Equal(got, want) {
		t.Fatalf("drift %v, want %v", got, want)
	}
}

// The three operations whose registry row moved between ops f67b568a and ops b0f621e5: the
// throughputForecast document selecting backlogState, backlogAsOfDay and grainCount, and the two
// not-served documents operatingReview and testopsRisk.
func registryDriftF67b568aToB0f621e5() map[string]string {
	return map[string]string{"operatingReview": "changed", "testopsRisk": "changed", "throughputForecast": "changed"}
}

// The catalogue pinned at ops f67b568a (the previous pin) against the registry ops b0f621e5
// serves: one schema digest drift warning and the three changed documents.
func TestRegistryWatch_previous_f67b568a_pin_against_the_b0f621e5_registry_shows_digest_drift_and_three_operations(t *testing.T) {
	oldPin, err := os.ReadFile("testdata/operations_f67b568a.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("testdata/query_registry_b0f621e5.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalogue(oldPin)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.SchemaDigest(); got != "sha256:76218f99d03ba1ca80c91c8d45c1ecf38570f2c139d27593415734c096fb6ec7" {
		t.Fatalf("old pin digest %s", got)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	var buf bytes.Buffer
	var mu sync.Mutex
	h := slog.NewTextHandler(&lockedWriter{&mu, &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})
	w, err := NewRegistryWatch(RegistryWatchConfig{Catalogue: cat, BaseURL: f.srv.URL, Logger: slog.New(h), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	w.Wait()
	out := buf.String()
	ws := warns(out)
	if len(ws) != 4 || !strings.Contains(ws[0], "registry digest drift") || !strings.Contains(ws[0], "served_digest=sha256:c3340e3a18800084eb8eccee7c7bb916153cc5ef6a6c4ae07ce35e85d0c8a4d7") || !strings.Contains(ws[0], "drifted_ops=3") {
		t.Fatalf("want the digest drift line (served b0f621e5, drifted_ops=3) + 3 operation drift lines: %q", ws)
	}
	if got, want := driftReasons(t, out), registryDriftF67b568aToB0f621e5(); !maps.Equal(got, want) {
		t.Fatalf("drift %v, want %v", got, want)
	}
}

// The same three operations from the other side: the registry ops f67b568a served (the prod
// fact before b0f621e5) against the catalogue pinned now.
func TestRegistryWatch_f67b568a_served_registry_against_the_new_pin_shows_digest_drift_and_three_operations(t *testing.T) {
	body, err := os.ReadFile("testdata/query_registry_f67b568a.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	w, _, buf := newWatch(t, f, nil)
	w.Start()
	w.Wait()
	out := buf.String()
	if ws := warns(out); len(ws) != 4 || !strings.Contains(ws[0], "registry digest drift") {
		t.Fatalf("want digest drift + 3 operation drift warnings: %q", ws)
	}
	if got, want := driftReasons(t, out), registryDriftF67b568aToB0f621e5(); !maps.Equal(got, want) {
		t.Fatalf("drift %v, want %v", got, want)
	}
}

// The registry ops 51652ba7 served (the prod fact until ops 0b763a85), against
// the catalogue pinned now: the schema digest moved (evidenceQualityStats
// gained uncategorizedCount and sizedUnitCount), investmentBreakdown and three
// testops documents changed text, the scoped investment documents
// (CHAOS-12698), teamWorkItemBreakdown and testopsTestWindow are not served
// yet, and the retired testOpsTest is not pinned.
func TestRegistryWatch_51652ba7_served_registry_against_the_new_pin_shows_digest_drift_and_three_operations(t *testing.T) {
	body, err := os.ReadFile("testdata/query_registry_51652ba7.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	w, _, buf := newWatch(t, f, nil)
	w.Start()
	w.Wait()
	out := buf.String()
	if ws := warns(out); len(ws) != 13 || !strings.Contains(ws[0], "registry digest drift") {
		t.Fatalf("want digest drift + 12 operation drift warnings: %q", ws)
	}
	for _, want := range []string{
		"operation=operatingReview ", "operation=testopsRisk ", "operation=throughputForecast ",
		"operation=investmentBreakdown ", "operation=testOpsCoverage ", "operation=testOpsPipeline ", "operation=testopsJobFailures ", "reason=changed",
		"operation=investmentBreakdownRepository ", "operation=investmentBreakdownTeam ", "operation=teamWorkItemBreakdown ", "operation=testopsTestWindow ", "reason=missing_in_served",
		"operation=testOpsTest ", "reason=missing_in_pinned",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no drift line with %q: %s", want, out)
		}
	}
}

// The prod fact before the roll: the catalogue pinned at ops 5c9a3d32
// against the registry ops 7a0a22a9 serves logs one schema digest drift
// warning plus the seven operations whose document text moved.
func TestRegistryWatch_previous_pin_against_the_served_registry_shows_the_digest_drift_only(t *testing.T) {
	oldPin, err := os.ReadFile("testdata/operations_5c9a3d32.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("testdata/query_registry_7a0a22a9.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalogue(oldPin)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.SchemaDigest(); got != "sha256:54a0f7d6ee428bc2f8c8ef6329680c8efeb4be94a7b41bdc12655006069335e4" {
		t.Fatalf("old pin digest %s", got)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	var buf bytes.Buffer
	var mu sync.Mutex
	h := slog.NewTextHandler(&lockedWriter{&mu, &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})
	w, err := NewRegistryWatch(RegistryWatchConfig{Catalogue: cat, BaseURL: f.srv.URL, Logger: slog.New(h), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	w.Wait()
	ws := warns(buf.String())
	if len(ws) != 8 {
		t.Fatalf("%d warnings, want 8 (digest drift + 7 changed documents): %q", len(ws), ws)
	}
	if !strings.Contains(ws[0], "registry digest drift") || !strings.Contains(ws[0], "sha256:608a6270255afcbd86fde5889e67102b3cb22b49280c77a53102269e6f2d60a6") {
		t.Errorf("no schema digest drift line: %s", ws[0])
	}
}

// The registry ops 5c9a3d32 served, against the catalogue pinned now: the same
// single difference seen from the other side. This is the state the new pods
// log until the ops release that serves the new schema is rolled.
func TestRegistryWatch_previous_served_registry_against_the_new_pin_shows_the_digest_drift_only(t *testing.T) {
	body, err := os.ReadFile("testdata/query_registry_5c9a3d32.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	w, _, buf := newWatch(t, f, nil)
	w.Start()
	w.Wait()
	ws := warns(buf.String())
	digest := 0
	for _, w := range ws {
		if strings.Contains(w, "registry digest drift") {
			digest++
		}
	}
	if digest != 1 || len(ws) != 20 {
		t.Fatalf("want one digest drift warning + 19 operation drift lines (14 changed documents, throughputForecast among them; 4 documents missing in served: the two scoped investment documents, teamWorkItemBreakdown and testopsTestWindow; the retired testOpsTest missing in pinned): %q", ws)
	}
}

// The registry ops 754d86cf served (pin 21), against the catalogue pinned now:
// the schema digest moved (deltaPct nullable, rateState added), the documents
// whose registered text moved since (home, operatingReview and the rest
// listed below) drift, the scoped investment documents (ops 0b763a85),
// teamWorkItemBreakdown and testopsTestWindow are missing in served, and the
// retired testOpsTest is missing in pinned.
func TestRegistryWatch_pin21_served_registry_against_the_new_pin_shows_digest_drift_and_three_changed_documents(t *testing.T) {
	body, err := os.ReadFile("testdata/query_registry_754d86cf.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	w, _, buf := newWatch(t, f, nil)
	w.Start()
	w.Wait()
	ws := warns(buf.String())
	if len(ws) != 16 {
		t.Fatalf("want digest drift + 15 operation drift warnings: %q", ws)
	}
	if !strings.Contains(buf.String(), "registry digest drift") {
		t.Errorf("no schema digest drift line: %s", buf)
	}
	for _, op := range []string{"compoundingRisk", "home", "investmentBreakdown", "investmentBreakdownRepository", "investmentBreakdownTeam", "investmentFull", "operatingReview", "teamWorkItemBreakdown", "testOpsCoverage", "testOpsPipeline", "testOpsTest", "testopsJobFailures", "testopsRisk", "testopsTestWindow", "throughputForecast"} {
		if !strings.Contains(buf.String(), "operation="+op+" ") {
			t.Errorf("no drift line for %s", op)
		}
	}
}

// The registry ops 5376c7c1 served (the prod fact until ops 51652ba7), against
// the catalogue pinned now: the schema digest moved (home and investmentFull
// gained fields), seven operations changed text: home, investmentFull,
// investmentBreakdown (ops 0b763a85) and the four that are not served
// (operatingReview, testOpsCoverage, testOpsPipeline, testopsJobFailures), the
// scoped investment documents, teamWorkItemBreakdown and testopsTestWindow are
// missing in served, and the retired testOpsTest is missing in pinned.
func TestRegistryWatch_5376c7c1_served_registry_against_the_new_pin_shows_digest_drift_and_five_changed_documents(t *testing.T) {
	body, err := os.ReadFile("testdata/query_registry_5376c7c1.json")
	if err != nil {
		t.Fatal(err)
	}
	f := newRegistryFixture(t)
	f.set(200, body)
	w, _, buf := newWatch(t, f, nil)
	w.Start()
	w.Wait()
	ws := warns(buf.String())
	if len(ws) != 15 {
		t.Fatalf("want digest drift + 14 operation drift warnings: %q", ws)
	}
	if !strings.Contains(buf.String(), "registry digest drift") {
		t.Errorf("no schema digest drift line: %s", buf)
	}
	for _, op := range []string{"home", "investmentBreakdown", "investmentBreakdownRepository", "investmentBreakdownTeam", "investmentFull", "operatingReview", "teamWorkItemBreakdown", "testOpsCoverage", "testOpsPipeline", "testOpsTest", "testopsJobFailures", "testopsRisk", "testopsTestWindow", "throughputForecast"} {
		if !strings.Contains(buf.String(), "operation="+op+" ") {
			t.Errorf("no drift line for %s", op)
		}
	}
}
