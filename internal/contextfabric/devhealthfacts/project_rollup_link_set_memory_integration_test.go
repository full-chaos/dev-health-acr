package devhealthfacts_test

import (
	"context"
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
