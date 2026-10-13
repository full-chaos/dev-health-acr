package devhealthfacts_test

import (
	"context"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const (
	projectRollupLinkSetSmallLinks = 100_000
	projectRollupLinkSetMoreLinks  = 200_000
	projectRollupLinkSetSlope      = 1.15
	projectRollupLinkSetFloor      = uint64(2 << 20)
	projectRollupLinkSetWarmRuns   = 3
)

// The project pull request rollup reads the links of the requested project's
// issues only. Links of issues outside the project, in any number, leave the
// statement's peak memory flat: the rule fails a growth over 1.15x that is
// also over 2 MiB, taken on the smallest of three warm runs per size.
func TestProjectPullRequestRollupMemoryIgnoresLinksOutsideTheProjectOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	s := newProjectRollupSeed(t, ctx, direct)
	s.workItem("i-native", "repo-a", "issue", "open", "proj-p")
	s.workItem("i-heuristic", "repo-a", "issue", "open", "proj-p")
	s.pullRequest("repo-a", 1, s.at, nil, nil)
	s.pullRequest("repo-a", 2, s.at, nil, nil)
	s.link("repo-a", "i-native", 1, "native")
	s.link("repo-a", "i-heuristic", 2, "heuristic")
	outsideLinks := func(prefix string, count int) {
		t.Helper()
		s.exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat(?, repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?)`,
			repoUUID("repo-a"), prefix, s.at, projectRollupOrg, uint64(count))
	}
	project := projectRollupRef(t, "proj-p")
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	principal := storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"*"}}
	filter := statementFilter{Table: "work_graph_issue_pr", QueryContains: "lp.project_key"}
	peak := func() uint64 {
		t.Helper()
		var smallest uint64
		for run := 0; run < projectRollupLinkSetWarmRuns; run++ {
			mark := serverNowMicros(t, ctx, direct)
			result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
				Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{project}, Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			})
			memory := statementMemorySince(t, ctx, direct, filter, mark)
			if err != nil {
				t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
			}
			wantInt(t, factFor(t, result, project).Fields, "pull_requests_open_current", 2)
			if len(memory) != 1 {
				t.Fatalf("measurement did not happen: %d distinct project rollup statements logged, want 1", len(memory))
			}
			for _, bytes := range memory {
				if run == 0 || bytes < smallest {
					smallest = bytes
				}
			}
		}
		return smallest
	}
	outsideLinks("outside-", projectRollupLinkSetSmallLinks)
	peak()
	small := peak()
	outsideLinks("outside-more-", projectRollupLinkSetMoreLinks)
	large := peak()
	t.Logf("project pull request rollup peak memory: %d bytes at %d outside links, %d at %d", small, projectRollupLinkSetSmallLinks, large, projectRollupLinkSetSmallLinks+projectRollupLinkSetMoreLinks)
	if float64(large) > projectRollupLinkSetSlope*float64(small) && large > small+projectRollupLinkSetFloor {
		t.Errorf("project pull request rollup grew with links outside the project: peak %d bytes at the larger size, %d at the smaller", large, small)
	}
}

// On one dataset the project pull request rollup returns the same rows when
// its scalar reads the project's links as when it builds the organization's
// link-id set, and the served fact carries the project's links per tier.
func TestProjectPullRequestRollupRowsEqualTheOrgLinkSetFormOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	s := newProjectRollupSeed(t, ctx, direct)
	s.workItem("i-native", "repo-a", "issue", "open", "proj-p")
	s.workItem("i-heuristic", "repo-a", "issue", "open", "proj-p")
	s.workItem("i-text", "repo-a", "issue", "open", "proj-p")
	s.workItem("i-unlinked", "repo-a", "issue", "open", "proj-p")
	s.workItem("pr-item", "repo-a", "pr", "open", "proj-p")
	s.workItem("i-other", "repo-a", "issue", "open", "proj-q1")
	s.workItem("i-none", "repo-a", "issue", "open", "")
	for n := uint32(1); n <= 5; n++ {
		s.pullRequest("repo-a", n, s.at, nil, nil)
	}
	s.link("repo-a", "i-native", 1, "native")
	s.link("repo-a", "i-heuristic", 2, "heuristic")
	s.link("repo-a", "i-heuristic", 1, "heuristic")
	s.link("repo-a", "i-text", 3, "explicit_text")
	s.link("repo-a", "pr-item", 4, "native")
	s.link("repo-a", "i-other", 5, "native")
	s.link("repo-a", "i-none", 5, "native")
	s.link("repo-a", "i-native", 99, "native")
	recorder := &recordingQueryClient{inner: query}
	project := projectRollupRef(t, "proj-p")
	provider := findProvider(t, devhealthfacts.NewProviders(recorder), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{project}, Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
	}
	wantInt(t, factFor(t, result, project).Fields, "pull_requests_open_current", 3)
	served, ok := recorder.find("lp.project_key")
	if !ok {
		t.Fatal("the project rollup statement was not sent")
	}
	old, rewritten := devhealthfacts.OrgLinkSetProjectRollupStatementForTest(served.statement)
	if !rewritten {
		t.Fatal("the served statement does not carry the project scalar")
	}
	got := scopeRows(t, ctx, query, served)
	want := scopeRows(t, ctx, query, capturedStatement{statement: old, bindings: served.bindings})
	if len(got) == 0 {
		t.Fatal("the statement returned no rows, the comparison did not happen")
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows differ from the organization link-set form:\nnew %q\nold %q", got, want)
	}
}
