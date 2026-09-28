package falkorgraph_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func chaos7074Ctx(id string) context.Context {
	sum := sha256.Sum256([]byte(id))
	return observability.WithRequestID(context.Background(), "req_"+hex.EncodeToString(sum[:16]))
}

type chaos7074Fixture struct {
	adapter *falkorgraph.Adapter
	orgID   string
	now     time.Time
	// visible[principal class] = set of "rid|from" keys a current in-read of
	// team T's OWNED_BY_TEAM edges must return.
	allCurrent        map[string]bool
	restrictedCurrent map[string]bool
	expiredKey        string
	deniedEdgeKey     string
}

// seedChaos7074 projects, through the REAL ApplyProjectionBatch:
//
//   - team T owning acme/a and acme/b;
//   - 120 repositories r000..r119; every sixth is acme/b, the rest acme/a;
//     each has a current OWNED_BY_TEAM edge to T, authorized to its slug;
//   - 10 further edges in a second batch, with ids that sort between the
//     first batch's ids (the keyset must interleave them);
//   - one repository whose edge ENDED two hours ago (valid_to in the past);
//   - one acme/a repository whose edge to T is authorized to acme/b only
//     (two visible nodes, an edge the restricted caller may not see: T15);
//   - one work item in acme/a that BELONGS_TO_REPOSITORY r001 (depth 2).
func seedChaos7074(t *testing.T, ctx context.Context, adapter *falkorgraph.Adapter, orgID string) *chaos7074Fixture {
	t.Helper()
	now := time.Now().UTC()
	longAgo, ended := now.Add(-72*time.Hour), now.Add(-2*time.Hour)
	fixture := &chaos7074Fixture{adapter: adapter, orgID: orgID, now: now, allCurrent: map[string]bool{}, restrictedCurrent: map[string]bool{}}
	ref := func(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id}
	}
	team := ref(contextfabric.SubjectTeam, "team:T")
	entity := func(subject contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + strings.NewReplacer(":", "_", ".", "_").Replace(subject.CanonicalID)},
			ObservedAt: now, ValidFrom: &longAgo, SourceVersion: "v1",
		}
	}
	edge := func(id, relation string, from, to contextfabric.SubjectRef, slug string, validTo *time.Time) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: contextfabric.RelationshipType(relation), From: from, To: to,
			Derivation: contextfabric.DerivationRuleInferred, EpistemicStatus: contextfabric.EpistemicSourceAsserted,
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{slug}, TeamIDs: []string{"T"}},
			EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: now, ValidFrom: &longAgo, ValidTo: validTo, SourceVersion: "v1",
		}
	}
	entities := []contextfabric.EntityProjection{entity(team, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a", "acme/b"}, TeamIDs: []string{"T"}})}
	var first, second []contextfabric.RelationshipProjection
	slugOf := func(i int) string {
		if i%6 == 0 {
			return "acme/b"
		}
		return "acme/a"
	}
	for i := 0; i < 120; i++ {
		repo := ref(contextfabric.SubjectRepository, fmt.Sprintf("repository:r%03d", i))
		entities = append(entities, entity(repo, contextfabric.AuthorizationScope{RepositorySlugs: []string{slugOf(i)}}))
		rid := fmt.Sprintf("rel_own_%03d", (i*37)%120)
		first = append(first, edge(rid, "OWNED_BY_TEAM", repo, team, slugOf(i), nil))
		fixture.allCurrent[rid+"|"+repo.CanonicalID] = true
		if slugOf(i) == "acme/a" {
			fixture.restrictedCurrent[rid+"|"+repo.CanonicalID] = true
		}
	}
	for i := 0; i < 10; i++ {
		repo := ref(contextfabric.SubjectRepository, fmt.Sprintf("repository:d%02d", i))
		entities = append(entities, entity(repo, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}))
		rid := fmt.Sprintf("rel_own_%03d_b", i*11) // sorts right after rel_own_<i*11>
		second = append(second, edge(rid, "OWNED_BY_TEAM", repo, team, "acme/a", nil))
		fixture.allCurrent[rid+"|"+repo.CanonicalID] = true
		fixture.restrictedCurrent[rid+"|"+repo.CanonicalID] = true
	}
	expiredRepo := ref(contextfabric.SubjectRepository, "repository:expired")
	entities = append(entities, entity(expiredRepo, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}))
	first = append(first, edge("rel_own_expired", "OWNED_BY_TEAM", expiredRepo, team, "acme/a", &ended))
	fixture.expiredKey = "rel_own_expired|" + expiredRepo.CanonicalID
	deniedRepo := ref(contextfabric.SubjectRepository, "repository:edge-denied")
	entities = append(entities, entity(deniedRepo, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}))
	first = append(first, edge("rel_own_edge_denied", "OWNED_BY_TEAM", deniedRepo, team, "acme/b", nil))
	fixture.deniedEdgeKey = "rel_own_edge_denied|" + deniedRepo.CanonicalID
	fixture.allCurrent[fixture.deniedEdgeKey] = true
	// A repository NODE that ended two hours ago, with a current edge to T,
	// and a current work item w2 whose edge points at it: neither edge is
	// current, because an end node must be valid too (both node clauses).
	endedNode := ref(contextfabric.SubjectRepository, "repository:ended-node")
	endedEntity := entity(endedNode, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}})
	endedEntity.ValidTo = &ended
	entities = append(entities, endedEntity)
	first = append(first, edge("rel_own_ended_node", "OWNED_BY_TEAM", endedNode, team, "acme/a", nil))
	work2 := ref(contextfabric.SubjectWorkItem, "work_item.v2:w2")
	entities = append(entities, entity(work2, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}))
	first = append(first, edge("rel_work2_ended_repo", "BELONGS_TO_REPOSITORY", work2, endedNode, "acme/a", nil))
	work := ref(contextfabric.SubjectWorkItem, "work_item.v2:w1")
	entities = append(entities, entity(work, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}))
	first = append(first, edge("rel_work_repo", "BELONGS_TO_REPOSITORY", work, ref(contextfabric.SubjectRepository, "repository:r001"), "acme/a", nil))

	for index, batch := range [][]contextfabric.RelationshipProjection{nil, first, second} {
		b := contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_chaos7074_edges_%08d", index+1), OrgID: orgID, Source: "live-test",
			SourceVersion: "v1", Cursor: fmt.Sprintf("cursor-%d", index), NextCursor: fmt.Sprintf("cursor-%d", index+1), GeneratedAt: now,
			Entities: []contextfabric.EntityProjection{}, Relationships: batch,
			Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		}
		if index == 0 {
			b.Entities, b.Relationships = entities, []contextfabric.RelationshipProjection{}
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%d) error = %v", index, err)
		}
	}
	return fixture
}

