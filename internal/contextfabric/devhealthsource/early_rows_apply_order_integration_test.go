package devhealthsource_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
)

// graphState is one organization's graph as the backend holds it: every node
// and every edge, by its own identity, with its labels and properties.
type graphState struct {
	nodes map[string]map[string]any
	edges map[string]map[string]any
}

func readGraphState(t *testing.T, ctx context.Context, raw *redis.Client, orgID string) graphState {
	t.Helper()
	keys, err := raw.Do(ctx, "GRAPH.LIST").StringSlice()
	if err != nil {
		t.Fatalf("GRAPH.LIST: %v", err)
	}
	var key string
	for _, k := range keys {
		if strings.Contains(k, orgID) {
			if key != "" {
				t.Fatalf("two graphs name the organization %s: %s and %s", orgID, key, k)
			}
			key = k
		}
	}
	if key == "" {
		t.Fatalf("no graph names the organization %s (graphs: %v)", orgID, keys)
	}
	rows := func(query string) []map[string]any {
		reply, err := raw.Do(ctx, "GRAPH.RO_QUERY", key, query).Slice()
		if err != nil || len(reply) < 2 {
			t.Fatalf("%s: reply of %d parts, err=%v", query, len(reply), err)
		}
		records, ok := reply[1].([]any)
		if !ok {
			t.Fatalf("%s: the records are a %T", query, reply[1])
		}
		out := make([]map[string]any, 0, len(records))
		for _, record := range records {
			cells, ok := record.([]any)
			if !ok || len(cells) != 1 {
				t.Fatalf("%s: a record is %#v", query, record)
			}
			text, ok := cells[0].(string)
			if !ok {
				t.Fatalf("%s: a cell is a %T", query, cells[0])
			}
			var decoded map[string]any
			if err := json.Unmarshal([]byte(text), &decoded); err != nil {
				t.Fatalf("%s: %v in %s", query, err, text)
			}
			out = append(out, decoded)
		}
		return out
	}
	properties := func(value any) map[string]any {
		object, _ := value.(map[string]any)
		props, _ := object["properties"].(map[string]any)
		return props
	}
	state := graphState{nodes: map[string]map[string]any{}, edges: map[string]map[string]any{}}
	for _, node := range rows("MATCH (n) RETURN toJSON(n)") {
		props := properties(node)
		labels, _ := node["labels"].([]any)
		names := make([]string, 0, len(labels))
		for _, label := range labels {
			names = append(names, fmt.Sprint(label))
		}
		sort.Strings(names)
		id := fmt.Sprintf("%v|%v|%v", names, props["subject_kind"], props["canonical_id"])
		if _, twice := state.nodes[id]; twice {
			t.Fatalf("two nodes are %s", id)
		}
		state.nodes[id] = props
	}
	for _, edge := range rows("MATCH ()-[r]->() RETURN toJSON(r)") {
		props := properties(edge)
		id := fmt.Sprintf("%v|%v|%v>%v", edge["relationship"], props["relationship_id"], properties(edge["start"])["canonical_id"], properties(edge["end"])["canonical_id"])
		if _, twice := state.edges[id]; twice {
			t.Fatalf("two edges are %s", id)
		}
		state.edges[id] = props
	}
	return state
}

// differences lists what two graph states disagree on, ignoring the property
// names in volatile.
func graphDifferences(a, b graphState, volatile map[string]bool) []string {
	var out []string
	compare := func(what string, left, right map[string]map[string]any) {
		for id, props := range left {
			other, ok := right[id]
			if !ok {
				out = append(out, what+" only in the first: "+id)
				continue
			}
			for name, value := range props {
				if !volatile[name] && !reflect.DeepEqual(value, other[name]) {
					out = append(out, fmt.Sprintf("%s %s: %s = %v, and %v", what, id, name, value, other[name]))
				}
			}
			for name := range other {
				if _, ok := props[name]; !ok && !volatile[name] {
					out = append(out, fmt.Sprintf("%s %s: %s only in the second", what, id, name))
				}
			}
		}
		for id := range right {
			if _, ok := left[id]; !ok {
				out = append(out, what+" only in the second: "+id)
			}
		}
	}
	compare("node", a.nodes, b.nodes)
	compare("edge", a.edges, b.edges)
	sort.Strings(out)
	return out
}

