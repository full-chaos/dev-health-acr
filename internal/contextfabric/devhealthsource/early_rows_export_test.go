package devhealthsource

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// EarlyRowsWalksForTest builds, for one organization, the batches of the same
// from-zero walk twice: with the complete tables in its first batch, and
// alone. The walk has three tables over the snapshot cap (repositories, old;
// old edges, old; work items, new) and two complete tables: new edges from a
// work item to a repository, and tombstones that retire old edges and
// repositories. Some repository tombstones sort before the new edge that
// names the repository and some after. So the first batch of the first walk
// applies edges before their endpoint entities and tombstones before the rows
// they retire. (A tombstone never retires an edge of the complete table: the
// contract refuses a batch that asserts and retracts one edge, and the source
// then leaves the tables to the walk.)
//
// untombstoned is the walk alone over the same tables less the tombstone
// table: what a walk emits when no tombstone ever comes with a page.
func EarlyRowsWalksForTest(t *testing.T, orgID string) (with, alone, untombstoned []contextfabric.ProjectionBatch) {
	t.Helper()
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	repoKey := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
	itemKey := func(n int) string { return fmt.Sprintf("item-%04d", n) }
	edgeID := func(n int) string { return fmt.Sprintf("relationship.v2:early:%04d", n) }
	oldEdgeID := func(n int) string { return fmt.Sprintf("relationship.v2:old:%04d", n) }
	repoOf := func(edge int) int { return edge * 4 }

	repos := &liveKeysetRows{}
	for n := 1; n <= 400; n++ {
		repos.rows = append(repos.rows, keysetRow{at: now.Add(-90 * 24 * time.Hour).Add(time.Duration(n) * time.Hour), key: repoKey(n)})
	}
	items, edges, oldEdges, tombstones := &liveKeysetRows{}, &liveKeysetRows{}, &liveKeysetRows{}, &liveKeysetRows{}
	for n := 1; n <= snapshotPerQueryCap+10; n++ {
		oldEdges.rows = append(oldEdges.rows, keysetRow{at: now.Add(-85 * 24 * time.Hour).Add(time.Duration(n) * time.Minute), key: fmt.Sprintf("%04d", n)})
	}
	for n := 1; n <= snapshotPerQueryCap+10; n++ {
		items.rows = append(items.rows, keysetRow{at: now.Add(-9 * time.Minute).Add(time.Duration(n) * time.Second), key: itemKey(n)})
	}
	for n := 1; n <= 100; n++ {
		edges.rows = append(edges.rows, keysetRow{at: now.Add(-20 * time.Minute).Add(time.Duration(n) * time.Second), key: fmt.Sprintf("%04d", n)})
	}
	// Tombstone rows: key = "<kind>|<id>". 1-10 retire old edges (of the
	// second page: not rows the first batch asserts); 11-15
	// retire repositories after every new edge; 16-20 retire repositories
	// before the new edge that names them.
	for n := 1; n <= 20; n++ {
		at := now.Add(-15 * time.Minute)
		var key string
		switch {
		case n <= 10:
			key = "relationship|" + oldEdgeID(snapshotPerQueryCap+n)
		case n <= 15:
			key = "repository|repository:" + repoKey(repoOf(60+n))
		default:
			at, key = now.Add(-25*time.Minute), "repository|repository:"+repoKey(repoOf(64+n))
		}
		tombstones.rows = append(tombstones.rows, keysetRow{at: at.Add(time.Duration(n) * time.Second), key: key})
	}

	scope := func(n int) contractsv1.ContextFabricAuthorizationScope {
		return repoAuthorization("acme/" + repoKey(n))
	}
	repoRef := func(n int) contractsv1.ContextFabricSubjectRef {
		return contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + repoKey(n), Label: "acme/" + repoKey(n)}
	}
	table := func(name string, store *liveKeysetRows, scan func(at time.Time, key string) candidate) entityTable {
		return entityTable{name: name, query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
			return fetch(ctx, store, "", rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
				var at time.Time
				var key string
				if err := r.Scan(&at, &key); err != nil {
					return nil, err
				}
				return []candidate{scan(at, key)}, nil
			})
		}}
	}
	itemTable := table("items", items, func(at time.Time, key string) candidate {
		return candidate{observedAt: at, sortKey: key, entity: &contractsv1.ContextFabricEntityProjection{
			Subject:       contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + key, Label: key},
			Authorization: scope(1), EvidenceRefIDs: []string{"acr:v1:early:item:" + key},
			ObservedAt: at, ValidFrom: requiredTime(at), SourceVersion: ClickHouseSourceVersion,
		}}
	})
	edge := func(at time.Time, key, id string, item, repo int) candidate {
		return candidate{observedAt: at, sortKey: key, relationship: &contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: id, Type: contractsv1.ContextFabricRelationshipBelongsToRepository,
			From:       contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + itemKey(item), Label: itemKey(item)},
			To:         repoRef(repo),
			Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization: scope(repo), EvidenceRefIDs: []string{"acr:v1:early:edge:" + id},
			ObservedAt: at, ValidFrom: requiredTime(at), SourceVersion: ClickHouseSourceVersion,
		}}
	}
	edgeTable := table("edges", edges, func(at time.Time, key string) candidate {
		var n int
		fmt.Sscanf(key, "%d", &n)
		return edge(at, key, edgeID(n), n, repoOf(n))
	})
	oldEdgeTable := table("old_edges", oldEdges, func(at time.Time, key string) candidate {
		var n int
		fmt.Sscanf(key, "%d", &n)
		return edge(at, key, oldEdgeID(n), 1000+n, n)
	})
	tombstoneTable := table("tombstones", tombstones, func(at time.Time, key string) candidate {
		kind, id := key, ""
		for i := range key {
			if key[i] == '|' {
				kind, id = key[:i], key[i+1:]
				break
			}
		}
		return candidate{observedAt: at, sortKey: key, tombstone: &contractsv1.ContextFabricProjectionTombstone{
			Kind: kind, CanonicalID: id, Reason: "retired", EffectiveAt: now, SourceVersion: ClickHouseSourceVersion,
		}}
	})
	plan := func() sourcePlan {
		return sourcePlan{
			client: keysetRows{}, source: "early_rows_test", version: ClickHouseSourceVersion,
			tables: []entityTable{namedRepositoryTable("repos", repos), oldEdgeTable, itemTable, edgeTable, tombstoneTable},
			now:    func() time.Time { return now }, overlap: defaultReprojectOverlap, window: newWindowMemo(),
		}
	}
	drain := func(p sourcePlan, first func(sourcePlan) (contextfabric.ProjectionBatch, bool, error)) []contextfabric.ProjectionBatch {
		var batches []contextfabric.ProjectionBatch
		batch, available, err := first(p)
		for calls := 0; available && calls < 50; calls++ {
			if err != nil {
				t.Fatal(err)
			}
			batches = append(batches, batch)
			batch, available, err = p.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: p.source, Cursor: batch.NextCursor})
		}
		if err != nil {
			t.Fatal(err)
		}
		return batches
	}
	with = drain(plan(), func(p sourcePlan) (contextfabric.ProjectionBatch, bool, error) {
		return p.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: p.source})
	})
	walkAlone := func(p sourcePlan) (contextfabric.ProjectionBatch, bool, error) {
		p.windowScope = windowScopeFor(orgID, 0)
		return p.pagedBatch(context.Background(), orgID, "", cursorState{}, true)
	}
	alone = drain(plan(), walkAlone)
	less := plan()
	less.tables = less.tables[:len(less.tables)-1]
	untombstoned = drain(less, walkAlone)
	return with, alone, untombstoned
}

