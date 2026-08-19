package devhealthsource_test

// CHAOS-3898 D-6: the same bare natural-key fields D-1..D-4 used as
// canonical ids (c.run_id, r.review_id, d.deployment_id, w.work_item_id --
// design brief §0) were ALSO the keyset-pagination rowKeys queryCIRuns,
// queryPullRequestReviews, queryDeployments, and queryWorkItems order and
// page by. A rowKey that ties across repos is the same root cause as
// D-1..D-4 applied to pagination instead of canonical identity: two
// different repos' rows landing on the same (timestamp, rowKey) cursor
// position, where keyset pagination can only ever return one of them per
// position -- the other is silently skipped at a page boundary rather than
// merely mis-identified.
//
// This is pinned at the SQL-text level, the same shape as
// chaos3898_d7_join_fix_test.go's TestD7_DeploymentIncidentEdgeJoinIsRepoQualified
// and for the identical reason (see that file's own doc comment): the fake
// ClickHouseQueryClient this package's test suite uses does not evaluate
// the SQL statement text itself -- it resolves canned rows via a Go-side
// cursorOf callback that must be kept in sync BY HAND with the production
// rowKey expression (see workItemCursorOf's own doc comment). A
// data-level pagination test through that fake therefore cannot actually
// prove production's rowKey is repo-qualified -- it can only prove the
// fake's OWN assumption is internally consistent, which is not evidence
// about the production SQL text at all. Pinning the SQL text directly
// catches exactly the regression this defect is about: someone dropping
// the repo_id qualifier from the rowKey expression in a future edit.

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestD6_PaginationRowKeysAreRepoQualified(t *testing.T) {
	t.Parallel()

	recorder := &statementRecordingClient{inner: &fakeClient{tables: baseTables(zeroTime)}}
	source, err := devhealthsource.NewClickHouseProjectionSource(recorder)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	if _, _, err := source.NextProjectionBatch(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err != nil {
		t.Fatalf("next projection batch: %v", err)
	}

	cases := []struct {
		kind          string
		match         string
		wantSubstring string
	}{
		{"ci_pipeline_run", "FROM ci_pipeline_runs AS c", "concat(toString(c.repo_id), ':', c.run_id)"},
		{"pull_request_review", "FROM git_pull_request_reviews AS r", "concat(toString(r.repo_id), ':', toString(r.number), ':', r.review_id)"},
		{"deployment", "FROM deployments AS d", "concat(toString(d.repo_id), ':', d.deployment_id)"},
		{"work_item", "FROM work_items AS w", "concat(toString(w.repo_id), ':', w.work_item_id)"},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			var statement string
			for _, s := range recorder.statements {
				if strings.Contains(s, c.match) {
					statement = s
					break
				}
			}
			if statement == "" {
				t.Fatalf("no statement queried %q", c.match)
			}
			if !strings.Contains(statement, c.wantSubstring) {
				t.Errorf("D-6 regression: %s's pagination rowKey is missing the repo_id qualifier.\nwant substring: %s\ngot statement:\n%s", c.kind, c.wantSubstring, statement)
			}
		})
	}
}
