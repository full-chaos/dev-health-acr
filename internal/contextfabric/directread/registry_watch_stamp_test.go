package directread_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const watchServedDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

// data_catalog stamps the SERVED digest once a registry watch knows it, the
// pinned one before. A refused run_operation reports the same stamped digest
// as a served call, from the same source, never the pinned one beside a served
// digest the watch already knows.
func TestRegistryWatchStampsDataCatalogAndARefusalReportsTheServedDigest(t *testing.T) {
	cat, err := directread.LoadCatalogue(directread.EmbeddedCatalogueJSON())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_digest": watchServedDigest, "operations": []any{}})
	}))
	defer srv.Close()
	watch, err := directread.NewRegistryWatch(directread.RegistryWatchConfig{Catalogue: cat, BaseURL: srv.URL, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	client, err := directread.NewHTTPQueryClient(srv.URL, 5_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	opRec := &opDigestRecorder{}
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
		Catalogue: cat, Gate: directread.NewSubjectGate(newOpGraph(), nil), Client: client, Recorder: opRec,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := func() directread.OperationResponse {
		resp, err := runner.Run(context.Background(), opUnrestricted("org-a"), directread.OperationRequest{Operation: "no_such_operation", Variables: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	catalog := func() string {
		return directread.BuildDataCatalog(cat, directread.CatalogCaller{PrincipalClass: directread.ClassUnrestricted, Scopes: []string{"context:read", "data:read"}, DataRead: true}, nil).Versions.SchemaDigest
	}
	if got := run().Source.SchemaDigest; got != cat.SchemaDigest() {
		t.Fatalf("before the check: run_operation stamp %s, want pinned", got)
	}
	watch.Start()
	watch.Wait()
	if got := run().Source.SchemaDigest; got != watchServedDigest {
		t.Fatalf("refused run_operation stamp %s, want served %s", got, watchServedDigest)
	}
	if last := opRec.reads[len(opRec.reads)-1]; last.SchemaDigest != watchServedDigest {
		t.Fatalf("the run_operation read log carries %s, want the served digest %s", last.SchemaDigest, watchServedDigest)
	}
	if got := catalog(); got != watchServedDigest {
		t.Fatalf("data_catalog stamp %s, want served %s", got, watchServedDigest)
	}
}

type opDigestRecorder struct{ reads []directread.OperationRead }

func (r *opDigestRecorder) RecordOperationRead(_ context.Context, _ storage.Principal, read directread.OperationRead) {
	r.reads = append(r.reads, read)
}

type digestRecorder struct{ reads []directread.GraphQLQueryRead }

func (r *digestRecorder) RecordGraphQLQuery(_ context.Context, _ storage.Principal, read directread.GraphQLQueryRead) {
	r.reads = append(r.reads, read)
}

func TestRefusedGraphQLQueryReportsTheServedDigest(t *testing.T) {
	rec := &digestRecorder{}
	h := newGQLHarness(t, gqlHarnessOptions{recorder: rec, ownCatalogue: true})
	cat := h.policy.Catalogue()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_digest": watchServedDigest, "operations": []any{}})
	}))
	defer srv.Close()
	watch, err := directread.NewRegistryWatch(directread.RegistryWatchConfig{Catalogue: cat, BaseURL: srv.URL, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	watch.Start()
	watch.Wait()
	resp := h.run(t, opUnrestricted("org-a"), "{ not a query", nil)
	if resp.Call != directread.CallRefused {
		t.Fatalf("want a refusal, got %s", resp.Call)
	}
	if resp.Source.SchemaDigest != watchServedDigest {
		t.Fatalf("refused graphql_query stamp %s, want served %s", resp.Source.SchemaDigest, watchServedDigest)
	}
	if len(rec.reads) != 1 || rec.reads[0].SchemaDigest != watchServedDigest {
		t.Fatalf("the read log carries %+v, want the served digest %s", rec.reads, watchServedDigest)
	}
}

func startWatch(t *testing.T, cat *directread.Catalogue) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_digest": watchServedDigest, "operations": []any{}})
	}))
	t.Cleanup(srv.Close)
	watch, err := directread.NewRegistryWatch(directread.RegistryWatchConfig{Catalogue: cat, BaseURL: srv.URL, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	watch.Start()
	watch.Wait()
	if cat.StampedSchemaDigest() != watchServedDigest {
		t.Fatalf("the watch did not take: stamped %s", cat.StampedSchemaDigest())
	}
}

// A refusal decided AFTER the query service answered (a row outside the grant)
// reports the served digest the watch holds, in the reply and in the read log,
// the same digest a served call carries.
func TestPostDispatchRefusalOfRunOperationReportsTheServedDigest(t *testing.T) {
	cat, err := directread.LoadCatalogue(directread.EmbeddedCatalogueJSON())
	if err != nil {
		t.Fatal(err)
	}
	op, _ := cat.Lookup("hotspots")
	scope := op.Scope(directread.CallerRestricted)
	vars := opMerge(opMinimalVariables(t, op), scope.ForcedVariablePath, []any{opRepo(opRepoA)})
	rec := &opDigestRecorder{}
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opRowsAnswer(scope.RowIDPaths[0], []any{opRepoB}) }, opHarnessOptions{catalogue: cat, recorder: rec})
	startWatch(t, cat)
	resp := h.run(t, opRestrictedA(), op.Name, vars)
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalRowOutsideGrant {
		t.Fatalf("want row_outside_grant, got %+v", resp)
	}
	if len(h.upstream.requests()) == 0 {
		t.Fatal("the query service was not called: this is not a post-dispatch refusal")
	}
	if resp.Source.SchemaDigest != watchServedDigest {
		t.Fatalf("reply digest %s, want served %s", resp.Source.SchemaDigest, watchServedDigest)
	}
	if len(rec.reads) != 1 || rec.reads[0].SchemaDigest != watchServedDigest {
		t.Fatalf("read log %+v, want served %s", rec.reads, watchServedDigest)
	}
}

func TestPostDispatchRefusalOfGraphQLQueryReportsTheServedDigest(t *testing.T) {
	rec := &digestRecorder{}
	h := newGQLHarness(t, gqlHarnessOptions{recorder: rec, ownCatalogue: true, fake: func(cfg *fakeMCPConfig) { cfg.RowID = func() string { return opRepoB } }})
	startWatch(t, h.policy.Catalogue())
	op, _ := h.policy.Catalogue().Lookup("hotspots")
	vars := opMerge(opMinimalVariables(t, op), op.Scope(directread.CallerRestricted).ForcedVariablePath, []any{opRepo(opRepoA)})
	q := gqlQueryFor(t, op, vars, nil, "")
	resp := h.run(t, opRestrictedA(), q.text, q.vars)
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalRowOutsideGrant {
		t.Fatalf("want row_outside_grant, got %+v", resp.Refusal)
	}
	if len(h.listener.requests()) == 0 {
		t.Fatal("the listener was not called: this is not a post-dispatch refusal")
	}
	if resp.Source.SchemaDigest != watchServedDigest {
		t.Fatalf("reply digest %s, want served %s", resp.Source.SchemaDigest, watchServedDigest)
	}
	if len(rec.reads) != 1 || rec.reads[0].SchemaDigest != watchServedDigest {
		t.Fatalf("read log %+v, want served %s", rec.reads, watchServedDigest)
	}
}
