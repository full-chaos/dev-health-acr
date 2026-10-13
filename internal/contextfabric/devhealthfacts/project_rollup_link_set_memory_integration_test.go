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
	projectRollupLinkSetWarmRuns = 3
	// Measured in CI (non-race job, smallest of three warm runs): 56.5 MB at
	// 100k links, 122-124 MB at 200k, 135.5 MB at 300k, merged or not. The
	// bound is 650 B per organization link plus 5 MiB.
	projectRollupLinkSetBytesPerLink = 650
	projectRollupLinkSetBaseBytes    = uint64(5 << 20)
)

// The project pull request rollup builds the organization's link-id set, so
// its peak memory is linear in the organization's links and flat in the
// project. This row bounds the peak at three link counts, in both part
// states, as the smallest of three warm runs: a per-link cost above the
// measured one (the set built twice) fails it.
func TestProjectPullRequestRollupMemoryAgainstOrganizationLinksOnRealClickHouse(t *testing.T) {
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
	peak()
	links := 0
	for _, step := range []struct {
		prefix string
		add    int
	}{{"a-", 100_000}, {"b-", 100_000}, {"c-", 100_000}} {
		outsideLinks(step.prefix, step.add)
		links += step.add
		unmerged := peak()
		if err := direct.Exec(ctx, `OPTIMIZE TABLE work_graph_issue_pr FINAL`); err != nil {
			t.Fatalf("optimize: %v", err)
		}
		merged := peak()
		t.Logf("links=%d unmerged_peak=%d merged_peak=%d", links, unmerged, merged)
		bound := uint64(links)*projectRollupLinkSetBytesPerLink + projectRollupLinkSetBaseBytes
		for state, got := range map[string]uint64{"unmerged": unmerged, "merged": merged} {
			if got > bound {
				t.Errorf("project pull request rollup peak %d bytes at %d organization links (%s parts), over the %d-byte bound", got, links, state, bound)
			}
		}
	}
}
