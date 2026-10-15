package devhealthsource_test

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
)

type mappingRevision struct {
	id, repoID string
	at         time.Time
	active     uint8
}

// A service mapping that moves or ends with no new incident revision changes
// the graph on the next incremental read, to exactly what a from-zero read of
// the same rows builds: the edge to the repository the mapping left is gone.
// Each case is one organization on one real ClickHouse and one real FalkorDB.
func TestIncidentGraphFollowsServiceMappingChanges(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	var addr string
	incremental := chaos7074FalkorAdapterWith(t, ctx, func(config *falkorgraph.Config) {
		config.GraphPrefix = "acr-cf-incident-incremental"
		addr = config.Addr
	})
	adapter := func(prefix string) *falkorgraph.Adapter {
		a, err := falkorgraph.New(falkorgraph.Config{
			Addr: addr, GraphPrefix: prefix, RequestTimeout: 15 * time.Second,
			MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true,
		})
		if err != nil {
			t.Fatalf("falkorgraph.New: %v", err)
		}
		return a
	}
	rebuildA, rebuildB := adapter("acr-cf-incident-rebuild-a"), adapter("acr-cf-incident-rebuild-b")
	raw := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = raw.Close() })

	// apply writes the batches and returns the graph key the first write created.
	apply := func(t *testing.T, a *falkorgraph.Adapter, key string, batches []contextfabric.ProjectionBatch) string {
		t.Helper()
		before := graphKeys(t, ctx, raw)
		for i, batch := range batches {
			if _, err := a.ApplyProjectionBatch(ctx, batch); err != nil {
				t.Fatalf("apply batch %d of %d: %v", i+1, len(batches), err)
			}
		}
		if key != "" {
			return key
		}
		for k := range graphKeys(t, ctx, raw) {
			if !before[k] {
				return k
			}
		}
		t.Fatal("the apply created no graph")
		return ""
	}
	subjects := func(state graphState) graphState {
		out := graphState{nodes: map[string]map[string]any{}, edges: state.edges}
		for id, props := range state.nodes {
			if _, subject := props["canonical_id"]; subject {
				out.nodes[id] = props
			}
		}
		return out
	}

	now := time.Now().UTC().Truncate(time.Second)
	earlier, later := now.Add(-2*time.Hour), now.Add(-time.Hour)
	const (
		repoA     = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a01"
		repoB     = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a02"
		serviceID = "pagerduty:PX7Q2LM:service:PS4K9D4"
	)
	for _, tc := range []struct {
		name, orgID   string
		before, after []mappingRevision
		wantRepos     []string
	}{
		{
			name: "a mapping moved to another repository", orgID: "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0b01",
			before: []mappingRevision{{"svc:github:acme/checkout:admin", repoA, earlier, 1}},
			after: []mappingRevision{
				{"svc:github:acme/checkout:admin", repoA, later, 0},
				{"svc:github:acme/ledger:admin", repoB, later, 1},
			},
			wantRepos: []string{repoB},
		},
		{
			name: "one of two mappings ended", orgID: "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0b02",
			before: []mappingRevision{
				{"svc:github:acme/checkout:admin", repoA, earlier, 1},
				{"svc:github:acme/ledger:admin", repoB, earlier, 1},
			},
			after:     []mappingRevision{{"svc:github:acme/ledger:admin", repoB, later, 0}},
			wantRepos: []string{repoA},
		},
		{
			name: "two active mappings", orgID: "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0b04",
			before: []mappingRevision{
				{"svc:github:acme/checkout:admin", repoA, earlier, 1},
				{"svc:github:acme/ledger:admin", repoB, earlier, 1},
			},
			wantRepos: []string{repoA, repoB},
		},
		{
			name: "the last mapping ended", orgID: "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0b03",
			before: []mappingRevision{{"svc:github:acme/checkout:admin", repoA, earlier, 1}},
			after:  []mappingRevision{{"svc:github:acme/checkout:admin", repoA, later, 0}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			incidentID := "8e4b1d70-2c9a-4f63-b815-" + tc.orgID[len(tc.orgID)-12:]
			for _, repo := range [][2]string{{repoA, "acme/checkout"}, {repoB, "acme/ledger"}} {
				mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo[0], repo[1], nil, now, nil, earlier.Add(-time.Hour), tc.orgID, "github")
			}
			write := func(revisions []mappingRevision) {
				for _, m := range revisions {
					mustExec(t, ctx, direct, `INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, last_synced, service_id, repo_id, is_active) VALUES (?, ?, ?, ?, ?, ?, ?)`,
						tc.orgID, m.at, m.id, m.at, serviceID, m.repoID, m.active)
				}
			}
			write(tc.before)
			mustExec(t, ctx, direct, `INSERT INTO operational_incidents (id, org_id, source_version_at, service_id, title, normalized_status, raw_status, normalized_severity, raw_severity, started_at, source_event_at, observed_at, is_deleted, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				incidentID, tc.orgID, earlier, serviceID, "checkout timeouts", "triggered", "triggered", "sev2", "sev2", earlier, earlier, earlier, uint8(0), earlier)

			src, err := devhealthsource.NewClickHouseProjectionSource(query)
			if err != nil {
				t.Fatal(err)
			}
			h := &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.SourceName, orgID: tc.orgID, repo: repoA}
			first := h.drain("")
			key := apply(t, incremental, "", first.batches)
			t.Cleanup(func() { _ = incremental.PurgeOrganization(context.Background(), tc.orgID) })
			write(tc.after)
			apply(t, incremental, key, h.drain(first.cursor).batches)
			got := subjects(readSubjectGraph(t, ctx, raw, key))

			var repos []string
			incidentNode := false
			for id := range got.edges {
				if _, end, ok := strings.Cut(id, "|incident:"+incidentID+">repository:"); ok {
					repos = append(repos, end)
				}
			}
			for id := range got.nodes {
				incidentNode = incidentNode || strings.HasSuffix(id, "|incident:"+incidentID)
			}
			sort.Strings(repos)
			if strings.Join(repos, ",") != strings.Join(tc.wantRepos, ",") {
				t.Errorf("after the incremental read the incident belongs to %v, want %v", repos, tc.wantRepos)
			}
			if incidentNode != (len(tc.wantRepos) > 0) {
				t.Errorf("after the incremental read the incident node is present=%v, want %v", incidentNode, len(tc.wantRepos) > 0)
			}

			rebuilt := h.drain("")
			keyA := apply(t, rebuildA, "", rebuilt.batches)
			keyB := apply(t, rebuildB, "", rebuilt.batches)
			t.Cleanup(func() {
				_ = rebuildA.PurgeOrganization(context.Background(), tc.orgID)
				_ = rebuildB.PurgeOrganization(context.Background(), tc.orgID)
			})
			stateA, stateB := subjects(readSubjectGraph(t, ctx, raw, keyA)), subjects(readSubjectGraph(t, ctx, raw, keyB))
			volatile := map[string]bool{}
			for _, pair := range [][2]map[string]map[string]any{{stateA.nodes, stateB.nodes}, {stateA.edges, stateB.edges}} {
				for id, props := range pair[0] {
					for name, value := range props {
						if other, ok := pair[1][id]; ok && !reflect.DeepEqual(value, other[name]) {
							volatile[name] = true
						}
					}
				}
			}
			for _, name := range []string{"canonical_id", "subject_kind", "relationship_id"} {
				if volatile[name] {
					t.Fatalf("%s differs between two rebuilds of one organization: the comparison would read nothing", name)
				}
			}
			if diff := graphDifferences(got, stateA, volatile); len(diff) != 0 {
				t.Errorf("the incremental graph differs from the rebuilt one (%d differences), first: %v", len(diff), diff[:min(len(diff), 10)])
			}
		})
	}
}

// When a mapping changes, every incident of its service is re-read at one
// keyset position. Pages of a few rows, with a boundary inside the rows of one
// incident (two active mappings give two rows each), read each incident once.
func TestIncidentPagesNeitherSkipNorRepeatAcrossAServiceReread(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	const (
		orgID     = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0c01"
		repoA     = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0c02"
		repoB     = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0c03"
		serviceID = "pagerduty:PX7Q2LM:service:PS4K9D5"
	)
	now := time.Now().UTC().Truncate(time.Second)
	earlier, later := now.Add(-2*time.Hour), now.Add(-time.Hour)
	for _, repo := range [][2]string{{repoA, "acme/checkout"}, {repoB, "acme/ledger"}} {
		mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo[0], repo[1], nil, now, nil, earlier, orgID, "github")
	}
	mapping := func(id, repoID string, at time.Time) {
		mustExec(t, ctx, direct, `INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, last_synced, service_id, repo_id, is_active) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			orgID, at, id, at, serviceID, repoID, uint8(1))
	}
	mapping("svc:github:acme/checkout:admin", repoA, earlier)
	mapping("svc:github:acme/ledger:admin", repoB, earlier)
	var want []string
	for i := range 7 {
		id := "8e4b1d70-2c9a-4f63-b815-00000000000" + string(rune('1'+i))
		want = append(want, id)
		at := earlier.Add(time.Duration(i) * time.Minute)
		mustExec(t, ctx, direct, `INSERT INTO operational_incidents (id, org_id, source_version_at, service_id, title, normalized_status, raw_status, normalized_severity, raw_severity, started_at, source_event_at, observed_at, is_deleted, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, orgID, at, serviceID, "incident "+id, "triggered", "triggered", "sev2", "sev2", at, at, at, uint8(0), at)
	}
	check := func(when string) {
		t.Helper()
		pages, err := devhealthsource.IncidentPagesForTest(ctx, query, orgID, 3)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		count := map[string]int{}
		for _, page := range pages {
			for _, id := range page {
				count[id]++
			}
		}
		for _, id := range want {
			if count[id] != 1 {
				t.Errorf("%s: incident %s was read %d times over %d pages %v", when, id, count[id], len(pages), pages)
			}
		}
		if len(pages) < 3 {
			t.Errorf("%s: %d pages; the boundary this test needs did not occur", when, len(pages))
		}
		t.Logf("%s: pages %v", when, pages)
	}
	check("before the mapping change")
	mapping("svc:github:acme/ledger:admin", repoB, later)
	check("after the mapping change")
}

// readSubjectGraph reads the subject nodes and the edges of one graph, by
// named properties (a node's search text holds newlines, which toJSON does
// not escape).
func readSubjectGraph(t *testing.T, ctx context.Context, raw *redis.Client, key string) graphState {
	t.Helper()
	rows := func(query string) [][]any {
		reply, err := raw.Do(ctx, "GRAPH.RO_QUERY", key, query).Slice()
		if err != nil || len(reply) < 2 {
			t.Fatalf("%s: reply of %d parts, err=%v", query, len(reply), err)
		}
		records, ok := reply[1].([]any)
		if !ok {
			t.Fatalf("%s: the records are a %T", query, reply[1])
		}
		out := make([][]any, 0, len(records))
		for _, record := range records {
			cells, ok := record.([]any)
			if !ok {
				t.Fatalf("%s: a record is a %T", query, record)
			}
			out = append(out, cells)
		}
		return out
	}
	nodeProps := []string{"label", "authorization_repositories", "valid_from", "valid_to", "observed_at", "evidence_refs", "property_status", "property_severity", "source_version"}
	edgeProps := []string{"relation_type", "authorization_repositories", "valid_from", "valid_to", "observed_at", "evidence_refs", "source_version"}
	ret := func(alias string, names []string) string {
		parts := make([]string, len(names))
		for i, name := range names {
			parts[i] = alias + "." + name
		}
		return strings.Join(parts, ", ")
	}
	state := graphState{nodes: map[string]map[string]any{}, edges: map[string]map[string]any{}}
	for _, cells := range rows("MATCH (n) WHERE n.canonical_id IS NOT NULL RETURN n.subject_kind, n.canonical_id, " + ret("n", nodeProps)) {
		id := fmt.Sprintf("%v|%v", cells[0], cells[1])
		if _, twice := state.nodes[id]; twice {
			t.Fatalf("two nodes are %s", id)
		}
		props := map[string]any{"canonical_id": cells[1], "subject_kind": cells[0]}
		for i, name := range nodeProps {
			props[name] = cells[2+i]
		}
		state.nodes[id] = props
	}
	for _, cells := range rows("MATCH (a)-[r]->(b) RETURN r.relationship_id, a.canonical_id, b.canonical_id, " + ret("r", edgeProps)) {
		id := fmt.Sprintf("%v|%v>%v", cells[0], cells[1], cells[2])
		if _, twice := state.edges[id]; twice {
			t.Fatalf("two edges are %s", id)
		}
		props := map[string]any{"relationship_id": cells[0]}
		for i, name := range edgeProps {
			props[name] = cells[3+i]
		}
		state.edges[id] = props
	}
	return state
}
