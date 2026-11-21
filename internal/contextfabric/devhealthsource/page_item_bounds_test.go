package devhealthsource

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var fanOutStamp = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// fanOutIncident is one incident row: how many active and ended repository
// mappings its service carries.
type fanOutIncident struct {
	id            string
	active, ended int
}

func fanOutActiveRepo(n int) string { return fmt.Sprintf("6c2f9a10-4b7e-4d21-9f3a-%012d", n) }
func fanOutEndedRepo(n int) string  { return fmt.Sprintf("7d2f9a10-4b7e-4d21-9f3a-%012d", n) }

func fanOutIncidentID(n int) string { return fmt.Sprintf("8e4b1d70-2c9a-4f63-b815-%012d", n) }

// fanOutTable reads incident rows through the real fetch and the real
// incidentRowCandidates, one keyset position per incident.
func fanOutTable(incidents []fanOutIncident) entityTable {
	byID := map[string]fanOutIncident{}
	rows := make(keysetRows, 0, len(incidents))
	for _, incident := range incidents {
		byID[incident.id] = incident
		rows = append(rows, keysetRow{at: fanOutStamp, key: incident.id})
	}
	return entityTable{name: "incidents", query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		var stats incidentPageStats
		return fetch(ctx, rows, "", rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
			var at time.Time
			var id string
			if err := r.Scan(&at, &id); err != nil {
				return nil, err
			}
			incident := byID[id]
			active := make([]string, 0, incident.active)
			for n := 0; n < incident.active; n++ {
				active = append(active, fmt.Sprintf("%s|acme/r%05d", fanOutActiveRepo(n), n))
			}
			ended := make([]string, 0, incident.ended)
			for n := 0; n < incident.ended; n++ {
				ended = append(ended, fanOutEndedRepo(n))
			}
			out := incidentRowCandidates(incidentRow{
				id: id, title: "incident " + id, activeRepos: parseIncidentRepos(active), endedRepos: ended,
				observedAt: at, cursorAt: at,
			}, &stats)
			for i := range out {
				out[i].cursorAt = at
			}
			return out, nil
		})
	}}
}

type batchCounts struct{ Entities, Relationships, Tombstones int }

type fanOutWalk struct {
	batches       []batchCounts
	relationships map[string]int
	tombstones    map[string]int
	quarantined   map[string]int
}

// walkFanOut runs the paged read from cursor zero, carrying each batch's
// NextCursor into the next call, until a call publishes nothing.
func walkFanOut(t *testing.T, incidents []fanOutIncident) fanOutWalk {
	t.Helper()
	walk := fanOutWalk{relationships: map[string]int{}, tombstones: map[string]int{}, quarantined: map[string]int{}}
	plan := sourcePlan{
		client: keysetRows{}, source: "page_item_bounds_test", version: "v1", tables: []entityTable{fanOutTable(incidents)},
		observeQuarantine: func(o quarantineObservation) { walk.quarantined[o.Kind+"/"+o.Reason]++ },
	}
	cursor, state := "", cursorState{}
	for call := 1; ; call++ {
		if call > 20 {
			t.Fatalf("the read did not catch up in 20 calls; %d batches", len(walk.batches))
		}
		batch, published, err := plan.pagedBatch(context.Background(), "org", cursor, state, false)
		if err != nil {
			t.Fatalf("call %d at cursor %q: %v", call, cursor, err)
		}
		if !published {
			return walk
		}
		if err := batch.Validate(); err != nil {
			t.Fatalf("call %d built an invalid batch: %v", call, err)
		}
		if batch.NextCursor == cursor {
			t.Fatalf("call %d did not move the cursor", call)
		}
		walk.batches = append(walk.batches, batchCounts{
			Entities: len(batch.Entities), Relationships: len(batch.Relationships), Tombstones: len(batch.Tombstones),
		})
		for _, r := range batch.Relationships {
			walk.relationships[r.RelationshipID]++
		}
		for _, tomb := range batch.Tombstones {
			walk.tombstones[tomb.CanonicalID]++
		}
		cursor = batch.NextCursor
		if state, err = decodeCursor(cursor); err != nil {
			t.Fatal(err)
		}
		state.Ack = ""
	}
}

