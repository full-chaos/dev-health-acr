package devhealthsource_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestLiveWorkItemProjectMembershipComesFromTheIssuesOwnProjectLink is
// CHAOS-12226 against a real ClickHouse with the ops presence view
// (devhealthschema.ProjectMembershipPresenceViewDDL, verbatim):
//
//   - the column arm by project id, and by project key, resolves to the one
//     project row and is stated as its canonical project.v2 identity;
//   - a key two projects share names no project (stated empty);
//   - a work item with no project link is stated empty;
//   - a work item with membership history follows its latest transition;
//   - a REMOVE transition ingested later, with no work-item sync, is picked up
//     by the refresh table and re-states the work item empty.
func TestLiveWorkItemProjectMembershipComesFromTheIssuesOwnProjectLink(t *testing.T) {
	ctx := context.Background()
	t1 := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	const org = "org-12226"
	project := func(id, key string) {
		mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, 'linear', 'started', '', 1, ?)`,
			id, org, "Project "+id, key, t1)
	}
	project("P1", "ENG")
	project("P2", "")
	project("P3", "DUP")
	project("P4", "DUP")
	workItem := func(id, projectID string) {
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, created_at, last_synced) VALUES (?, ?, ?, ?, 'open', '', '', 'linear', ?, ?, ?, ?)`,
			id, zeroRepositoryUUID, org, "Item "+id, projectID, t1, t1, t1)
	}
	workItem("WI-COL", "P1")
	workItem("WI-KEY", "ENG")
	workItem("WI-AMB", "DUP")
	workItem("WI-NONE", "")
	workItem("WI-TRANS", "")
	transition := func(subject, from, to, event string, at time.Time) {
		mustExec(t, ctx, direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?, ?)`,
			org, zeroRepositoryUUID, subject, from, to, at, at, event, at)
	}
	transition("WI-TRANS", "", "P2", "ev-12226-1", t1)

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source.WithWorkItemProjectMembership(true)
	drain := func(cursor string) (map[string][]string, string) {
		t.Helper()
		stated := map[string][]string{}
		for page := 0; page < 40; page++ {
			batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
			if err != nil {
				t.Fatalf("page %d: %v", page, err)
			}
			if !available {
				return stated, cursor
			}
			if err := batch.Validate(); err != nil {
				t.Fatalf("page %d: batch failed contract validation: %v", page, err)
			}
			for _, entity := range batch.Entities {
				if entity.Subject.Kind != contractsv1.ContextFabricSubjectWorkItem {
					continue
				}
				if entity.ProjectIdentities == nil {
					t.Fatalf("page %d: %s states no membership with membership on", page, entity.Subject.CanonicalID)
				}
				stated[entity.Subject.CanonicalID] = append([]string{}, *entity.ProjectIdentities...)
			}
			if batch.NextCursor == cursor {
				return stated, cursor
			}
			cursor = batch.NextCursor
		}
		t.Fatal("the source did not drain in 40 pages")
		return nil, ""
	}
	id := func(workItemID string) string {
		return fmt.Sprintf("work_item.v2:%s:%s", zeroRepositoryUUID, workItemID)
	}

	stated, cursor := drain("")
	want := map[string][]string{
		id("WI-COL"):   {"project.v2:linear:P1"},
		id("WI-KEY"):   {"project.v2:linear:P1"},
		id("WI-AMB"):   {},
		id("WI-NONE"):  {},
		id("WI-TRANS"): {"project.v2:linear:P2"},
	}
	for workItemID, wantSet := range want {
		got, found := stated[workItemID]
		if !found {
			t.Fatalf("first drain: %s not projected; stated=%v", workItemID, stated)
		}
		if !reflect.DeepEqual(got, wantSet) {
			t.Fatalf("first drain: %s states %v, want %v", workItemID, got, wantSet)
		}
	}

	// The work item leaves its last project. Only the transition is written:
	// the work item itself does not sync again.
	transition("WI-TRANS", "P2", "", "ev-12226-2", t2)
	stated, _ = drain(cursor)
	got, found := stated[id("WI-TRANS")]
	if !found {
		t.Fatalf("after the REMOVE: WI-TRANS was not re-emitted (the refresh table missed a membership change with no work-item sync); stated=%v", stated)
	}
	if len(got) != 0 {
		t.Fatalf("after the REMOVE: WI-TRANS states %v, want [] (in no project)", got)
	}
}
