package devhealthsource_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// itemWithEpic writes one version of a work item that names an epic as well as
// (or instead of) a parent.
func (w *edgeWorld) itemWithEpic(repo, id, provider, parent, epic string, synced time.Time) {
	w.exec("work item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, parent_id, epic_id, provider, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, repo, w.orgID, "item "+id, "issue", "open", "", parent, epic, provider, w.now.Add(-30*24*time.Hour), synced, synced)
}

// A work item whose parent_id is empty names its parent by epic_id: the GitLab
// epic (a group-qualified id) and the Jira Epic Link. A row that names both has
// one parent, the parent_id. An epic id that does not name an epic row, or only
// its bare number, is no parent, and a cleared or moved epic leaves one open
// edge.
func partOfEpicParent(t *testing.T, adapter *falkorgraph.Adapter) {
	const org = "97950000-0000-4000-8000-000000000006"
	w := newEdgeWorld(t, adapter, org)
	const repoA, repoB = "97950000-0000-4000-8000-0000000000d1", "97950000-0000-4000-8000-0000000000d2"
	const noRepo = "00000000-0000-0000-0000-000000000000"
	w.repo(repoA, "acme/gitlab-project")
	w.repo(repoB, "acme/jira-project")
	first := w.now.Add(-5 * time.Hour)
	const epicOne, epicTwo = "gitlab:acme/platform:epic:3", "gitlab:acme/platform:epic:4"

	// GitLab: epics are group rows with no repository; an issue names its epic.
	w.itemWithEpic(noRepo, epicOne, "gitlab", "", "", first)
	w.itemWithEpic(noRepo, epicTwo, "gitlab", "", "", first)
	w.itemWithEpic(repoA, "gitlab:acme/platform/svc#7", "gitlab", "", epicOne, first)
	w.itemWithEpic(repoA, "gitlab:acme/platform/svc#8", "gitlab", "", "gitlab:acme/platform:epic:99", first)
	w.itemWithEpic(repoA, "gitlab:acme/platform/svc#9", "gitlab", "", "gitlab:41:epic:3", first)
	// A GitLab epic nested in an epic names its parent epic in epic_id as well.
	w.itemWithEpic(noRepo, "gitlab:acme/platform:epic:5", "gitlab", "", epicOne, first)
	// Jira: a parent in parent_id, an Epic Link in epic_id, and both on one row.
	w.itemWithEpic(repoB, "jira:EP-1", "jira", "", "", first)
	w.itemWithEpic(repoB, "jira:PAR-1", "jira", "", "", first)
	w.itemWithEpic(repoB, "jira:SUB-1", "jira", "jira:PAR-1", "", first)
	w.itemWithEpic(repoB, "jira:LNK-1", "jira", "", "jira:EP-1", first)
	w.itemWithEpic(repoB, "jira:BOTH-1", "jira", "jira:PAR-1", "jira:EP-1", first)
	w.drain()

	edges := func(repo, id string) []string {
		return w.edgesOut(w.workItemID(repo, id), contractsv1.ContextFabricRelationshipPartOf)
	}
	edgeTo := func(childRepo, child, parentRepo, parent string) []string {
		return []string{w.workItemID(childRepo, child) + " -> " + w.workItemID(parentRepo, parent)}
	}
	for _, c := range []struct {
		name             string
		childRepo, child string
		want             []string
	}{
		{"a GitLab issue is part of its epic", repoA, "gitlab:acme/platform/svc#7", edgeTo(repoA, "gitlab:acme/platform/svc#7", noRepo, epicOne)},
		{"a GitLab epic is part of its parent epic", noRepo, "gitlab:acme/platform:epic:5", edgeTo(noRepo, "gitlab:acme/platform:epic:5", noRepo, epicOne)},
		{"an epic id that names no epic row is no parent", repoA, "gitlab:acme/platform/svc#8", nil},
		{"an epic id with another group's number is no parent", repoA, "gitlab:acme/platform/svc#9", nil},
		{"a Jira parent is the parent", repoB, "jira:SUB-1", edgeTo(repoB, "jira:SUB-1", repoB, "jira:PAR-1")},
		{"a Jira Epic Link is the parent where parent_id is empty", repoB, "jira:LNK-1", edgeTo(repoB, "jira:LNK-1", repoB, "jira:EP-1")},
		{"a row that names a parent and an epic has the parent only", repoB, "jira:BOTH-1", edgeTo(repoB, "jira:BOTH-1", repoB, "jira:PAR-1")},
	} {
		if got := edges(c.childRepo, c.child); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: PART_OF edges %v, want %v", c.name, got, c.want)
		}
	}

	issue := "gitlab:acme/platform/svc#7"
	t.Run("the epic changes", func(t *testing.T) {
		w.itemWithEpic(repoA, issue, "gitlab", "", epicTwo, time.Now().UTC().Add(time.Second))
		w.drain()
		if got := edges(repoA, issue); fmt.Sprint(got) != fmt.Sprint(edgeTo(repoA, issue, noRepo, epicTwo)) {
			t.Fatalf("after the epic moved the graph holds %v, want only the edge to the new epic", got)
		}
	})
	t.Run("the epic is cleared", func(t *testing.T) {
		w.itemWithEpic(repoA, issue, "gitlab", "", "", time.Now().UTC().Add(2*time.Second))
		w.drain()
		if got := edges(repoA, issue); len(got) != 0 {
			t.Fatalf("the epic was cleared and the graph holds %v, want no PART_OF edge", got)
		}
	})
}