// The first batch of a from-zero walk applies the complete tables' edges
// before their endpoint entities and their tombstones before the rows they
// retire. After the whole walk the backend must hold exactly what it holds
// after the same walk without those early rows.
//
// Four organizations on one real FalkorDB. Two take the walk alone: what
// differs between them (the organization id, the time of the write) is what
// no comparison may read. One takes the walk with the early rows. One takes
// that walk with its tombstones only in the first batch, never again: its
// graph must differ, or the comparison cannot see an order fault at all.
func TestEarlyRowsLeaveTheGraphTheWalkAloneLeaves(t *testing.T) {
	ctx := context.Background()
	var addr string
	adapter := chaos7074FalkorAdapterWith(t, ctx, func(config *falkorgraph.Config) {
		config.GraphPrefix = "acr-cf-early-rows"
		addr = config.Addr
	})
	raw := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = raw.Close() })

	apply := func(orgID string, batches []contextfabric.ProjectionBatch) graphState {
		t.Helper()
		t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
		for i, batch := range batches {
			if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
				t.Fatalf("%s: apply batch %d of %d: %v", orgID, i+1, len(batches), err)
			}
		}
		return readGraphState(t, ctx, raw, orgID)
	}
	_, aloneA := devhealthsource.EarlyRowsWalksForTest(t, "early-rows-alone-a")
	_, aloneB := devhealthsource.EarlyRowsWalksForTest(t, "early-rows-alone-b")
	with, _ := devhealthsource.EarlyRowsWalksForTest(t, "early-rows-with")
	once, _ := devhealthsource.EarlyRowsWalksForTest(t, "early-rows-once")
	for i := range once[1:] {
		once[i+1].Tombstones = nil
	}

	stateA, stateB := apply("early-rows-alone-a", aloneA), apply("early-rows-alone-b", aloneB)
	if len(stateA.nodes) < 400 || len(stateA.edges) < 150 {
		t.Fatalf("the walk alone left %d nodes and %d edges; the fixture has over 400 and 150 that stay", len(stateA.nodes), len(stateA.edges))
	}
	retired := 0
	for id := range stateA.nodes {
		for _, n := range []int{284, 288, 292, 296, 300} { // the repositories retired after every edge
			if strings.HasSuffix(id, fmt.Sprintf("repository:00000000-0000-4000-8000-%012d", n)) {
				retired++
			}
		}
	}
	if retired != 0 {
		t.Fatalf("%d of the 5 repositories retired after every edge are still nodes after the walk alone: the tombstones did not apply", retired)
	}
	volatile := map[string]bool{}
	for _, pair := range [][2]map[string]map[string]any{{stateA.nodes, stateB.nodes}, {stateA.edges, stateB.edges}} {
		if len(pair[0]) != len(pair[1]) {
			t.Fatalf("two walks alone left %d and %d rows", len(pair[0]), len(pair[1]))
		}
		for id, props := range pair[0] {
			other, ok := pair[1][id]
			if !ok {
				t.Fatalf("two walks alone disagree on the identity %s", id)
			}
			for name, value := range props {
				if !reflect.DeepEqual(value, other[name]) {
					volatile[name] = true
				}
			}
		}
	}
	for _, name := range []string{"canonical_id", "subject_kind", "relationship_id", "label"} {
		if volatile[name] {
			t.Fatalf("the property %s differs between two walks alone: the comparison would read nothing", name)
		}
	}
	t.Logf("properties that differ between two walks alone, not compared: %v", volatile)

	if diff := graphDifferences(stateA, apply("early-rows-with", with), volatile); len(diff) != 0 {
		t.Fatalf("the walk with the early rows left a different graph (%d differences), first: %v", len(diff), diff[:min(len(diff), 10)])
	}
	if diff := graphDifferences(stateA, apply("early-rows-once", once), volatile); len(diff) == 0 {
		t.Fatal("a walk whose tombstones came only with the first batch left the same graph: the comparison cannot see an apply-order fault")
	}
}