// wantFanOut is what a from-zero build of the incidents projects: one edge
// per active repository and one retraction per ended one, per incident.
func wantFanOut(incidents []fanOutIncident) (relationships, tombstones []string) {
	for _, incident := range incidents {
		canonical := "incident:" + incident.id
		for n := 0; n < incident.active; n++ {
			relationships = append(relationships, incidentRepositoryRelationshipID(canonical, fanOutActiveRepo(n)))
		}
		for n := 0; n < incident.ended; n++ {
			tombstones = append(tombstones, incidentRepositoryRelationshipID(canonical, fanOutEndedRepo(n)))
		}
	}
	return relationships, tombstones
}

func requireOnce(t *testing.T, what string, got map[string]int, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: projected %d distinct, want %d", what, len(got), len(want))
	}
	for _, id := range want {
		if got[id] != 1 {
			t.Fatalf("%s %s projected %d times, want once", what, id, got[id])
		}
	}
}

func batchShape(walk fanOutWalk) []string {
	out := make([]string, len(walk.batches))
	for i, b := range walk.batches {
		out[i] = fmt.Sprintf("e%d/r%d/t%d", b.Entities, b.Relationships, b.Tombstones)
	}
	return out
}

func incidentsOf(count, active, ended int) []fanOutIncident {
	out := make([]fanOutIncident, count)
	for n := range out {
		out[n] = fanOutIncident{id: fanOutIncidentID(n), active: active, ended: ended}
	}
	return out
}

// A page whose rows together pass the batch's relationship or tombstone bound
// is cut before the row that would pass it, so the read publishes valid
// batches and catches up with every edge and retraction of a from-zero build.
func TestHighFanOutIncidentPagesStayInsideTheBatchBounds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		incidents []fanOutIncident
		batches   []string
	}{
		{name: "two incidents over the relationship bound together", incidents: incidentsOf(2, 2600, 0),
			batches: []string{"e1/r2600/t0", "e1/r2600/t0"}},
		{name: "two hundred incidents of one service with 26 repositories", incidents: incidentsOf(200, 26, 0),
			batches: []string{"e192/r4992/t0", "e8/r208/t0"}},
		{name: "two incidents over the tombstone bound together", incidents: incidentsOf(2, 1, 2600),
			batches: []string{"e1/r1/t2600", "e1/r1/t2600"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			walk := walkFanOut(t, tc.incidents)
			relationships, tombstones := wantFanOut(tc.incidents)
			requireOnce(t, "relationship", walk.relationships, relationships)
			requireOnce(t, "tombstone", walk.tombstones, tombstones)
			if got := batchShape(walk); fmt.Sprint(got) != fmt.Sprint(tc.batches) {
				t.Fatalf("batches %v, want %v", got, tc.batches)
			}
			if len(walk.quarantined) != 0 {
				t.Fatalf("quarantined %v, want nothing", walk.quarantined)
			}
		})
	}
}

// The cut is exact: a page that reaches the bound stays one batch, one item
// past it splits, and a first row that alone fills the bound still publishes.
func TestPageItemBoundIsInclusive(t *testing.T) {
	for _, tc := range []struct {
		name      string
		incidents []fanOutIncident
		batches   []string
	}{
		{name: "exactly the relationship bound", incidents: incidentsOf(2, 2500, 0), batches: []string{"e2/r5000/t0"}},
		{name: "one pair past the relationship bound", incidents: incidentsOf(2, 2501, 0), batches: []string{"e1/r2501/t0", "e1/r2501/t0"}},
		{name: "exactly the tombstone bound", incidents: incidentsOf(2, 1, 2500), batches: []string{"e2/r2/t5000"}},
		{name: "one first row that fills the relationship bound", incidents: incidentsOf(1, 5000, 0), batches: []string{"e1/r5000/t0"}},
		{name: "a first row that fills the bound, then one more row", incidents: incidentsOf(2, 5000, 0), batches: []string{"e1/r5000/t0", "e1/r5000/t0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			walk := walkFanOut(t, tc.incidents)
			relationships, tombstones := wantFanOut(tc.incidents)
			requireOnce(t, "relationship", walk.relationships, relationships)
			requireOnce(t, "tombstone", walk.tombstones, tombstones)
			if got := batchShape(walk); fmt.Sprint(got) != fmt.Sprint(tc.batches) {
				t.Fatalf("batches %v, want %v", got, tc.batches)
			}
		})
	}
}

