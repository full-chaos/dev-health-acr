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

// CHAOS-7074: the production graph (*falkorgraph.Adapter) serves bounded
// edge pages, so a composed engine yields a read_relationships reader; no
// gate, no investigator, or a graph that cannot page edges yields none (the
// route fails closed), and the last case is logged loudly.
func TestChaos7074BuildDirectRelationships(t *testing.T) {
	adapter, err := falkorgraph.New(falkorgraph.Config{Addr: "127.0.0.1:1", GraphPrefix: "acr-cf-test", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true})
	if err != nil {
		t.Fatalf("falkorgraph.New (lazy, no server needed): %v", err)
	}
	gate := directread.NewSubjectGate(adapter, nil)
	if buildDirectRelationships(chaos7071Investigator{graph: adapter, facts: chaos7071Facts{}}, gate, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))) == nil {
		t.Fatal("a composed engine over the production graph adapter built no relationships reader")
	}
	if buildDirectRelationships(chaos7071Investigator{graph: adapter}, nil, nil) != nil {
		t.Fatal("no gate built a reader")
	}
	if buildDirectRelationships(nil, gate, nil) != nil {
		t.Fatal("no investigator built a reader")
	}
	var logs bytes.Buffer
	if buildDirectRelationships(chaos7071Investigator{graph: chaos7071PlainGraph{}}, gate, slog.New(slog.NewTextHandler(&logs, nil))) != nil ||
		!strings.Contains(logs.String(), "context fabric direct relationships reader not composed") {
		t.Fatalf("unsupported graph: logs %q", logs.String())
	}
}
