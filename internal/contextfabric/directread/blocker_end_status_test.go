package directread

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const blockerAsOf = "2026-09-27T00:00:00Z"

type blockerFixture struct {
	graph *fakeEdgeGraph
	root  contextfabric.SubjectRef
}

func newBlockerFixture() *blockerFixture {
	return &blockerFixture{
		graph: &fakeEdgeGraph{fakeGraph: graphOfOrgA()},
		root:  subject(contractsv1.ContextFabricSubjectWorkItem, "work_item:root"),
	}
}

// addBlocks adds a BLOCKS edge from a new blocker to the root. A nil status
// stores no status on the blocker.
func (f *blockerFixture) addBlocks(id string, fromStatus *string) {
	from := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item:"+id)
	f.graph.nodes[graphrank.SubjectKey(from)] = repos("acme/a")
	f.graph.nodes[graphrank.SubjectKey(f.root)] = repos("acme/a")
	edge := edgeBetween("rel-"+id, "BLOCKS", from, f.root, nil)
	edge.From.Attributes = map[string]interface{}{"observed_at": blockerAsOf}
	if fromStatus != nil {
		edge.From.Attributes["property_status"] = *fromStatus
	}
	edge.To.Attributes = map[string]interface{}{"property_status": "in_progress", "observed_at": blockerAsOf}
	f.graph.edges = append(f.graph.edges, edge)
}

func (f *blockerFixture) request(currentOnly bool, limit int) RelationshipsRequest {
	return RelationshipsRequest{
		Subject: RelationshipsSubject{Kind: string(f.root.Kind), CanonicalID: f.root.CanonicalID},
		Types:   []string{"BLOCKS"}, Limit: limit, CurrentOnly: currentOnly,
	}
}

func str(v string) *string { return &v }

func TestBlocksEdgeServesEndStatusAndActive(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     *string
		wantStatus string
		wantActive *bool
	}{
		{"done", str("done"), "done", boolPtr(false)},
		{"canceled", str("canceled"), "canceled", boolPtr(false)},
		{"in progress", str("in_progress"), "in_progress", boolPtr(true)},
		{"blocked counts as not done", str("blocked"), "blocked", boolPtr(true)},
		{"backlog", str("backlog"), "backlog", boolPtr(true)},
		{"no stored status", nil, "unknown", nil},
		{"stored unknown", str("unknown"), "unknown", nil},
		{"outside the vocabulary", str("cancelled"), "unknown", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBlockerFixture()
			f.addBlocks("a", tc.status)
			reader, _ := newRelReader(f.graph, nil)
			response, err := reader.Read(relCtx("end-status-"+tc.name), unrestricted, f.request(false, 10))
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Edges) != 1 {
				t.Fatalf("edges = %d, want 1", len(response.Edges))
			}
			edge := response.Edges[0]
			if edge.From.Status != tc.wantStatus || edge.To.Status != "in_progress" {
				t.Fatalf("from.status=%q to.status=%q, want %q / in_progress", edge.From.Status, edge.To.Status, tc.wantStatus)
			}
			if edge.From.StatusAsOf != blockerAsOf {
				t.Fatalf("from.status_as_of = %q", edge.From.StatusAsOf)
			}
			switch {
			case tc.wantActive == nil && edge.Active != nil:
				t.Fatalf("active = %v, want absent for an unknown status", *edge.Active)
			case tc.wantActive != nil && (edge.Active == nil || *edge.Active != *tc.wantActive):
				t.Fatalf("active = %v, want %v", edge.Active, *tc.wantActive)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }

func TestBlocksEdgeBothEndsDoneIsServedWithStatus(t *testing.T) {
	f := newBlockerFixture()
	f.addBlocks("a", str("done"))
	f.graph.edges[0].To.Attributes["property_status"] = "canceled"
	reader, _ := newRelReader(f.graph, nil)
	response, err := reader.Read(relCtx("both-done"), unrestricted, f.request(false, 10))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response.Edges)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"status":"done"`, `"status":"canceled"`, `"active":false`, `"status_as_of":"` + blockerAsOf + `"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("served edge lacks %s: %s", want, raw)
		}
	}
	if response.Page.DroppedInactive != 0 {
		t.Fatalf("dropped_inactive = %d without current_only", response.Page.DroppedInactive)
	}
}

