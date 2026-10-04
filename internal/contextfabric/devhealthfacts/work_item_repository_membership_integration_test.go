package devhealthfacts_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The rows below are the shapes the ops writers produce: a GitHub pull
// request is a work item ghpr:<owner>/<repo>#<n> of type pr on its
// repository; a GitHub issue is gh:<owner>/<repo>#<n> on its own repository;
// Linear and Jira issues carry the zero repository id; every link is a
// relates_to row whose source is the pull request (GitHub closing reference,
// Linear attachment, Jira development status).
const (
	repoWalkOrg      = "repository-work-item-walk-live"
	repoWalkZero     = "00000000-0000-0000-0000-000000000000"
	repoWalkAPI      = "40000000-0000-4000-8000-000000000001"
	repoWalkWeb      = "40000000-0000-4000-8000-000000000002"
	repoWalkQuiet    = "40000000-0000-4000-8000-000000000003"
	repoWalkNoPulls  = "40000000-0000-4000-8000-000000000004"
	repoWalkNotARepo = "40000000-0000-4000-8000-000000000009"
)

type repoWalkWorkItem struct {
	id, repo, provider, kind, status string
	completed                        *time.Time
}

type repoWalkLink struct{ source, target, kind, raw string }

var repoWalkSeed sync.Once

// seedRepositoryWalk writes the fixture once per test binary; the tests only
// read it.
func seedRepositoryWalk(t *testing.T) {
	t.Helper()
	repoWalkSeed.Do(func() { seedRepositoryWalkRows(t) })
}