// The fixture of the container-backed row does what that row says, with no
// container: the first batch of the walk with the complete tables carries
// every edge and every tombstone, ahead of most of the entities they name,
// and the walk alone carries none of them in its first batch.
func TestEarlyRowsWalksFixture(t *testing.T) {
	with, alone, untombstoned := EarlyRowsWalksForTest(t, "org")
	for _, batch := range untombstoned {
		if len(batch.Tombstones) != 0 {
			t.Fatalf("the walk without the tombstone table carries %d tombstones", len(batch.Tombstones))
		}
	}
	if !reflect.DeepEqual(untombstoned[0].Entities, alone[0].Entities) || !reflect.DeepEqual(untombstoned[0].Relationships, alone[0].Relationships) {
		t.Fatal("the first page of the walk without the tombstone table is not the first page of the walk alone: its later batches would not continue the first batch")
	}
	if len(with) != len(alone) || len(with) < 4 {
		t.Fatalf("batches: %d with the complete tables, %d alone; want the same number, 4 or more", len(with), len(alone))
	}
	newEdges := func(batch contextfabric.ProjectionBatch) (n int) {
		for _, r := range batch.Relationships {
			if strings.HasPrefix(r.RelationshipID, "relationship.v2:early:") {
				n++
			}
		}
		return n
	}
	if got, want := newEdges(with[0]), 100; got != want {
		t.Fatalf("the first batch carries %d of the new edges, want %d", got, want)
	}
	if got, want := len(with[0].Tombstones), 20; got != want {
		t.Fatalf("the first batch carries %d tombstones, want %d", got, want)
	}
	for _, e := range with[0].Entities {
		if e.Subject.Kind == contractsv1.ContextFabricSubjectWorkItem {
			t.Fatalf("the first batch carries the work item %s: the edges must come before their work items", e.Subject.CanonicalID)
		}
	}
	if newEdges(alone[0])+len(alone[0].Tombstones) != 0 {
		t.Fatalf("the walk alone carries %d new edges and %d tombstones in its first batch, want none", newEdges(alone[0]), len(alone[0].Tombstones))
	}
	edges, tombstones := 0, 0
	for _, batch := range with[1:] {
		edges += newEdges(batch)
		tombstones += len(batch.Tombstones)
	}
	if edges != 100 || tombstones != 20 {
		t.Fatalf("the later batches carry %d edges and %d tombstones, want the 100 new edges and the 20 tombstones again", edges, tombstones)
	}
}
