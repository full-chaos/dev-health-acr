package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FalkorDB/falkordb-go/v2"
	"github.com/redis/go-redis/v9"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	nodeEdgesCostItems = 1200
	nodeEdgesCostRepos = 20
	// the single-pattern in arm scans the organization's 1222 nodes, fewer under a window
	nodeEdgesCostLegacyScanFloor = 100
)

type nodeEdgesCostVenue struct {
	adapter *Adapter
	raw     *redis.Client
	key     string
	orgID   string
	now     time.Time
	team    contextfabric.SubjectRef
	repos   []contextfabric.SubjectRef
}

func nodeEdgesCostRID(prefix string, i int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", prefix, i)))
	return "rel_" + hex.EncodeToString(sum[:8])
}

func nodeEdgesCostItem(i int) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item.v2:w%05d", i), Label: fmt.Sprintf("Work item %d", i)}
}

func nodeEdgesCostSeed(t *testing.T, ctx context.Context) *nodeEdgesCostVenue {
	t.Helper()
	adapter, addr := newLiveFalkorAdapter(t, ctx)
	v := &nodeEdgesCostVenue{adapter: adapter}
	v.raw = redis.NewClient(&redis.Options{Addr: addr, ReadTimeout: 60 * time.Second})
	t.Cleanup(func() { _ = v.raw.Close() })
	v.orgID = "live-node-edges-cost-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), v.orgID) })
	v.now = time.Now().UTC()
	start := v.now.Add(-600 * 24 * time.Hour)
	v.team = contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T", Label: "T"}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:U", Label: "U"}
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
	entities := []contextfabric.EntityProjection{entity(v.team, &start, nil), entity(other, &start, nil)}
	var relationships []contextfabric.RelationshipProjection
	for i := 0; i < nodeEdgesCostRepos; i++ {
		repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: fmt.Sprintf("repository:r%04d", i), Label: fmt.Sprintf("acme/r%04d", i)}
		v.repos = append(v.repos, repo)
		entities = append(entities, entity(repo, &start, nil))
		relationships = append(relationships, relation(nodeEdgesCostRID("own-repo", i), "OWNED_BY_TEAM", repo, v.team, &start, nil))
	}
	for i := 0; i < nodeEdgesCostItems; i++ {
		created := v.now.Add(-time.Duration(i%500+5) * 24 * time.Hour)
		var ended *time.Time
		if i%5 != 0 {
			e := created.Add(time.Duration(i%30+1) * 24 * time.Hour)
			if e.After(v.now) {
				e = v.now.Add(-time.Hour)
			}
			ended = &e
		}
		owner := v.team
		if i%2 == 1 {
			owner = other
		}
		entities = append(entities, entity(nodeEdgesCostItem(i), &created, ended))
		relationships = append(relationships,
			relation(nodeEdgesCostRID("own-item", i), "OWNED_BY_TEAM", nodeEdgesCostItem(i), owner, nil, nil),
			relation(nodeEdgesCostRID("item-repo", i), "BELONGS_TO_REPOSITORY", nodeEdgesCostItem(i), v.repos[i%len(v.repos)], &created, ended),
			relation(nodeEdgesCostRID("item-item", i), "RELATES_TO", nodeEdgesCostItem(i), nodeEdgesCostItem((i+1)%nodeEdgesCostItems), nil, nil))
	}
	apply := func(index int, entities []contextfabric.EntityProjection, relationships []contextfabric.RelationshipProjection) {
		b := contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_node_edges_cost_%08d", index), OrgID: v.orgID, Source: "live-test",
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
	return v
}

func (v *nodeEdgesCostVenue) profile(t *testing.T, ctx context.Context, cypher string, params map[string]interface{}) []string {
	t.Helper()
	safe, err := safeParams(params)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := v.raw.Do(ctx, "GRAPH.PROFILE", v.key, falkordb.BuildParamsHeader(safe)+cypher).Result()
	if err != nil {
		t.Fatalf("GRAPH.PROFILE: %v", err)
	}
	var lines []string
	var walk func(interface{})
	walk = func(x interface{}) {
		switch value := x.(type) {
		case []interface{}:
			for _, item := range value {
				walk(item)
			}
		default:
			lines = append(lines, fmt.Sprint(value))
		}
	}
	walk(reply)
	return lines
}

var nodeEdgesCostRecords = regexp.MustCompile(`^\s*(Node By Index Scan|Conditional Traverse|Node By Label Scan|All Node Scan)\b.*Records produced: (\d+)`)

func nodeEdgesCostReadWork(lines []string) (scanned, traversed int) {
	for _, line := range lines {
		m := nodeEdgesCostRecords.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if m[1] == "Conditional Traverse" {
			traversed += n
		} else {
			scanned += n
		}
	}
	return scanned, traversed
}

func nodeEdgesLegacyInArm(cypher string) string {
	origin := fmt.Sprintf("(n:%s {%s:$org, %s:$kind, %s:$id})", labelSubject, propOrgID, propKind, propCanonicalID)
	other := fmt.Sprintf("(other:%s {%s:$org})", labelSubject, propOrgID)
	return strings.Replace(cypher, "MATCH "+origin+" WITH n MATCH "+other+"-[r:"+labelRelation+"]->(n)", "MATCH "+other+"-[r:"+labelRelation+"]->"+origin, 1)
}

func nodeEdgesFingerprint(edges []graphrank.CandidateEdge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.UUID+" "+e.SourceNodeUUID+" -> "+e.TargetNodeUUID)
	}
	sort.Strings(out)
	return out
}

