package devhealthsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type recordingCensusClient struct {
	*censusFakeClient
	bindings [][]contextpacket.ClickHouseBinding
}

func (c *recordingCensusClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.bindings = append(c.bindings, bindings)
	return c.censusFakeClient.Query(ctx, statement, bindings)
}

func runFilteredCensus(t *testing.T, kind graphrank.CensusKind, handle string, slugs []string) (*recordingCensusClient, graphrank.CensusOutcome) {
	t.Helper()
	client := &recordingCensusClient{censusFakeClient: &censusFakeClient{aggregateCount: 2, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"o:r1:747", "o:r2:747"}}}
	ctx := graphrank.WithCensusRepositoryFilter(context.Background(), slugs)
	outcome, err := devhealthsource.NewCensusFunc(client)(ctx, "org_1", kind, handle, true, "", "", false)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	return client, outcome
}

func TestCensusRepositoryFilterIsAppliedInEveryStatement(t *testing.T) {
	t.Parallel()
	client, outcome := runFilteredCensus(t, contextfabric.SubjectPullRequest, "747", []string{"ACME/Repo-25", " acme/other "})
	if !outcome.RepositoryFilterApplied {
		t.Fatalf("RepositoryFilterApplied = false, want true")
	}
	if len(client.calls) != 2 {
		t.Fatalf("statements = %d, want aggregate and satisfier set", len(client.calls))
	}
	for i, statement := range client.calls {
		if !strings.Contains(statement, "toString(p.repo_id) IN (SELECT toString(id) FROM repos FINAL WHERE org_id = {census_org_id:String}") || !strings.Contains(statement, "lower(trimBoth(repo)) = {census_repo_0:String}") {
			t.Fatalf("statement %d = %q, want the repository filter inside it", i, statement)
		}
		values := map[string]any{}
		for _, b := range client.bindings[i] {
			values[b.Name] = b.Value
		}
		if values["census_repo_0"] != "acme/repo-25" || values["census_repo_1"] != "acme/other" {
			t.Fatalf("statement %d bindings = %v, want the normalized slugs", i, values)
		}
	}
}

func TestCensusRepositoryFilterOwnerWildcard(t *testing.T) {
	t.Parallel()
	client, outcome := runFilteredCensus(t, contextfabric.SubjectPullRequest, "747", []string{"ACME/*"})
	if !outcome.RepositoryFilterApplied || !strings.Contains(client.calls[0], "startsWith(lower(trimBoth(repo)), {census_repo_0:String})") {
		t.Fatalf("applied = %v statement = %q, want an owner prefix clause", outcome.RepositoryFilterApplied, client.calls[0])
	}
	for _, b := range client.bindings[0] {
		if b.Name == "census_repo_0" && b.Value != "acme/" {
			t.Fatalf("owner binding = %v, want acme/", b.Value)
		}
	}
}

func TestCensusRepositoryFilterFallsBackToUnfilteredWhenItCannotBeExact(t *testing.T) {
	t.Parallel()
	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = "acme/repo"
	}
	atCap := make([]string, 200)
	for i := range atCap {
		atCap[i] = "acme/repo"
	}
	_, capOutcome := runFilteredCensus(t, contextfabric.SubjectPullRequest, "747", atCap)
	if !capOutcome.RepositoryFilterApplied {
		t.Fatalf("200 slugs (the request maximum) must still be filtered in the query")
	}
	cases := map[string]struct {
		kind  graphrank.CensusKind
		slugs []string
	}{
		"wildcard":           {contextfabric.SubjectPullRequest, []string{"*"}},
		"malformed slug":     {contextfabric.SubjectPullRequest, []string{"acme/ok", "not a slug"}},
		"empty owner prefix": {contextfabric.SubjectPullRequest, []string{"/*"}},
		"too many":           {contextfabric.SubjectPullRequest, tooMany},
		"work item wildcard": {contextfabric.SubjectWorkItem, []string{"*"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			handle := "747"
			if tc.kind == contextfabric.SubjectWorkItem {
				handle = "CHAOS-77"
			}
			client, outcome := runFilteredCensus(t, tc.kind, handle, tc.slugs)
			if outcome.RepositoryFilterApplied {
				t.Fatalf("RepositoryFilterApplied = true, want false")
			}
			for _, statement := range client.calls {
				if strings.Contains(statement, "FROM repos") {
					t.Fatalf("statement = %q, want no repository filter", statement)
				}
			}
		})
	}
}

func TestCensusWithoutAFilterIsUnchanged(t *testing.T) {
	t.Parallel()
	client, outcome := runFilteredCensus(t, contextfabric.SubjectPullRequest, "747", nil)
	if outcome.RepositoryFilterApplied {
		t.Fatalf("RepositoryFilterApplied = true without a filter")
	}
	want := "SELECT count(), now64(), min(concat(p.org_id, ':', toString(p.repo_id), ':', toString(p.number))) FROM git_pull_requests AS p FINAL WHERE p.org_id = {census_org_id:String} AND p.number = {census_handle_pr_number:UInt32} SETTINGS empty_result_for_aggregation_by_empty_set = 0"
	if client.calls[0] != want {
		t.Fatalf("aggregate statement = %q\nwant %q", client.calls[0], want)
	}
	wantSet := "SELECT concat(p.org_id, ':', toString(p.repo_id), ':', toString(p.number)) FROM git_pull_requests AS p FINAL WHERE p.org_id = {census_org_id:String} AND p.number = {census_handle_pr_number:UInt32} LIMIT 1000"
	if client.calls[1] != wantSet {
		t.Fatalf("satisfier statement = %q\nwant %q", client.calls[1], wantSet)
	}
}

// TestAWorkItemCensusIsNeverFilteredOnItsOwnRepository: the entity tree
// relates a work item to a repository only through its linked pull requests,
// so a repository narrowing never reaches the work item's own repository
// column; the census round scopes a work item by the link walk instead.
func TestAWorkItemCensusIsNeverFilteredOnItsOwnRepository(t *testing.T) {
	t.Parallel()
	client, outcome := runFilteredCensus(t, contextfabric.SubjectWorkItem, "CHAOS-77", []string{"ACME/Repo-25", "acme/*"})
	if outcome.RepositoryFilterApplied {
		t.Fatalf("RepositoryFilterApplied = true, want false: a work item is not scoped by its own repository")
	}
	for i, statement := range client.calls {
		if strings.Contains(statement, "FROM repos") || strings.Contains(statement, "w.repo_id) IN") {
			t.Fatalf("statement %d = %q, want no filter on the work item's own repository", i, statement)
		}
	}
}

func TestWorkItemCensusRepositoryAnchorStaysUnavailable(t *testing.T) {
	t.Parallel()
	client := &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"o:r1:CHAOS-77"}}
	_, err := devhealthsource.NewCensusFunc(client)(context.Background(), "org_1", contextfabric.SubjectWorkItem, "CHAOS-77", true, contextfabric.SubjectRepository, "repository:acme/repo", true)
	if err == nil {
		t.Fatalf("a repository anchor on a work item census must stay refused: a repo-less item carries the zero repo id")
	}
}
