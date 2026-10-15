package directread_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

// refreshingFailingGraph lets a refresh land inside the subject gate and then
// makes the gate's decision unavailable, so the call ends in an error return.
type refreshingFailingGraph struct {
	*opGraph
	refresh func()
}

func (g *refreshingFailingGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, _ []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	g.refresh()
	return nil, errors.New("graph unavailable")
}

// A call that ends in an error return records its read with the digest the
// watch holds when it ends, not the one read at entry: a refresh that landed
// during the call shows in the read log of an error return too.
func TestErrorReturnReadLogReportsADigestRefreshedDuringTheCall(t *testing.T) {
	t.Run("run_operation", func(t *testing.T) {
		cat, err := directread.LoadCatalogue(directread.EmbeddedCatalogueJSON())
		if err != nil {
			t.Fatal(err)
		}
		reg := newSwappableRegistry(t, watchServedDigest)
		watch := reg.watch(t, cat)
		watch.Start()
		watch.Wait()
		graph := &refreshingFailingGraph{opGraph: newOpGraph(), refresh: func() { reg.landRefresh(cat, watch, watchRefreshedDigest) }}
		upstream := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(upstream.Close)
		client, err := directread.NewHTTPQueryClient(upstream.URL, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		rec := &opDigestRecorder{}
		runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
			Catalogue: cat, Gate: directread.NewSubjectGate(graph, nil), Client: client, Recorder: rec, Grants: opDefaultGrants(),
		})
		if err != nil {
			t.Fatal(err)
		}
		op, _ := cat.Lookup("hotspots")
		vars := opMerge(opMinimalVariables(t, op), op.Scope(directread.CallerRestricted).ForcedVariablePath, []any{opRepo(opRepoA)})
		raw, _ := json.Marshal(vars)
		_, err = runner.Run(context.Background(), opRestrictedA(), directread.OperationRequest{Operation: op.Name, Variables: raw})
		if !errors.Is(err, directread.ErrOperationAuthorizationUnavailable) {
			t.Fatalf("want the gate decision unavailable, got %v", err)
		}
		if len(rec.reads) != 1 || rec.reads[0].SchemaDigest != watchRefreshedDigest {
			t.Fatalf("error read log %+v, want the refreshed digest %s", rec.reads, watchRefreshedDigest)
		}
	})
	t.Run("graphql_query", func(t *testing.T) {
		cat, err := directread.LoadCatalogue(directread.EmbeddedCatalogueJSON())
		if err != nil {
			t.Fatal(err)
		}
		policy, err := directread.NewGraphQLPolicy(cat, directread.EmbeddedOpsSchema(), directread.DefaultGraphQLLimits())
		if err != nil {
			t.Fatal(err)
		}
		reg := newSwappableRegistry(t, watchServedDigest)
		watch := reg.watch(t, cat)
		watch.Start()
		watch.Wait()
		graph := &refreshingFailingGraph{opGraph: newOpGraph(), refresh: func() { reg.landRefresh(cat, watch, watchRefreshedDigest) }}
		listener := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(listener.Close)
		client, err := directread.NewHTTPGraphQLClient(listener.URL, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		rec := &digestRecorder{}
		runner, err := directread.NewGraphQLRunner(directread.GraphQLRunnerConfig{
			Policy: policy, Gate: directread.NewSubjectGate(graph, nil), Client: client, Recorder: rec, Grants: opDefaultGrants(),
		})
		if err != nil {
			t.Fatal(err)
		}
		op, _ := cat.Lookup("hotspots")
		vars := opMerge(opMinimalVariables(t, op), op.Scope(directread.CallerRestricted).ForcedVariablePath, []any{opRepo(opRepoA)})
		q := gqlQueryFor(t, op, vars, nil, "")
		raw, _ := json.Marshal(q.vars)
		_, err = runner.Run(context.Background(), opRestrictedA(), directread.GraphQLRequest{Query: q.text, Variables: raw})
		if !errors.Is(err, directread.ErrOperationAuthorizationUnavailable) {
			t.Fatalf("want the gate decision unavailable, got %v", err)
		}
		if len(rec.reads) != 1 || rec.reads[0].SchemaDigest != watchRefreshedDigest {
			t.Fatalf("error read log %+v, want the refreshed digest %s", rec.reads, watchRefreshedDigest)
		}
	})
}

