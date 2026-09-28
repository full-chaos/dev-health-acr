package hosted

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type chaos7071Investigator struct {
	graph contextfabric.GraphReader
	facts contextfabric.CanonicalFactReader
}

func (chaos7071Investigator) Investigate(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	return contextfabric.InvestigationResult{}, nil
}

func (i chaos7071Investigator) DirectReadSources() (contextfabric.GraphReader, contextfabric.CanonicalFactReader) {
	return i.graph, i.facts
}

type chaos7071Facts struct{}

func (chaos7071Facts) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{}, nil
}

// a graph reader that is NOT a gate authority (no OwnershipReachedRepositories).
type chaos7071PlainGraph struct{ contextfabric.GraphReader }

// The production graph (*falkorgraph.Adapter) is a gate authority, so a
// composed engine yields a gate and a fact reader; a missing investigator or
// a graph that cannot authorize yields none (the data handlers fail closed),
// and the second case is logged loudly.
func TestChaos7071BuildDirectReads(t *testing.T) {
	adapter, err := falkorgraph.New(falkorgraph.Config{Addr: "127.0.0.1:1", GraphPrefix: "acr-cf-test", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true})
	if err != nil {
		t.Fatalf("falkorgraph.New (lazy, no server needed): %v", err)
	}
	gate, reader := buildDirectReads(chaos7071Investigator{graph: adapter, facts: chaos7071Facts{}}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if gate == nil || reader == nil {
		t.Fatal("a composed engine over the production graph adapter built no direct-read gate")
	}
	if gate, reader := buildDirectReads(nil, nil); gate != nil || reader != nil {
		t.Fatal("no investigator built a gate")
	}
	var logs bytes.Buffer
	gate, reader = buildDirectReads(chaos7071Investigator{graph: chaos7071PlainGraph{}, facts: chaos7071Facts{}}, slog.New(slog.NewTextHandler(&logs, nil)))
	if gate != nil || reader != nil || !strings.Contains(logs.String(), "context fabric direct read gate not composed") {
		t.Fatalf("unsupported graph: gate %v reader %v logs %q", gate, reader, logs.String())
	}
	gate, reader = buildDirectReads(chaos7071Investigator{graph: adapter}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if gate != nil || reader != nil {
		t.Fatal("no fact registry built a gate")
	}
}