// One row whose own items pass a bound cannot be cut: the items past the
// bound are quarantined as a contract bound violation, the row publishes what
// fits, and the read moves on to the next row.
func TestSingleRowPastABatchBoundQuarantinesTheOverflow(t *testing.T) {
	incidents := []fanOutIncident{
		{id: fanOutIncidentID(0), active: 1, ended: 5003},
		{id: fanOutIncidentID(1), active: 2, ended: 0},
	}
	walk := walkFanOut(t, incidents)
	if got, want := batchShape(walk), []string{"e1/r1/t5000", "e1/r2/t0"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("batches %v, want %v", got, want)
	}
	if want := map[string]int{"tombstone/" + quarantineContractBoundViolation: 3}; fmt.Sprint(walk.quarantined) != fmt.Sprint(want) {
		t.Fatalf("quarantined %v, want %v", walk.quarantined, want)
	}
	relationships, tombstones := wantFanOut(incidents)
	requireOnce(t, "relationship", walk.relationships, relationships)
	// The first 5000 retractions in candidate order publish; the rest are the
	// overflow.
	requireOnce(t, "tombstone", walk.tombstones, tombstones[:5000])
}

func overflowEntities(count int) []candidate {
	var out []candidate
	var stats incidentPageStats
	for n := 0; n < count; n++ {
		for _, c := range incidentRowCandidates(incidentRow{
			id: fanOutIncidentID(n), title: "incident", activeRepos: parseIncidentRepos([]string{fanOutActiveRepo(0) + "|acme/r0"}),
			observedAt: fanOutStamp, cursorAt: fanOutStamp,
		}, &stats) {
			if c.entity != nil {
				out = append(out, c)
			}
		}
	}
	return out
}

func overflowRows(incidents []fanOutIncident) []candidate {
	var out []candidate
	var stats incidentPageStats
	for _, incident := range incidents {
		active := make([]string, 0, incident.active)
		for n := 0; n < incident.active; n++ {
			active = append(active, fmt.Sprintf("%s|acme/r%05d", fanOutActiveRepo(n), n))
		}
		ended := make([]string, 0, incident.ended)
		for n := 0; n < incident.ended; n++ {
			ended = append(ended, fanOutEndedRepo(n))
		}
		out = append(out, incidentRowCandidates(incidentRow{
			id: incident.id, title: "incident", activeRepos: parseIncidentRepos(active), endedRepos: ended,
			observedAt: fanOutStamp, cursorAt: fanOutStamp,
		}, &stats)...)
	}
	return out
}

func overflowEpisodes(count int) []candidate {
	out := make([]candidate, 0, count)
	for n := 0; n < count; n++ {
		out = append(out, episodeCandidate(storage.EpisodeProjectionRecord{
			EpisodeID: fmt.Sprintf("ep-%05d", n), RepoSlug: "example-org/widget-service", Goal: "fix flake", Outcome: "succeeded", Summary: "fixed it",
			StartedAt: fanOutStamp, EndedAt: fanOutStamp.Add(time.Minute), CreatedAt: fanOutStamp, UpdatedAt: fanOutStamp, RedactionState: "active",
		}))
	}
	return out
}

