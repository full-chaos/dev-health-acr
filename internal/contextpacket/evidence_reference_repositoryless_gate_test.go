package contextpacket_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// scriptedClient answers the repository lookup and the repository-less issue
// gate, and records the bindings of every other statement.
type scriptedClient struct {
	gateIDs  []string
	resolved [][]contextpacket.ClickHouseBinding
}

func (c *scriptedClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	switch {
	case strings.Contains(statement, "repository_scopes"):
		return &rowScanner{rows: [][]any{{"20000000-0000-4000-8000-000000000002", "example-org/widget-service"}}}, nil
	case strings.Contains(statement, "max_threads = 1"):
		rows := make([][]any, 0, len(c.gateIDs))
		for _, id := range c.gateIDs {
			rows = append(rows, []any{id})
		}
		return &rowScanner{rows: rows}, nil
	}
	c.resolved = append(c.resolved, bindings)
	return &rowScanner{}, nil
}

// The packet statement for work_graph.v1 names a repository-less issue by title
// only for the ids the caller's grant decided; the evidence-reference read of
// the same row must carry the same decision, or the expansion names the issue
// by key alone.
func TestEvidenceReferenceReadCarriesTheRepositorylessIssueGate(t *testing.T) {
	client := &scriptedClient{gateIDs: []string{"PROJ-5"}}
	codec := fixtureEvidenceCodec(t)
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(contextpacket.NewCatalogClickHouseRows(client), contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := codec.Encode("org-fixture", "20000000-0000-4000-8000-000000000002", "work_graph.v1", "acr:v1:work-graph:edge", false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.ResolveEvidence(context.Background(), fixturePrincipal(), handle)
	if len(client.resolved) != 1 {
		t.Fatalf("reference reads = %d, want 1", len(client.resolved))
	}
	got, ok := sourceRowBinding(client.resolved[0], "readable_repositoryless_issue_ids")
	if !ok || !reflect.DeepEqual(got, []string{"PROJ-5"}) {
		t.Fatalf("readable_repositoryless_issue_ids = %#v, want [PROJ-5]", got)
	}
}
