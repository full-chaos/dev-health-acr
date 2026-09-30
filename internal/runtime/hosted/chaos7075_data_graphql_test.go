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

// CHAOS-7075: graphql_query is composed only with ACR_DATA_GRAPHQL_URL, a
// loaded catalogue and a derived root policy; independent of the operations
// URL; a configured URL whose policy does not derive fails startup; no URL
// is logged, never silent.
func TestChaos7075BuildDataReadsGraphQL(t *testing.T) {
	adapter, err := falkorgraph.New(falkorgraph.Config{Addr: "127.0.0.1:1", GraphPrefix: "acr-cf-test", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	engine := chaos7071Investigator{graph: adapter, facts: chaos7071Facts{}}
	gate, _ := buildDirectReads(engine, logger)

	reads, err := buildDataReads("", "", 30*time.Second, engine, gate, logger)
	if err != nil || reads.graphql != nil {
		t.Fatalf("no graphql URL composed a runner: %+v %v", reads, err)
	}
	if !strings.Contains(logs.String(), "data_graphql_not_configured") {
		t.Fatalf("graphql off was silent: %q", logs.String())
	}
	reads, err = buildDataReads("", "http://dev-health-ops-query-api-mcp.dev-health.svc:8092", 30*time.Second, engine, gate, logger)
	if err != nil || reads.graphql == nil || reads.operations != nil {
		t.Fatalf("graphql URL only: %+v %v", reads, err)
	}
	if _, err := buildDataReads("", "http://user:pw@h:8092", 30*time.Second, engine, gate, logger); err == nil || strings.Contains(err.Error(), "pw") {
		t.Fatalf("bad graphql URL: %v", err)
	}

	saved := dataReadsGraphQLPolicy
	t.Cleanup(func() { dataReadsGraphQLPolicy = saved })
	dataReadsGraphQLPolicy = func() (*directread.GraphQLPolicy, error) { return nil, directread.ErrGraphQLPolicyInvalid }
	if _, err := buildDataReads("", "http://h:8092", 30*time.Second, engine, gate, logger); !errors.Is(err, directread.ErrGraphQLPolicyInvalid) {
		t.Fatalf("configured graphql URL with a bad policy started: %v", err)
	}
	if reads, err := buildDataReads("", "", 30*time.Second, engine, gate, logger); err != nil || reads.graphql != nil {
		t.Fatalf("no URL with a bad policy: %v", err)
	}
}