// Items one batch cannot carry are quarantined as a contract bound violation,
// never silently lost, for every kind the batch bounds; the first items in
// candidate order stay.
func TestOverflowQuarantineCoversEveryBoundedKind(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		all   []candidate
		kept  int
		count func([]candidate) int
	}{
		{kind: "entity", all: overflowEntities(contractsv1.ContextFabricProjectionBatchMaxEntities + 2), kept: contractsv1.ContextFabricProjectionBatchMaxEntities},
		{kind: "relationship", all: overflowRows(incidentsOf(2, 2501, 0)), kept: contractsv1.ContextFabricProjectionBatchMaxRelationships},
		{kind: "episode", all: overflowEpisodes(contractsv1.ContextFabricProjectionBatchMaxEpisodes + 2), kept: contractsv1.ContextFabricProjectionBatchMaxEpisodes},
		{kind: "tombstone", all: overflowRows(incidentsOf(2, 1, 2501)), kept: contractsv1.ContextFabricProjectionBatchMaxTombstones},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			counts := map[string]int{}
			items := partitionProjectableCandidates(tc.all, func(o quarantineObservation) { counts[o.Kind+"/"+o.Reason]++ })
			var want []candidate
			for _, c := range tc.all {
				if kind, _ := validateCandidateItem(c); kind == tc.kind {
					want = append(want, c)
				}
			}
			var got []candidate
			for _, c := range items {
				if kind, _ := validateCandidateItem(c); kind == tc.kind {
					got = append(got, c)
				}
			}
			if len(got) != tc.kept {
				t.Fatalf("kept %d %s items, want %d", len(got), tc.kind, tc.kept)
			}
			for i := range got {
				if itemIdentity(got[i]) != itemIdentity(want[i]) {
					t.Fatalf("kept %s item %d is not the %d-th in candidate order", tc.kind, i, i)
				}
			}
			if wantCounts := map[string]int{tc.kind + "/" + quarantineContractBoundViolation: len(want) - tc.kept}; fmt.Sprint(counts) != fmt.Sprint(wantCounts) {
				t.Fatalf("quarantined %v, want %v", counts, wantCounts)
			}
		})
	}
}

// The page cut counts every bounded kind: a second row that would pass any one
// bound starts the next page.
func TestPageCutCountsEveryBoundedKind(t *testing.T) {
	for _, tc := range []struct {
		kind string
		all  []candidate
	}{
		{kind: "entity", all: overflowEntities(contractsv1.ContextFabricProjectionBatchMaxEntities + 1)},
		{kind: "relationship", all: overflowRows(incidentsOf(2, 2501, 0))},
		{kind: "episode", all: overflowEpisodes(contractsv1.ContextFabricProjectionBatchMaxEpisodes + 1)},
		{kind: "tombstone", all: overflowRows(incidentsOf(2, 1, 2501))},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			sortCandidates(tc.all)
			kept, bounded := truncateToCompleteRows(tc.all, len(tc.all), pageBound{})
			if bounded {
				t.Fatal("an item bound cut reported a truncated-table bound")
			}
			if len(kept) == len(tc.all) || len(kept) == 0 {
				t.Fatalf("kept %d of %d candidates, want the page cut before the row past the %s bound", len(kept), len(tc.all), tc.kind)
			}
			if !withinBatchBoundsAll(kept) {
				t.Fatalf("the kept page passes a batch bound")
			}
			if withinBatchBoundsAll(tc.all[:len(kept)+rowLen(tc.all, len(kept))]) {
				t.Fatalf("the row after the cut fits the page: the cut is early")
			}
		})
	}
}

func rowLen(all []candidate, start int) int {
	end := start + 1
	for end < len(all) && all[end].position().Equal(all[start].position()) && all[end].sortKey == all[start].sortKey {
		end++
	}
	return end - start
}

// withinBatchBoundsAll counts apart from the code under test.
func withinBatchBoundsAll(all []candidate) bool {
	episodes := 0
	for _, c := range all {
		if c.episode != nil {
			episodes++
		}
	}
	entities, relationships, tombstones := candidateCounts(all)
	return entities <= 1000 && relationships <= 5000 && episodes <= 1000 && tombstones <= 5000
}

func itemIdentity(c candidate) string {
	return fmt.Sprintf("%p|%p|%p|%p", c.entity, c.relationship, c.episode, c.tombstone)
}