func TestNonBlocksEdgeCarriesNoEndStatus(t *testing.T) {
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
	edge := edgeBetween("rel-1", "OWNED_BY_TEAM", repoA, teamT, nil)
	edge.From.Attributes = map[string]interface{}{"property_status": "done", "observed_at": blockerAsOf}
	graph.edges = []EdgeCandidate{edge}
	reader, _ := newRelReader(graph, nil)
	response, err := reader.Read(relCtx("non-blocks"), unrestricted, RelationshipsRequest{
		Subject: RelationshipsSubject{Kind: string(repoA.Kind), CanonicalID: repoA.CanonicalID}, CurrentOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(response.Edges)
	if len(response.Edges) != 1 || strings.Contains(string(raw), `"status"`) || strings.Contains(string(raw), `"active"`) {
		t.Fatalf("non-BLOCKS edge changed: %s", raw)
	}
}

func TestCurrentOnlyDropsInactiveBlockersAndKeepsUnknown(t *testing.T) {
	f := newBlockerFixture()
	f.addBlocks("done", str("done"))
	f.addBlocks("canceled", str("canceled"))
	f.addBlocks("open", str("in_progress"))
	f.addBlocks("blocked", str("blocked"))
	f.addBlocks("unknown", nil)

	reader, recorder := newRelReader(f.graph, nil)
	all, err := reader.Read(relCtx("current-only-off"), unrestricted, f.request(false, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Edges) != 5 || all.Page.DroppedInactive != 0 || all.Effective.CurrentOnly {
		t.Fatalf("default read = %d edges dropped=%d current_only=%v, want 5 / 0 / false", len(all.Edges), all.Page.DroppedInactive, all.Effective.CurrentOnly)
	}

	current, err := reader.Read(relCtx("current-only-on"), unrestricted, f.request(true, 10))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range current.Edges {
		ids = append(ids, e.RelationshipID)
	}
	slices.Sort(ids)
	if want := []string{"rel-blocked", "rel-open", "rel-unknown"}; !slices.Equal(ids, want) {
		t.Fatalf("current_only edges = %v, want %v", ids, want)
	}
	if current.Page.DroppedInactive != 2 || current.Page.Examined != 5 || current.Page.Returned != 3 || !current.Effective.CurrentOnly {
		t.Fatalf("page = %+v current_only=%v", current.Page, current.Effective.CurrentOnly)
	}
	if got := recorder.reads[len(recorder.reads)-1].EdgesDroppedInactive; got != 2 {
		t.Fatalf("recorded EdgesDroppedInactive = %d, want 2", got)
	}
}

func TestCurrentOnlyKeepsEdgeWhoseBlockedEndIsDone(t *testing.T) {
	f := newBlockerFixture()
	f.addBlocks("open", str("in_progress"))
	f.graph.edges[0].To.Attributes["property_status"] = "done"
	reader, _ := newRelReader(f.graph, nil)
	response, err := reader.Read(relCtx("blocked-end-done"), unrestricted, f.request(true, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Edges) != 1 || response.Page.DroppedInactive != 0 {
		t.Fatalf("edges=%d dropped=%d: only the blocking end decides", len(response.Edges), response.Page.DroppedInactive)
	}
}

func TestCurrentOnlyPagesJoinWithoutGapOrDuplicate(t *testing.T) {
	f := newBlockerFixture()
	active := map[string]bool{}
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("n%02d", i)
		status := "in_progress"
		if i%3 != 0 {
			status = "done"
		} else {
			active["rel-"+id] = true
		}
		f.addBlocks(id, str(status))
	}
	reader, _ := newRelReader(f.graph, nil)
	pages, ids := readAll(t, reader, unrestricted, f.request(true, 2))
	got := map[string]int{}
	dropped := 0
	for _, id := range ids {
		got[id]++
	}
	for _, page := range pages {
		dropped += page.Page.DroppedInactive
	}
	if len(got) != len(active) {
		t.Fatalf("served %v, want %d active edges", got, len(active))
	}
	for id, n := range got {
		if n != 1 || !active[id] {
			t.Fatalf("edge %s served %d times, active=%v", id, n, active[id])
		}
	}
	if dropped != 6 {
		t.Fatalf("dropped across pages = %d, want 6", dropped)
	}
}

func TestCurrentOnlyBindsTheCursor(t *testing.T) {
	f := newBlockerFixture()
	for i := 0; i < 4; i++ {
		f.addBlocks(fmt.Sprintf("c%d", i), str("in_progress"))
	}
	reader, _ := newRelReader(f.graph, nil)
	first, err := reader.Read(relCtx("cursor-1"), unrestricted, f.request(false, 2))
	if err != nil || first.Page.NextCursor == "" {
		t.Fatalf("first page: %v cursor=%q", err, first.Page.NextCursor)
	}
	request := f.request(true, 2)
	request.Cursor = first.Page.NextCursor
	if _, err := reader.Read(relCtx("cursor-2"), unrestricted, request); err == nil {
		t.Fatal("a cursor issued without current_only continued a current_only walk")
	}
}

func TestCurrentOnlyDoesNotCountWithheldEdges(t *testing.T) {
	f := newBlockerFixture()
	f.addBlocks("hidden", str("done"))
	f.graph.edges[0].Attributes["authorization_repositories"] = []string{"acme/b"}
	f.addBlocks("seen", str("done"))
	reader, _ := newRelReader(f.graph, nil)
	response, err := reader.Read(relCtx("withheld-not-dropped"), restrictedA, f.request(true, 10))
	if err != nil {
		t.Fatal(err)
	}
	if response.Withheld.EdgesNotVisible != 1 || response.Page.DroppedInactive != 1 || len(response.Edges) != 0 {
		t.Fatalf("withheld=%+v dropped=%d edges=%d: a withheld edge must not leak into the dropped count", response.Withheld, response.Page.DroppedInactive, len(response.Edges))
	}
}

func TestDroppedInactiveIsOnTheReadLogLine(t *testing.T) {
	args := RelationshipsReadLogArgs(unrestricted, RelationshipsReadRecord{EdgesDroppedInactive: 3})
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == "edges_dropped_inactive" {
			if args[i+1] != 3 {
				t.Fatalf("edges_dropped_inactive = %v, want 3", args[i+1])
			}
			return
		}
	}
	t.Fatalf("read log line lacks edges_dropped_inactive: %v", args)
}
