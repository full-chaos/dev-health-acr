package devhealthfacts_test

// The ownership read of a team rollup runs on the production ClickHouse client,
// whose max_result_rows is 1000 with result_overflow_mode throw. The read pages
// under that cap and the per-repository reads run in key chunks, so a team that
// owns more than 1000 repositories is served in full up to the 2000 bound; one
// past the bound is a named read failure, never a partial list. Executed
// against a real ClickHouse through the real deployments provider. The
// container is mandatory: a missing one fails the test, never skips it.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	ownershipBoundOrg = "6d1f0a52-37c4-4b8e-a9d0-5e2c7b9f1a43"
	clientRowCap      = 1000
)

func TestTeamOwnershipReadMeetsTheClientRowCapOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("repos", "team_repo_ownership", "deploy_metrics_daily", "projects", "team_project_ownership", "work_items", "work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	// seed gives teamID exactly n owned repositories: n repos rows and n
	// ownership rows, ids offset so two teams never share a repository.
	seed := func(teamID, prefix string, offset, n int) {
		t.Helper()
		id := `toUUID(concat('00000000-0000-4000-8000-', leftPad(toString(number + ` + strconv.Itoa(offset) + `), 12, '0')))`
		name := `concat('acme/` + prefix + `-', toString(number))`
		for _, statement := range []string{
			`INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT ` + id + `, '` + ownershipBoundOrg + `', ` + name + `, 'github', now64(3) FROM numbers(` + strconv.Itoa(n) + `)`,
			`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) SELECT '` + ownershipBoundOrg + `', 'github', '` + teamID + `', ` + id + `, ` + name + `, 'exact', 'native', 1, 100, 0, toDateTime64('2026-01-01 00:00:00', 3, 'UTC'), NULL, now64(3) FROM numbers(` + strconv.Itoa(n) + `)`,
		} {
			if err := direct.Exec(ctx, statement); err != nil {
				t.Fatalf("seed %s: %v\n%s", teamID, err, statement)
			}
		}
	}
	type teamCase struct {
		team  string
		count int
	}
	cases := []teamCase{
		{"team-at-cap", clientRowCap},
		{"team-over-cap", clientRowCap + 1},
		{"team-at-bound", 2000},
		{"team-over-bound", 2001},
	}
	offset := 0
	for _, c := range cases {
		seed(c.team, c.team, offset, c.count)
		offset += c.count
	}

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactDeployments)
	read := func(team string) (contextfabric.FactProviderResult, error) {
		return provider.ReadFacts(ctx, storage.Principal{OrgID: ownershipBoundOrg}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactDeployments,
			Subjects: []contextfabric.SubjectRef{teamSubject(team)},
		})
	}

	for _, c := range cases[:3] {
		c := c
		t.Run(strconv.Itoa(c.count)+" owned repositories are served in full", func(t *testing.T) {
			result, err := read(c.team)
			if err != nil {
				t.Fatalf("a team owning %d repositories must be served, got %v", c.count, err)
			}
			wantInt(t, factFor(t, result, teamSubject(c.team)).Fields, "owned_repository_count", int64(c.count))
		})
	}

	t.Run("one past the 2000 bound is a named read failure", func(t *testing.T) {
		result, err := read("team-over-bound")
		if err == nil {
			t.Fatalf("a team owning 2001 repositories must be rejected, got a served result")
		}
		var failure *contextfabric.FactReadFailure
		if !errors.As(err, &failure) {
			t.Fatalf("rejection is not a FactReadFailure: %T %v", err, err)
		}
		if failure.State != contextfabric.SourceUnavailable {
			t.Fatalf("State = %v, want %v", failure.State, contextfabric.SourceUnavailable)
		}
		if !strings.Contains(failure.Reason, "query team deployments failed") {
			t.Fatalf("Reason = %q", failure.Reason)
		}
		if got := readFailureCause(err); !strings.Contains(got, "query team owned repositories exceeded its bound") {
			t.Fatalf("Cause = %q, want the named ownership bound failure", got)
		}
		if len(result.Facts) != 0 {
			t.Fatalf("a failed read served facts: %#v", result.Facts)
		}
	})
}
