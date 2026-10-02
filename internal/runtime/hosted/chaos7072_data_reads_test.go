package hosted

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
)

// CHAOS-7072 (S1a) composition of data_catalog, find_subjects and
// run_operation (design E.5): each part is present only when it can serve,
// and a policy that does not load fails startup only when an operator
// configured the query service.
func TestChaos7072BuildDataReads(t *testing.T) {
	adapter, err := falkorgraph.New(falkorgraph.Config{Addr: "127.0.0.1:1", GraphPrefix: "acr-cf-test", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	engine := chaos7071Investigator{graph: adapter, facts: chaos7071Facts{}}
	gate, _ := buildDirectReads(engine, logger)
	if gate == nil {
		t.Fatal("no gate over the production adapter")
	}

	// No URL: catalogue and subjects, no runner.
	reads, err := buildDataReads("", "", "", 30*time.Second, engine, gate, logger)
	if err != nil || reads.catalogue == nil || reads.subjects == nil || reads.operations != nil {
		t.Fatalf("no URL: %+v %v", reads, err)
	}
	// URL: all three.
	reads, err = buildDataReads("http://query-api.internal:8091", "", "", 30*time.Second, engine, gate, logger)
	if err != nil || reads.catalogue == nil || reads.subjects == nil || reads.operations == nil {
		t.Fatalf("URL: %+v %v", reads, err)
	}
	// URL, no graph: a runner (its gate fails closed) and no subjects.
	reads, err = buildDataReads("http://query-api.internal:8091", "", "", 30*time.Second, nil, nil, logger)
	if err != nil || reads.operations == nil || reads.subjects != nil {
		t.Fatalf("URL, no graph: %+v %v", reads, err)
	}

	// A policy that does not load.
	saved := dataReadsCatalogue
	t.Cleanup(func() { dataReadsCatalogue = saved })
	dataReadsCatalogue = func() (*directread.Catalogue, error) { return nil, directread.ErrCatalogueInvalid }
	if _, err := buildDataReads("http://query-api.internal:8091", "", "", 30*time.Second, engine, gate, logger); !errors.Is(err, directread.ErrCatalogueInvalid) {
		t.Fatalf("configured URL with a bad policy started: %v", err)
	}
	var logs bytes.Buffer
	reads, err = buildDataReads("", "", "", 30*time.Second, engine, gate, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil || reads.catalogue != nil || reads.operations != nil {
		t.Fatalf("no URL, bad policy: %+v %v", reads, err)
	}
	if !strings.Contains(logs.String(), "catalogue_invalid") {
		t.Fatalf("a policy load failure was silent: %q", logs.String())
	}
}