func seedRepositoryWalkRows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	_, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	recent := at.Add(-24 * time.Hour)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	for _, repo := range []struct{ id, slug string }{{repoWalkAPI, "acme/api"}, {repoWalkWeb, "acme/web"}, {repoWalkQuiet, "acme/quiet"}, {repoWalkNoPulls, "acme/nopulls"}} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo.id, repoWalkOrg, repo.slug, "github", at)
	}
	items := []repoWalkWorkItem{
		{"ghpr:acme/api#10", repoWalkAPI, "github", "pr", "done", nil},
		{"ghpr:acme/api#11", repoWalkAPI, "github", "pr", "todo", nil},
		{"ghpr:acme/web#20", repoWalkWeb, "github", "pr", "todo", nil},
		{"ghpr:acme/quiet#30", repoWalkQuiet, "github", "pr", "todo", nil},
		// linked to a pull request of acme/api
		{"gh:acme/api#5", repoWalkAPI, "github", "issue", "todo", nil},
		{"linear:ENG-12", repoWalkZero, "linear", "issue", "todo", nil},
		// linked to two pull requests of acme/api
		{"jira:OPS-3", repoWalkZero, "jira", "bug", "done", &recent},
		// linked into two repositories
		{"linear:ENG-40", repoWalkZero, "linear", "story", "in_progress", nil},
		// an issue of acme/api by its own repository column and no link
		{"gh:acme/api#6", repoWalkAPI, "github", "issue", "todo", nil},
		{"gh:acme/web#21", repoWalkWeb, "github", "issue", "todo", nil},
		// related to an issue of acme/api, not to a pull request
		{"linear:ENG-77", repoWalkZero, "linear", "issue", "todo", nil},
		// linked from a pull request of acme/api with the pull request as
		// the row's target
		{"linear:ENG-55", repoWalkZero, "linear", "issue", "todo", nil},
	}
	for _, item := range items {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, type, status, url, created_at, updated_at, completed_at, parent_id, project_id, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.id, item.repo, repoWalkOrg, item.provider, item.id, item.kind, item.status, "", recent, at, item.completed, "", "", at)
	}
	links := []repoWalkLink{
		{"ghpr:acme/api#10", "gh:acme/api#5", "relates_to", "github_closing_reference"},
		{"ghpr:acme/api#10", "linear:ENG-12", "relates_to", "linear_attachment"},
		{"ghpr:acme/api#10", "jira:OPS-3", "relates_to", "jira_dev_status"},
		{"ghpr:acme/api#11", "jira:OPS-3", "relates_to", "jira_dev_status"},
		{"ghpr:acme/api#11", "linear:ENG-40", "relates_to", "linear_attachment"},
		{"ghpr:acme/web#20", "linear:ENG-40", "relates_to", "linear_attachment"},
		{"ghpr:acme/web#20", "gh:acme/web#21", "relates_to", "github_closing_reference"},
		// not a link of acme/api: a blocking relation, an unresolved external
		// key, an issue related to an issue, and a pull request related to a
		// pull request of another repository
		{"ghpr:acme/api#10", "gh:acme/api#6", "blocks", "github_text_reference"},
		{"ghpr:acme/api#11", "ENG-99", "external_issue_key", "external_issue_key"},
		{"gh:acme/api#6", "linear:ENG-77", "relates_to", "github_text_reference"},
		{"ghpr:acme/api#11", "ghpr:acme/web#20", "relates_to", "github_text_reference"},
		// The graph's RELATES_TO is read in either direction, as the project
		// walk reads it: a row whose target is the pull request still links.
		{"linear:ENG-55", "ghpr:acme/api#11", "relates_to", "linear_attachment"},
	}
	for _, link := range links {
		exec(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?)`,
			link.source, link.target, link.kind, link.raw, at, repoWalkOrg)
	}
	// The provider-recorded link the library authorization reads for a
	// repository-less item.
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoWalkAPI, "linear:ENG-12", uint32(10), float32(1), "native", "linear_attachment", at, repoWalkOrg)
}

func repoWalkMemberID(t *testing.T, repo, workItemID string) string {
	t.Helper()
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{repo, workItemID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive %s: %v omitted=%t", workItemID, err, omitted)
	}
	return id
}

func readRepositoryWalk(t *testing.T, principal storage.Principal, repo string, mutate func(*contextfabric.WorkItemMembershipRequest)) contextfabric.WorkItemMembershipResult {
	t.Helper()
	query, _ := sharedClickHouseFixture(t)
	gate, err := contextfabric.NewWorkItemMembershipGate(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := devhealthfacts.NewWorkItemMembershipReader(query, devhealthfacts.WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.WorkItemMembershipRequest{Anchor: contextfabric.WorkItemMembershipAnchor{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repo}}}
	if mutate != nil {
		mutate(&request)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lease, result, err := reader.BeginWorkItemMembership(ctx, principal, request)
	if lease != nil {
		lease.Release()
	}
	if err != nil {
		t.Fatalf("BeginWorkItemMembership: %v", err)
	}
	return result
}

func repoWalkMembers(result contextfabric.WorkItemMembershipResult) []string {
	ids := make([]string, 0, len(result.Members))
	for _, member := range result.Members {
		ids = append(ids, member.WorkItemID)
	}
	sort.Strings(ids)
	return ids
}

func TestLiveARepositoryServesTheIssuesLinkedToItsPullRequests(t *testing.T) {
	seedRepositoryWalk(t)
	everyone := storage.Principal{OrgID: repoWalkOrg, RepositoryScopes: []string{"*"}}
	for _, tc := range []struct {
		name         string
		repo         string
		want         []string
		pullRequests int
		linked       int
	}{
		{"tracker, GitHub and Jira issues; two pull requests to one issue; an own-repository issue with no link is out", repoWalkAPI, []string{"gh:acme/api#5", "jira:OPS-3", "linear:ENG-12", "linear:ENG-40", "linear:ENG-55"}, 2, 5},
		{"one issue linked into two repositories is a member of each", repoWalkWeb, []string{"gh:acme/web#21", "linear:ENG-40"}, 1, 2},
		{"pull requests and no linked issue", repoWalkQuiet, []string{}, 1, 0},
		{"no pull request", repoWalkNoPulls, []string{}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := readRepositoryWalk(t, everyone, tc.repo, nil)
			if result.Census.State != contextfabric.WorkItemMembershipCensusExact || !result.Census.PopulationMeasured {
				t.Fatalf("census = %+v, want an exact measured read", result.Census)
			}
			got := repoWalkMembers(result)
			if len(got) != len(tc.want) {
				t.Fatalf("members = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("members = %v, want %v", got, tc.want)
				}
			}
			for _, member := range result.Members {
				if member.CanonicalID != repoWalkMemberID(t, member.RepoID, member.WorkItemID) {
					t.Fatalf("member %s carries canonical id %s", member.WorkItemID, member.CanonicalID)
				}
			}
			if result.Census.AuthorizedPopulation != len(tc.want) || result.Census.DeniedPopulation != 0 {
				t.Fatalf("census authorized=%d denied=%d, want %d/0", result.Census.AuthorizedPopulation, result.Census.DeniedPopulation, len(tc.want))
			}
			if result.Census.RepositoryPullRequests != tc.pullRequests || result.Census.RepositoryLinkedIssues != tc.linked {
				t.Fatalf("repository counts pull_requests=%d linked=%d, want %d/%d", result.Census.RepositoryPullRequests, result.Census.RepositoryLinkedIssues, tc.pullRequests, tc.linked)
			}
		})
	}
}

func TestLiveARepositoryAppliesTheStatusAndPeriodFilters(t *testing.T) {
	seedRepositoryWalk(t)
	everyone := storage.Principal{OrgID: repoWalkOrg, RepositoryScopes: []string{"*"}}
	done := readRepositoryWalk(t, everyone, repoWalkAPI, func(r *contextfabric.WorkItemMembershipRequest) { r.Status = "done" })
	if got := repoWalkMembers(done); len(got) != 1 || got[0] != "jira:OPS-3" {
		t.Fatalf("done members = %v, want the one done linked issue (the done pull request is not a member)", got)
	}
	now := time.Now().UTC()
	closed := readRepositoryWalk(t, everyone, repoWalkAPI, func(r *contextfabric.WorkItemMembershipRequest) {
		r.TimeColumn, r.TimeStart, r.TimeEnd = "completed_at", now.Add(-30*24*time.Hour), now
	})
	if got := repoWalkMembers(closed); len(got) != 1 || got[0] != "jira:OPS-3" {
		t.Fatalf("completed in the last 30 days = %v, want the one completed linked issue", got)
	}
	if closed.Census.RepositoryLinkedIssues != 5 {
		t.Fatalf("a filter changed the repository's linked issue count: %d", closed.Census.RepositoryLinkedIssues)
	}
}

func TestLiveARestrictedCallerSeesOnlyTheLinkedIssuesItMayRead(t *testing.T) {
	seedRepositoryWalk(t)
	restricted := storage.Principal{OrgID: repoWalkOrg, RepositoryScopes: []string{"acme/api"}}
	result := readRepositoryWalk(t, restricted, repoWalkAPI, nil)
	// gh:acme/api#5 by its own granted repository; linear:ENG-12 by its
	// provider-recorded link to a granted repository. jira:OPS-3,
	// linear:ENG-40 and linear:ENG-55 have no such grant and are neither
	// served nor named.
	if got := repoWalkMembers(result); len(got) != 2 || got[0] != "gh:acme/api#5" || got[1] != "linear:ENG-12" {
		t.Fatalf("restricted members = %v, want gh:acme/api#5 and linear:ENG-12", got)
	}
	if result.Census.AuthorizedPopulation != 2 || result.Census.DeniedPopulation != 3 {
		t.Fatalf("restricted census authorized=%d denied=%d, want 2/3", result.Census.AuthorizedPopulation, result.Census.DeniedPopulation)
	}
	other := readRepositoryWalk(t, storage.Principal{OrgID: repoWalkOrg, RepositoryScopes: []string{"acme/elsewhere"}}, repoWalkAPI, nil)
	if len(other.Members) != 0 || other.Census.AuthorizedPopulation != 0 {
		t.Fatalf("a caller granted another repository read %v", repoWalkMembers(other))
	}
}

func TestLiveAnUnknownRepositoryIsNotAnEmptyOne(t *testing.T) {
	seedRepositoryWalk(t)
	result := readRepositoryWalk(t, storage.Principal{OrgID: repoWalkOrg, RepositoryScopes: []string{"*"}}, repoWalkNotARepo, nil)
	if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.PopulationMeasured {
		t.Fatalf("an unknown repository measured %+v, want unmeasured", result.Census)
	}
}