func TestNodeEdgesReadOnlyTheNodesOwnEdges(t *testing.T) {
	ctx := context.Background()
	began := time.Now()
	v := nodeEdgesCostSeed(t, ctx)
	t.Logf("seeded %d work items, %d repositories in %s", nodeEdgesCostItems, nodeEdgesCostRepos, time.Since(began))

	leaf := nodeEdgesCostItem(7)
	leafUUID := subjectUUID(string(leaf.Kind), leaf.CanonicalID)
	itemUUID := func(i int) string {
		item := nodeEdgesCostItem(i)
		return subjectUUID(string(item.Kind), item.CanonicalID)
	}
	wantLeaf := nodeEdgesFingerprint([]graphrank.CandidateEdge{
		{UUID: nodeEdgesCostRID("own-item", 7), SourceNodeUUID: leafUUID, TargetNodeUUID: subjectUUID(string(contextfabric.SubjectTeam), "team:U")},
		{UUID: nodeEdgesCostRID("item-repo", 7), SourceNodeUUID: leafUUID, TargetNodeUUID: subjectUUID(string(contextfabric.SubjectRepository), v.repos[7].CanonicalID)},
		{UUID: nodeEdgesCostRID("item-item", 7), SourceNodeUUID: leafUUID, TargetNodeUUID: itemUUID(8)},
		{UUID: nodeEdgesCostRID("item-item", 6), SourceNodeUUID: itemUUID(6), TargetNodeUUID: leafUUID},
	})
	window := temporalFilter{active: true, startNs: nsTimestamp(v.now.Add(-120 * 24 * time.Hour)), endNs: nsTimestamp(v.now)}
	cases := []struct {
		name     string
		subject  contextfabric.SubjectRef
		temporal temporalFilter
		degree   int
		in       int
		want     []string
	}{
		{"leaf_current", leaf, temporalFilter{}, 4, 1, wantLeaf},
		{"leaf_range", leaf, window, 4, 1, wantLeaf},
		{"repository_range", v.repos[7], window, 1 + nodeEdgesCostItems/nodeEdgesCostRepos, -1, nil},
		{"hub_current", v.team, temporalFilter{}, nodeEdgesCostItems/2 + nodeEdgesCostRepos, nodeEdgesCostItems/2 + nodeEdgesCostRepos, nil},
	}
	for _, c := range cases {
		uuid := subjectUUID(string(c.subject.Kind), c.subject.CanonicalID)
		cypher := nodeEdgesCypher(c.temporal)
		legacy := nodeEdgesLegacyInArm(cypher)
		if legacy == cypher {
			t.Fatalf("%s: the single-pattern in arm could not be rebuilt from the statement: the comparison measured nothing\n%s", c.name, cypher)
		}
		params := c.temporal.bind(map[string]interface{}{"org": v.orgID, "kind": string(c.subject.Kind), "id": c.subject.CanonicalID})

		served, err := v.adapter.edgesOfNode(ctx, v.key, v.orgID, uuid, c.temporal)
		if err != nil {
			t.Fatalf("%s: edgesOfNode: %v", c.name, err)
		}
		legacyRows, err := v.adapter.api.query(ctx, v.key, legacy, params, true)
		if err != nil {
			t.Fatalf("%s: single-pattern reference: %v", c.name, err)
		}
		got, reference := nodeEdgesFingerprint(served), nodeEdgesFingerprint(nodeEdgesFromRows(legacyRows))
		if len(got) == 0 || strings.Join(got, "\n") != strings.Join(reference, "\n") {
			t.Errorf("%s: %d edges differ from the %d of the single-pattern reference\n got %v\nwant %v", c.name, len(got), len(reference), got, reference)
		}
		if c.want != nil && strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: edges\n got %v\nwant %v", c.name, got, c.want)
		}
		in := 0
		for _, e := range served {
			if e.TargetNodeUUID == uuid {
				in++
			}
		}
		if c.in >= 0 && in != c.in {
			t.Errorf("%s: %d edges end at the node, want %d", c.name, in, c.in)
		}

		plan := v.profile(t, ctx, cypher, params)
		scanned, traversed := nodeEdgesCostReadWork(plan)
		legacyPlan := v.profile(t, ctx, legacy, params)
		legacyScanned, legacyTraversed := nodeEdgesCostReadWork(legacyPlan)
		t.Logf("CASE %s edges=%d in=%d degree=%d read work after scanned=%d traversed=%d, before scanned=%d traversed=%d\nPROFILE after %s\n%s\nPROFILE before %s\n%s",
			c.name, len(got), in, c.degree, scanned, traversed, legacyScanned, legacyTraversed, c.name, strings.Join(plan, "\n"), c.name, strings.Join(legacyPlan, "\n"))
		if scanned == 0 || traversed == 0 {
			t.Errorf("%s: the profile named no scan or no traverse (scanned %d, traversed %d): the bound measured nothing", c.name, scanned, traversed)
		}
		if scanned > 2 || traversed > 2*c.degree {
			t.Errorf("%s: the edge read went beyond the node: scanned %d nodes for one origin, traversed %d edges for degree %d", c.name, scanned, traversed, c.degree)
		}
		if legacyScanned < nodeEdgesCostLegacyScanFloor {
			t.Errorf("%s: the single-pattern in arm scanned only %d nodes: the control no longer shows the organization-wide scan this bound exists to catch", c.name, legacyScanned)
		}
	}
}
