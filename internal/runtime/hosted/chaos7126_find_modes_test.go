package hosted

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
)

// CHAOS-7126: the production graph serves both new modes' graph reads; a
// graph that cannot, or no ClickHouse client, leaves the mode off and says
// so loudly.
func TestChaos7126ComposeFindModes(t *testing.T) {
	adapter, err := falkorgraph.New(falkorgraph.Config{Addr: "127.0.0.1:1", GraphPrefix: "acr-cf-test", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	var _ directread.EdgeGraph = adapter
	var _ directread.SubjectNodeReader = adapter
	gate := directread.NewSubjectGate(adapter, nil)
	var logs bytes.Buffer
	composeFindModes(directread.NewSubjectLookup(adapter, gate, nil), chaos7071Investigator{graph: adapter, facts: chaos7071Facts{}}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if strings.Contains(logs.String(), "mode=owned_by") || !strings.Contains(logs.String(), "mode=handle") {
		t.Fatalf("no ClickHouse client: want handle off and owned_by on, logs %q", logs.String())
	}
	logs.Reset()
	composeFindModes(directread.NewSubjectLookup(adapter, gate, nil), chaos7071Investigator{graph: chaos7071PlainGraph{}}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if !strings.Contains(logs.String(), "mode=owned_by") {
		t.Fatalf("graph without edge pages: logs %q", logs.String())
	}
	composeFindModes(nil, nil, nil, nil) // no lookup: nothing to compose, no panic
}
