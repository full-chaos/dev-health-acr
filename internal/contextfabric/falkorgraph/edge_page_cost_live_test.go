package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FalkorDB/falkordb-go/v2"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	edgePageCostItems = 12000
	edgePageCostRepos = 200
)

type edgePageCostVenue struct {
	adapter *Adapter
	raw     *redis.Client
	key     string
	orgID   string
	now     time.Time
	team    contextfabric.SubjectRef
}

func edgePageCostStart(t *testing.T, ctx context.Context) *edgePageCostVenue {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: codexRoundFalkordbImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start FalkorDB container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := host + ":" + port.Port()
	adapter, err := New(Config{
		Addr: addr, GraphPrefix: "acr-cf-edge-page-cost", RequestTimeout: 30 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw := redis.NewClient(&redis.Options{Addr: addr, ReadTimeout: 60 * time.Second})
	t.Cleanup(func() { _ = raw.Close() })
	return &edgePageCostVenue{adapter: adapter, raw: raw}
}

func edgePageCostRID(prefix string, i int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", prefix, i)))
	return "rel_" + hex.EncodeToString(sum[:8])
}

// seed: one team hub owning edgePageCostRepos repositories and
// edgePageCostItems work items (work-item OWNED_BY_TEAM edges carry no
// window, as the producer writes them); 80% of the work items ended in the
// past; every work item BELONGS_TO_REPOSITORY one repository with the item's
// own window. Written through the real ApplyProjectionBatch.
func (v *edgePageCostVenue) seed(t *testing.T, ctx context.Context) {
	t.Helper()
	v.orgID = "live-edge-page-cost-" + time.Now().UTC().Format("20060102T150405.000000000")
	v.now = time.Now().UTC()
	start := v.now.Add(-600 * 24 * time.Hour)
	v.team = contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T", Label: "T"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}, TeamIDs: []string{"T"}}
	entity := func(subject contextfabric.SubjectRef, from, to *time.Time) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + strings.NewReplacer(":", "_", ".", "_").Replace(subject.CanonicalID)},
			ObservedAt: v.now, ValidFrom: from, ValidTo: to, SourceVersion: "v1",
		}
	}
	relation := func(id, kind string, from, to contextfabric.SubjectRef, validFrom, validTo *time.Time) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: contextfabric.RelationshipType(kind), From: from, To: to,
			Derivation: contextfabric.DerivationRuleInferred, EpistemicStatus: contextfabric.EpistemicSourceAsserted,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: v.now,
			ValidFrom: validFrom, ValidTo: validTo, SourceVersion: "v1",
		}
	}
	entities := []contextfabric.EntityProjection{entity(v.team, &start, nil)}
	var relationships []contextfabric.RelationshipProjection
	repos := make([]contextfabric.SubjectRef, 0, edgePageCostRepos)
	for i := 0; i < edgePageCostRepos; i++ {
		repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: fmt.Sprintf("repository:r%04d", i), Label: fmt.Sprintf("acme/r%04d", i)}
		repos = append(repos, repo)
		entities = append(entities, entity(repo, &start, nil))
		relationships = append(relationships, relation(edgePageCostRID("own-repo", i), "OWNED_BY_TEAM", repo, v.team, &start, nil))
	}
	for i := 0; i < edgePageCostItems; i++ {
		item := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item.v2:w%05d", i), Label: fmt.Sprintf("Work item %d", i)}
		created := v.now.Add(-time.Duration(i%500+5) * 24 * time.Hour)
		var ended *time.Time
		if i%5 != 0 {
			e := created.Add(time.Duration(i%30+1) * 24 * time.Hour)
			if e.After(v.now) {
				e = v.now.Add(-time.Hour)
			}
			ended = &e
		}
		entities = append(entities, entity(item, &created, ended))
		relationships = append(relationships,
			relation(edgePageCostRID("own-item", i), "OWNED_BY_TEAM", item, v.team, nil, nil),
			relation(edgePageCostRID("item-repo", i), "BELONGS_TO_REPOSITORY", item, repos[i%len(repos)], &created, ended))
	}
	apply := func(index int, entities []contextfabric.EntityProjection, relationships []contextfabric.RelationshipProjection) {
		b := contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_edge_page_cost_%08d", index), OrgID: v.orgID, Source: "live-test",
			SourceVersion: "v1", Cursor: fmt.Sprintf("cursor-%d", index), NextCursor: fmt.Sprintf("cursor-%d", index+1), GeneratedAt: v.now,
			Entities: entities, Relationships: relationships,
			Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		}
		if _, err := v.adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%d): %v", index, err)
		}
	}
	const chunk = 1000
	index := 1
	for lo := 0; lo < len(entities); lo += chunk {
		apply(index, entities[lo:min(lo+chunk, len(entities))], []contextfabric.RelationshipProjection{})
		index++
	}
	for lo := 0; lo < len(relationships); lo += chunk {
		apply(index, []contextfabric.EntityProjection{}, relationships[lo:min(lo+chunk, len(relationships))])
		index++
	}
	binding, err := v.adapter.ResolveInvestigationBinding(ctx, storage.Principal{OrgID: v.orgID, Subject: "u", CredentialID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if v.key, err = v.adapter.effectiveKey(ctx, v.orgID, binding); err != nil {
		t.Fatal(err)
	}
}

func (v *edgePageCostVenue) text(t *testing.T, cypher string, params map[string]interface{}) string {
	t.Helper()
	safe, err := safeParams(params)
	if err != nil {
		t.Fatal(err)
	}
	return falkordb.BuildParamsHeader(safe) + cypher
}

func edgePageCostLines(reply interface{}) []string {
	var out []string
	var walk func(interface{})
	walk = func(x interface{}) {
		switch value := x.(type) {
		case []interface{}:
			for _, item := range value {
				walk(item)
			}
		case string:
			out = append(out, value)
		default:
			out = append(out, fmt.Sprint(value))
		}
	}
	walk(reply)
	return out
}

// internalMs runs one read and returns the server's internal execution time
// and the row count.
func (v *edgePageCostVenue) internalMs(t *testing.T, ctx context.Context, text string) (float64, int) {
	t.Helper()
	reply, err := v.raw.Do(ctx, "GRAPH.RO_QUERY", v.key, text).Result()
	if err != nil {
		t.Fatalf("GRAPH.RO_QUERY: %v\n%s", err, text)
	}
	parts, _ := reply.([]interface{})
	rows := 0
	if len(parts) == 3 {
		if list, ok := parts[1].([]interface{}); ok {
			rows = len(list)
		}
	}
	for _, line := range edgePageCostLines(parts[len(parts)-1]) {
		if strings.HasPrefix(line, "Query internal execution time:") {
			field := strings.Fields(strings.TrimPrefix(line, "Query internal execution time:"))
			ms, _ := strconv.ParseFloat(field[0], 64)
			return ms, rows
		}
	}
	t.Fatalf("no internal execution time in %v", reply)
	return 0, 0
}

func (v *edgePageCostVenue) plan(t *testing.T, ctx context.Context, command, text string) string {
	t.Helper()
	reply, err := v.raw.Do(ctx, command, v.key, text).Result()
	if err != nil {
		return fmt.Sprintf("%s error: %v", command, err)
	}
	return strings.Join(edgePageCostLines(reply), "\n")
}

func edgePageCostPerArmLimit(cypher string) string {
	order := fmt.Sprintf(" ORDER BY r.%s ASC LIMIT $lim", propRelationshipID)
	cypher = strings.Replace(cypher, "RETURN r, a, b UNION", "RETURN r, a, b"+order+" UNION", 1)
	return strings.Replace(cypher, "RETURN r, a, b } RETURN", "RETURN r, a, b"+order+" } RETURN", 1)
}

func edgePageCostCount(cypher string) string {
	cut := strings.LastIndex(cypher, "} RETURN")
	return cypher[:cut] + "} RETURN count(r) AS n"
}

func TestEdgePageCostProfile(t *testing.T) {
	ctx := context.Background()
	v := edgePageCostStart(t, ctx)
	began := time.Now()
	v.seed(t, ctx)
	t.Logf("seeded %d work items, %d repositories, %d edges in %s", edgePageCostItems, edgePageCostRepos, 2*edgePageCostItems+edgePageCostRepos, time.Since(began))
	asOf := v.now.Add(-250 * 24 * time.Hour)
	origin := []contextfabric.SubjectRef{v.team}
	cases := []struct {
		name  string
		query directread.EdgePageQuery
		mod   func(string) string
	}{
		{"current", directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: v.now, Current: true}, nil},
		{"as_of_past", directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: asOf}, nil},
		{"strict_now", directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: v.now}, nil},
		{"current_per_arm_limit", directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: v.now, Current: true}, edgePageCostPerArmLimit},
		{"as_of_past_per_arm_limit", directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: asOf}, edgePageCostPerArmLimit},
		{"current_in_types", directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: v.now, Current: true, Direction: directread.EdgeDirectionIn, Types: []string{"OWNED_BY_TEAM"}}, nil},
		{"as_of_past_in_types", directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: asOf, Direction: directread.EdgeDirectionIn, Types: []string{"OWNED_BY_TEAM"}}, nil},
		{"current_limit100", directread.EdgePageQuery{Origins: origin, Limit: 100, ValidAt: v.now, Current: true}, nil},
		{"current_limit100_per_arm", directread.EdgePageQuery{Origins: origin, Limit: 100, ValidAt: v.now, Current: true}, edgePageCostPerArmLimit},
	}
	pages := map[string][]string{}
	for _, c := range cases {
		cypher, params := directEdgePageCypher(v.orgID, c.query)
		if c.mod != nil {
			cypher = c.mod(cypher)
		}
		text := v.text(t, cypher, params)
		_, n := v.internalMs(t, ctx, v.text(t, edgePageCostCount(cypher), params))
		countReply, _ := v.raw.Do(ctx, "GRAPH.RO_QUERY", v.key, v.text(t, edgePageCostCount(cypher), params)).Result()
		var times []float64
		var rows int
		for run := 0; run < 6; run++ {
			ms, got := v.internalMs(t, ctx, text)
			if run > 0 {
				times = append(times, ms)
			}
			rows = got
		}
		sort.Float64s(times)
		t.Logf("CASE %s rows=%d count_reply=%v (n=%d) internal_ms sorted=%v median=%.2f\nCYPHER %s", c.name, rows, edgePageCostLines(countReply), n, times, times[len(times)/2], cypher)
		t.Logf("EXPLAIN %s\n%s", c.name, v.plan(t, ctx, "GRAPH.EXPLAIN", text))
		t.Logf("PROFILE %s\n%s", c.name, v.plan(t, ctx, "GRAPH.PROFILE", text))
		rowsOut, err := v.adapter.api.query(ctx, v.key, cypher, params, true)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for _, r := range rowsOut {
			if e, ok := r["r"].(*edge); ok {
				pages[c.name] = append(pages[c.name], propStringValue(e.Properties[propRelationshipID]))
			}
		}
		t.Logf("PAGE %s %v", c.name, pages[c.name])
	}
	for _, pair := range [][2]string{{"current", "current_per_arm_limit"}, {"as_of_past", "as_of_past_per_arm_limit"}, {"current_limit100", "current_limit100_per_arm"}} {
		if strings.Join(pages[pair[0]], ",") != strings.Join(pages[pair[1]], ",") {
			t.Errorf("page %s != %s", pair[0], pair[1])
		}
	}
	for _, parallel := range []int{1, 4} {
		cypher, params := directEdgePageCypher(v.orgID, directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: v.now, Current: true})
		for _, variant := range []struct {
			name string
			text string
		}{{"current", v.text(t, cypher, params)}, {"current_per_arm_limit", v.text(t, edgePageCostPerArmLimit(cypher), params)}} {
			var wg sync.WaitGroup
			walls := make([]time.Duration, parallel)
			for i := 0; i < parallel; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					began := time.Now()
					if err := v.raw.Do(ctx, "GRAPH.RO_QUERY", v.key, variant.text).Err(); err != nil {
						t.Errorf("parallel %s: %v", variant.name, err)
					}
					walls[i] = time.Since(began)
				}(i)
			}
			wg.Wait()
			t.Logf("PARALLEL %d %s walls=%v", parallel, variant.name, walls)
		}
	}
}