func (f *chaos7074Fixture) reader() *directread.RelationshipsReader {
	return directread.NewRelationshipsReader(directread.NewSubjectGate(f.adapter, nil), f.adapter, nil)
}

func chaos7074ReadAll(t *testing.T, reader *directread.RelationshipsReader, principal storage.Principal, request directread.RelationshipsRequest) (map[string]int, []directread.RelationshipsResponse) {
	t.Helper()
	got := map[string]int{}
	var pages []directread.RelationshipsResponse
	for page := 0; page < 200; page++ {
		response, err := reader.Read(chaos7074Ctx(fmt.Sprintf("%s-%d-%s", t.Name(), page, request.Cursor)), principal, request)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		pages = append(pages, response)
		for _, e := range response.Edges {
			got[e.RelationshipID+"|"+e.From.CanonicalID]++
		}
		if response.Page.Complete {
			return got, pages
		}
		request.Cursor = response.Page.NextCursor
	}
	t.Fatal("walk did not end")
	return nil, nil
}

func chaos7074AssertSet(t *testing.T, got map[string]int, want map[string]bool) {
	t.Helper()
	var dup, extra, missing []string
	for key, n := range got {
		if n != 1 {
			dup = append(dup, key)
		}
		if !want[key] {
			extra = append(extra, key)
		}
	}
	for key := range want {
		if got[key] == 0 {
			missing = append(missing, key)
		}
	}
	sort.Strings(dup)
	sort.Strings(extra)
	sort.Strings(missing)
	if len(dup)+len(extra)+len(missing) > 0 {
		t.Fatalf("duplicates=%v extra=%v missing=%v", dup, extra, missing)
	}
}

