package directread_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const watchServedDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

// CHAOS-7194: run_operation and data_catalog stamp the SERVED digest once a
// registry watch knows it, the pinned one before.
func TestRegistryWatchStampsRunOperationAndDataCatalog(t *testing.T) {
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
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
		Catalogue: cat, Gate: directread.NewSubjectGate(newOpGraph(), nil), Client: client,
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
		t.Fatalf("run_operation stamp %s, want served %s", got, watchServedDigest)
	}
	if got := catalog(); got != watchServedDigest {
		t.Fatalf("data_catalog stamp %s, want served %s", got, watchServedDigest)
	}
}