// After a successful watch, a failed refresh puts every reply back on the pinned
// digest: the stamp is the served digest only while the last fetch succeeded.
// A refusal and a data_catalog stamp agree with it at that moment.
func TestRefusalAfterASuccessfulWatchThenAFailedRefreshReportsThePinnedDigest(t *testing.T) {
	failRefresh := func(reg *swappableRegistry, cat *directread.Catalogue, watch *directread.RegistryWatch) {
		reg.status.Store(http.StatusInternalServerError)
		reg.offset.Add(int64(2 * time.Minute))
		cat.StampedSchemaDigest()
		watch.Wait()
	}
	catalog := func(cat *directread.Catalogue) string {
		return directread.BuildDataCatalog(cat, directread.CatalogCaller{PrincipalClass: directread.ClassUnrestricted, Scopes: []string{"context:read", "data:read"}, DataRead: true}, nil).Versions.SchemaDigest
	}
	t.Run("run_operation", func(t *testing.T) {
		cat, err := directread.LoadCatalogue(directread.EmbeddedCatalogueJSON())
		if err != nil {
			t.Fatal(err)
		}
		reg := newSwappableRegistry(t, watchServedDigest)
		watch := reg.watch(t, cat)
		watch.Start()
		watch.Wait()
		rec := &opDigestRecorder{}
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, `{}` }, opHarnessOptions{catalogue: cat, recorder: rec})
		refuse := func() string {
			resp := h.run(t, opUnrestricted("org-a"), "no_such_operation", json.RawMessage(`{}`))
			if resp.Call != directread.CallRefused {
				t.Fatalf("want a refusal, got %s", resp.Call)
			}
			return resp.Source.SchemaDigest
		}
		if got := refuse(); got != watchServedDigest {
			t.Fatalf("after a successful watch: refusal digest %s, want %s", got, watchServedDigest)
		}
		failRefresh(reg, cat, watch)
		pinned := cat.SchemaDigest()
		if got := refuse(); got != pinned || got != cat.StampedSchemaDigest() || got != catalog(cat) {
			t.Fatalf("after a failed refresh: refusal digest %s, stamped %s, data_catalog %s, want the pinned digest %s", got, cat.StampedSchemaDigest(), catalog(cat), pinned)
		}
		if last := rec.reads[len(rec.reads)-1]; last.SchemaDigest != pinned {
			t.Fatalf("read log carries %s, want %s", last.SchemaDigest, pinned)
		}
	})
	t.Run("graphql_query", func(t *testing.T) {
		rec := &digestRecorder{}
		h := newGQLHarness(t, gqlHarnessOptions{recorder: rec, ownCatalogue: true})
		cat := h.policy.Catalogue()
		reg := newSwappableRegistry(t, watchServedDigest)
		watch := reg.watch(t, cat)
		watch.Start()
		watch.Wait()
		refuse := func() string {
			resp := h.run(t, opUnrestricted("org-a"), "{ not a query", nil)
			if resp.Call != directread.CallRefused {
				t.Fatalf("want a refusal, got %s", resp.Call)
			}
			return resp.Source.SchemaDigest
		}
		if got := refuse(); got != watchServedDigest {
			t.Fatalf("after a successful watch: refusal digest %s, want %s", got, watchServedDigest)
		}
		failRefresh(reg, cat, watch)
		pinned := cat.SchemaDigest()
		if got := refuse(); got != pinned || got != cat.StampedSchemaDigest() || got != catalog(cat) {
			t.Fatalf("after a failed refresh: refusal digest %s, stamped %s, data_catalog %s, want the pinned digest %s", got, cat.StampedSchemaDigest(), catalog(cat), pinned)
		}
		if last := rec.reads[len(rec.reads)-1]; last.SchemaDigest != pinned {
			t.Fatalf("read log carries %s, want %s", last.SchemaDigest, pinned)
		}
	})
}