// TestLiveChaos7074DirectEdges runs the bounded edge query and the whole
// read_relationships reader against a real FalkorDB. The unit tests' fake
// graph applies the keyset in Go; only this test can catch a Cypher that
// orders, filters or bounds wrongly.
func TestLiveChaos7074DirectEdges(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-chaos7074-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	fixture := seedChaos7074(t, ctx, adapter, orgID)
	unrestricted := storage.Principal{OrgID: orgID, Subject: "u", CredentialID: "c"}
	restricted := storage.Principal{OrgID: orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/a"}}
	teamIn := directread.RelationshipsRequest{Subject: directread.RelationshipsSubject{Kind: "team", CanonicalID: "team:T"}, Types: []string{"OWNED_BY_TEAM"}, Direction: "in", Limit: 25}

	// T5 live: pages join to the full visible set, with shared relationship
	// ids, no duplicate and no gap; the ended edge is not current.
	t.Run("T5 pages join, unrestricted", func(t *testing.T) {
		got, pages := chaos7074ReadAll(t, fixture.reader(), unrestricted, teamIn)
		chaos7074AssertSet(t, got, fixture.allCurrent)
		if len(pages) < 5 {
			t.Fatalf("only %d pages for %d edges at limit 25", len(pages), len(fixture.allCurrent))
		}
	})
	t.Run("T5 pages join, restricted", func(t *testing.T) {
		got, pages := chaos7074ReadAll(t, fixture.reader(), restricted, teamIn)
		chaos7074AssertSet(t, got, fixture.restrictedCurrent)
		withheld := 0
		for _, page := range pages {
			withheld += page.Withheld.EdgesNotVisible
		}
		// 20 acme/b repositories (hidden source node) + the edge-denied edge.
		if withheld != 21 {
			t.Fatalf("edges_not_visible total = %d, want 21", withheld)
		}
	})
	// T15 live: two visible nodes (an acme/a repository, team T), one edge
	// authorized to acme/b only: the restricted caller does not get it.
	t.Run("T15 edge gate on real attributes", func(t *testing.T) {
		got, _ := chaos7074ReadAll(t, fixture.reader(), restricted, directread.RelationshipsRequest{
			Subject: directread.RelationshipsSubject{Kind: "repository", CanonicalID: "repository:edge-denied"},
		})
		if len(got) != 0 {
			t.Fatalf("edge-denied repository served %v", got)
		}
		got, _ = chaos7074ReadAll(t, fixture.reader(), unrestricted, directread.RelationshipsRequest{
			Subject: directread.RelationshipsSubject{Kind: "repository", CanonicalID: "repository:edge-denied"},
		})
		if got[fixture.deniedEdgeKey] != 1 {
			t.Fatalf("unrestricted caller lost the edge: %v", got)
		}
	})
	// An ended edge is not current; at an instant inside its window it is.
	t.Run("ended edge is read only at a time it was valid", func(t *testing.T) {
		expired := directread.RelationshipsRequest{Subject: directread.RelationshipsSubject{Kind: "repository", CanonicalID: "repository:expired"}}
		if got, _ := chaos7074ReadAll(t, fixture.reader(), unrestricted, expired); len(got) != 0 {
			t.Fatalf("current read returned an ended edge: %v", got)
		}
		expired.AsOf = fixture.now.Add(-3 * time.Hour).Format(time.RFC3339Nano)
		if got, _ := chaos7074ReadAll(t, fixture.reader(), unrestricted, expired); got[fixture.expiredKey] != 1 {
			t.Fatalf("as_of read inside the window lost the edge: %v", got)
		}
	})
	t.Run("ended end node is not current", func(t *testing.T) {
		out := directread.RelationshipsRequest{Subject: directread.RelationshipsSubject{Kind: "work_item", CanonicalID: "work_item.v2:w2"}, Direction: "out"}
		if got, _ := chaos7074ReadAll(t, fixture.reader(), unrestricted, out); len(got) != 0 {
			t.Fatalf("edge into an ended node served as current: %v", got)
		}
		out.AsOf = fixture.now.Add(-3 * time.Hour).Format(time.RFC3339Nano)
		if got, _ := chaos7074ReadAll(t, fixture.reader(), unrestricted, out); got["rel_work2_ended_repo|work_item.v2:w2"] != 1 {
			t.Fatalf("as_of inside the node window lost the edge: %v", got)
		}
	})
	t.Run("direction", func(t *testing.T) {
		out := teamIn
		out.Direction = "out"
		if got, _ := chaos7074ReadAll(t, fixture.reader(), unrestricted, out); len(got) != 0 {
			t.Fatalf("team has no outgoing ownership edge; got %v", got)
		}
		both := teamIn
		both.Direction = "both"
		got, _ := chaos7074ReadAll(t, fixture.reader(), unrestricted, both)
		chaos7074AssertSet(t, got, fixture.allCurrent)
	})
	t.Run("depth 2 reaches the work item through a visible repository", func(t *testing.T) {
		request := directread.RelationshipsRequest{Subject: directread.RelationshipsSubject{Kind: "team", CanonicalID: "team:T"}, Depth: 2, Limit: 100}
		got, pages := chaos7074ReadAll(t, fixture.reader(), restricted, request)
		if got["rel_work_repo|work_item.v2:w1"] != 1 {
			t.Fatalf("hop-2 edge missing: pages=%d", len(pages))
		}
		want := map[string]bool{"rel_work_repo|work_item.v2:w1": true}
		for key := range fixture.restrictedCurrent {
			want[key] = true
		}
		chaos7074AssertSet(t, got, want)
	})
	// r001 has an OWNED_BY_TEAM edge (to T) and a BELONGS_TO_REPOSITORY edge
	// (from the work item): the type filter keeps exactly the one asked for.
	t.Run("type filter", func(t *testing.T) {
		request := directread.RelationshipsRequest{Subject: directread.RelationshipsSubject{Kind: "repository", CanonicalID: "repository:r001"}}
		got, _ := chaos7074ReadAll(t, fixture.reader(), unrestricted, request)
		if len(got) != 2 {
			t.Fatalf("unfiltered r001 edges = %v, want 2", got)
		}
		request.Types = []string{"BELONGS_TO_REPOSITORY"}
		got, _ = chaos7074ReadAll(t, fixture.reader(), unrestricted, request)
		chaos7074AssertSet(t, got, map[string]bool{"rel_work_repo|work_item.v2:w1": true})
	})
	t.Run("another organization reads nothing", func(t *testing.T) {
		other := storage.Principal{OrgID: orgID + "-other", Subject: "u", CredentialID: "c"}
		response, err := fixture.reader().Read(chaos7074Ctx("other-org"), other, teamIn)
		if err != nil || response.Status != directread.RelationshipsDenied || len(response.Edges) != 0 {
			t.Fatalf("foreign org: %v %+v", err, response)
		}
	})
	// The raw page: a limit of 1 returns exactly one edge and More, and the
	// graph never returns an edge at or before the keyset position.
	t.Run("raw page bound and keyset", func(t *testing.T) {
		binding, err := adapter.ResolveInvestigationBinding(ctx, unrestricted)
		if err != nil {
			t.Fatal(err)
		}
		query := directread.EdgePageQuery{
			Origins:   []contextfabric.SubjectRef{{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T"}},
			Direction: directread.EdgeDirectionIn, Limit: 1, ValidAt: time.Now().UTC(),
		}
		var previous *directread.EdgeKey
		seen := 0
		for {
			page, err := adapter.DirectEdgePage(ctx, unrestricted, binding, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Edges) > 1 {
				t.Fatalf("limit 1 returned %d", len(page.Edges))
			}
			if len(page.Edges) == 0 {
				break
			}
			key := page.Edges[0].Key
			if previous != nil && !previous.Less(key) {
				t.Fatalf("keyset went backwards: %+v then %+v", *previous, key)
			}
			previous, seen = &key, seen+1
			if !page.More {
				break
			}
			query.After = &key
		}
		if seen != len(fixture.allCurrent) {
			t.Fatalf("raw walk saw %d edges, want %d", seen, len(fixture.allCurrent))
		}
	})
}
