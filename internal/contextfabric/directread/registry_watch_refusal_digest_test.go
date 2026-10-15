package directread_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const watchRefreshedDigest = "sha256:3333333333333333333333333333333333333333333333333333333333333333"

// swappableRegistry serves /registry with a digest the test can change, on a
// clock the test can advance past the watch TTL.
type swappableRegistry struct {
	digest atomic.Value
	offset atomic.Int64
	status atomic.Int64
	base   time.Time
	server *httptest.Server
}

func newSwappableRegistry(t *testing.T, initial string) *swappableRegistry {
	t.Helper()
	r := &swappableRegistry{base: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	r.digest.Store(initial)
	r.status.Store(http.StatusOK)
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if code := int(r.status.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_digest": r.digest.Load().(string), "operations": []any{}})
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *swappableRegistry) now() time.Time {
	return r.base.Add(time.Duration(r.offset.Load()))
}

func (r *swappableRegistry) watch(t *testing.T, cat *directread.Catalogue) *directread.RegistryWatch {
	t.Helper()
	w, err := directread.NewRegistryWatch(directread.RegistryWatchConfig{
		Catalogue: cat, BaseURL: r.server.URL, Logger: slog.New(slog.DiscardHandler),
		TTL: time.Minute, Now: r.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// landRefresh moves the registry to digest, expires the TTL and waits for the
// refresh it starts to finish: the watch now holds the new digest. It is called
// from inside a dispatch.
func (r *swappableRegistry) landRefresh(cat *directread.Catalogue, w *directread.RegistryWatch, digest string) {
	r.digest.Store(digest)
	r.offset.Add(int64(2 * time.Minute))
	cat.StampedSchemaDigest()
	w.Wait()
}

// A refresh that lands while the query service is answering must show in a
// refusal decided after that answer: a reply never reports a digest older than
// the last successful watch.
func TestPostDispatchRefusalOfRunOperationReportsADigestRefreshedDuringTheDispatch(t *testing.T) {
	cat, err := directread.LoadCatalogue(directread.EmbeddedCatalogueJSON())
	if err != nil {
		t.Fatal(err)
	}
	reg := newSwappableRegistry(t, watchServedDigest)
	watch := reg.watch(t, cat)
	watch.Start()
	watch.Wait()
	op, _ := cat.Lookup("hotspots")
	scope := op.Scope(directread.CallerRestricted)
	vars := opMerge(opMinimalVariables(t, op), scope.ForcedVariablePath, []any{opRepo(opRepoA)})
	rec := &opDigestRecorder{}
	h := newOpHarness(t, func(opRecorded) (int, string) {
		reg.landRefresh(cat, watch, watchRefreshedDigest)
		return 200, opRowsAnswer(scope.RowIDPaths[0], []any{opRepoB})
	}, opHarnessOptions{catalogue: cat, recorder: rec})
	resp := h.run(t, opRestrictedA(), op.Name, vars)
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalRowOutsideGrant {
		t.Fatalf("want row_outside_grant, got %+v", resp)
	}
	if resp.Source.SchemaDigest != watchRefreshedDigest {
		t.Fatalf("reply digest %s, want the digest refreshed during the dispatch %s", resp.Source.SchemaDigest, watchRefreshedDigest)
	}
	if len(rec.reads) != 1 || rec.reads[0].SchemaDigest != watchRefreshedDigest {
		t.Fatalf("read log %+v, want %s", rec.reads, watchRefreshedDigest)
	}
}

func TestPostDispatchRefusalOfGraphQLQueryReportsADigestRefreshedDuringTheDispatch(t *testing.T) {
	rec := &digestRecorder{}
	var reg *swappableRegistry
	var watch *directread.RegistryWatch
	var refresh func()
	h := newGQLHarness(t, gqlHarnessOptions{recorder: rec, ownCatalogue: true, fake: func(cfg *fakeMCPConfig) {
		cfg.RowID = func() string {
			refresh()
			return opRepoB
		}
	}})
	cat := h.policy.Catalogue()
	reg = newSwappableRegistry(t, watchServedDigest)
	watch = reg.watch(t, cat)
	refresh = func() { reg.landRefresh(cat, watch, watchRefreshedDigest) }
	watch.Start()
	watch.Wait()
	op, _ := cat.Lookup("hotspots")
	vars := opMerge(opMinimalVariables(t, op), op.Scope(directread.CallerRestricted).ForcedVariablePath, []any{opRepo(opRepoA)})
	q := gqlQueryFor(t, op, vars, nil, "")
	resp := h.run(t, opRestrictedA(), q.text, q.vars)
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalRowOutsideGrant {
		t.Fatalf("want row_outside_grant, got %+v", resp.Refusal)
	}
	if resp.Source.SchemaDigest != watchRefreshedDigest {
		t.Fatalf("reply digest %s, want the digest refreshed during the dispatch %s", resp.Source.SchemaDigest, watchRefreshedDigest)
	}
	if len(rec.reads) != 1 || rec.reads[0].SchemaDigest != watchRefreshedDigest {
		t.Fatalf("read log %+v, want %s", rec.reads, watchRefreshedDigest)
	}
}

// Before any watch has succeeded a refusal does what a served call does at that
// moment: it carries the pinned digest, with no watch attached and while every
// fetch fails.
func TestRefusalBeforeAnyWatchSucceededReportsThePinnedDigest(t *testing.T) {
	cat, err := directread.LoadCatalogue(directread.EmbeddedCatalogueJSON())
	if err != nil {
		t.Fatal(err)
	}
	rec := &opDigestRecorder{}
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, `{}` }, opHarnessOptions{catalogue: cat, recorder: rec})
	refuse := func() string {
		resp := h.run(t, opUnrestricted("org-a"), "no_such_operation", json.RawMessage(`{}`))
		if resp.Call != directread.CallRefused {
			t.Fatalf("want a refusal, got %s", resp.Call)
		}
		return resp.Source.SchemaDigest
	}
	if got := refuse(); got != cat.SchemaDigest() || got != cat.StampedSchemaDigest() {
		t.Fatalf("no watch attached: refusal digest %s, stamped %s, pinned %s", got, cat.StampedSchemaDigest(), cat.SchemaDigest())
	}
	reg := newSwappableRegistry(t, watchServedDigest)
	reg.status.Store(http.StatusInternalServerError)
	watch := reg.watch(t, cat)
	watch.Start()
	watch.Wait()
	if got := refuse(); got != cat.SchemaDigest() || got != cat.StampedSchemaDigest() {
		t.Fatalf("every fetch failed: refusal digest %s, stamped %s, pinned %s", got, cat.StampedSchemaDigest(), cat.SchemaDigest())
	}
	reg.status.Store(http.StatusOK)
	reg.offset.Add(int64(2 * time.Minute))
	cat.StampedSchemaDigest()
	watch.Wait()
	if got := refuse(); got != watchServedDigest || got != cat.StampedSchemaDigest() {
		t.Fatalf("after the first successful fetch: refusal digest %s, stamped %s, want %s", got, cat.StampedSchemaDigest(), watchServedDigest)
	}
	if last := rec.reads[len(rec.reads)-1]; last.SchemaDigest != watchServedDigest {
		t.Fatalf("read log carries %s, want %s", last.SchemaDigest, watchServedDigest)
	}
}
