package devhealthsource_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func chaos7126Ctx(id string) context.Context {
	sum := sha256.Sum256([]byte(id))
	return observability.WithRequestID(context.Background(), "req_"+hex.EncodeToString(sum[:16]))
}

// drainSource drains one real producer into the real graph.
func drainSource(t *testing.T, ctx context.Context, source contextfabric.ProjectionSource, adapter *falkorgraph.Adapter, orgID, name string) {
	t.Helper()
	cursor := ""
	replays := map[string]bool{}
	for page := 0; page < 200; page++ {
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: name, Cursor: cursor})
		if err != nil {
			t.Fatalf("%s page %d: %v", name, page, err)
		}
		if !available {
			return
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("apply %s page %d: %v", name, page, err)
		}
		requireCursorProgress(t, fmt.Sprintf("%s page %d", name, page), cursor, batch, replays)
		cursor = batch.NextCursor
	}
	t.Fatalf("%s did not drain", name)
}

func findIDs(response directread.FindResponse) []string {
	ids := make([]string, 0, len(response.Subjects))
	for _, s := range response.Subjects {
		ids = append(ids, s.CanonicalID)
	}
	sort.Strings(ids)
	return ids
}

// TestChaos7126FindModesOnRealProducers runs find_subjects owned_by and
// handle end to end on real stores: seeded ClickHouse, the REAL producers
// (devhealthsource main source and teams/projects source) into a real
// FalkorDB, the REAL census (devhealthsource.NewCensusFunc) and the REAL
// subject and edge gates.
//
//   - owned_by T1 equals oracle O3's path 2 (the team_repo_ownership
//     population) for the unrestricted caller: the same repository set as
//     read_relationships, through the find_subjects surface;
//   - owned_by T1 for a caller granted acme/r1 is repository R1 only;
//   - "PR 532" exists in R1 and R2: the unrestricted caller gets ambiguous
//     with both; the caller granted acme/r1 gets complete with R1's PR and
//     nothing that says a second one exists;
//   - "PR 999" exists nowhere: empty.
func TestChaos7126FindModesOnRealProducers(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	adapter := chaos7074FalkorAdapter(t, ctx)

	now := time.Now().UTC()
	orgID := "o3000000-0000-4000-8000-000000000126"
	o3Seed(t, ctx, direct, orgID, now)
	for _, pr := range []struct {
		repo   int
		number uint32
	}{{1, 532}, {2, 532}, {1, 777}} {
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`,
			o3UUID(orgID+"R"+fmt.Sprint(pr.repo)), orgID, pr.number, fmt.Sprintf("PR %d in r%d", pr.number, pr.repo), "open", now); err != nil {
			t.Fatalf("seed PR: %v", err)
		}
	}
	// A work item in R1 with ticket key CHAOS-77 (venue re-roll 2: the
	// census has no repository anchor for work items).
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"linear:CHAOS-77", o3UUID(orgID+"R1"), orgID, "CHAOS-77 in r1", "open", "", "", "linear", "", now); err != nil {
		t.Fatalf("seed work item: %v", err)
	}
	main, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	drainSource(t, ctx, main, adapter, orgID, devhealthsource.SourceName)
	teams, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatal(err)
	}
	drainSource(t, ctx, teams, adapter, orgID, devhealthsource.TeamsProjectsSourceName)

	gate := directread.NewSubjectGate(adapter, nil)
	lookup := directread.NewSubjectLookup(adapter, gate, nil).WithOwnershipAndHandles(adapter, devhealthsource.NewCensusFunc(query), adapter)
	unrestricted := storage.Principal{OrgID: orgID, Subject: "u", CredentialID: "c"}
	restricted := storage.Principal{OrgID: orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/r1"}}

	t.Run("owned_by equals the ownership population", func(t *testing.T) {
		want, _, _ := o3Population(t, ctx, direct, orgID, "T1")
		sort.Strings(want)
		response, err := lookup.Find(chaos7126Ctx("owned-u"), unrestricted, directread.FindRequest{OwnedBy: contextfabric.TeamCanonicalID("T1"), Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		if got := findIDs(response); strings.Join(got, ",") != strings.Join(want, ",") || response.Status != directread.FindComplete {
			t.Fatalf("owned_by T1 = %v (%s), ownership population = %v", got, response.Status, want)
		}
	})
	t.Run("owned_by for a restricted caller", func(t *testing.T) {
		response, err := lookup.Find(chaos7126Ctx("owned-r"), restricted, directread.FindRequest{OwnedBy: contextfabric.TeamCanonicalID("T1")})
		if err != nil {
			t.Fatal(err)
		}
		if got := findIDs(response); len(got) != 1 || got[0] != "repository:"+o3UUID(orgID+"R1") {
			t.Fatalf("restricted owned_by = %v", got)
		}
	})
	t.Run("handle", func(t *testing.T) {
		open, err := lookup.Find(chaos7126Ctx("h-u"), unrestricted, directread.FindRequest{Handle: "PR 532"})
		if err != nil || open.Status != directread.FindAmbiguous || len(open.Subjects) != 2 {
			t.Fatalf("unrestricted PR 532: %v %+v", err, open)
		}
		for _, s := range open.Subjects {
			if s.Kind != "pull_request" || s.Match != directread.MatchProviderKey || !strings.HasPrefix(s.Label, "PR 532") {
				t.Fatalf("subject = %+v", s)
			}
		}
		mine, err := lookup.Find(chaos7126Ctx("h-r"), restricted, directread.FindRequest{Handle: "PR 532"})
		if err != nil || mine.Status != directread.FindComplete || len(mine.Subjects) != 1 || mine.Population.TotalKnown != 1 {
			t.Fatalf("restricted PR 532: %v %+v", err, mine)
		}
		if !strings.Contains(mine.Subjects[0].Label, "r1") {
			t.Fatalf("restricted got the wrong PR: %+v", mine.Subjects[0])
		}
		none, err := lookup.Find(chaos7126Ctx("h-none"), unrestricted, directread.FindRequest{Handle: "PR 999"})
		if err != nil || none.Status != directread.FindEmpty {
			t.Fatalf("PR 999: %v %+v", err, none)
		}
		// Work items: the org-wide census finds CHAOS-77; a restricted
		// credential gets the typed scope_required refusal (the census cannot
		// anchor a work item on a repository), never an outage.
		item, err := lookup.Find(chaos7126Ctx("h-wi-u"), unrestricted, directread.FindRequest{Handle: "CHAOS-77"})
		if err != nil || item.Status != directread.FindComplete || len(item.Subjects) != 1 || item.Subjects[0].Kind != "work_item" {
			t.Fatalf("unrestricted CHAOS-77: %v %+v", err, item)
		}
		_, err = lookup.Find(chaos7126Ctx("h-wi-r"), restricted, directread.FindRequest{Handle: "CHAOS-77"})
		if errors.Is(err, directread.ErrFindUnavailable) || !errors.Is(err, directread.ErrFindScopeRequired) {
			t.Fatalf("restricted CHAOS-77: want scope_required, got %v", err)
		}
	})
}
