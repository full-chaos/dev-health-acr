package devhealthfacts_test

// The ownership read of a team rollup is bounded at 2000 repositories,
// inclusive: it reads one row past the bound and rejects only on overflow.
// Executed against a real ClickHouse through the real deployments provider.
// The container is mandatory: a missing one fails the test, never skips it.

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
	ownershipBoundOrg  = "6d1f0a52-37c4-4b8e-a9d0-5e2c7b9f1a43"
	ownershipBoundRows = 2000
)

func TestTeamOwnershipReadBoundAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("repos", "team_repo_ownership", "deploy_metrics_daily") {
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
	seed("team-at-bound", "at", 0, ownershipBoundRows)
	seed("team-over-bound", "over", ownershipBoundRows, ownershipBoundRows+1)

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactDeployments)
	read := func(team string) (contextfabric.FactProviderResult, error) {
		return provider.ReadFacts(ctx, storage.Principal{OrgID: ownershipBoundOrg}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactDeployments,
			Subjects: []contextfabric.SubjectRef{teamSubject(team)},
		})
	}

	t.Run("exactly the bound is served", func(t *testing.T) {
		result, err := read("team-at-bound")
		if err != nil {
			t.Fatalf("a team owning exactly %d repositories must be served, got %v", ownershipBoundRows, err)
		}
		wantInt(t, factFor(t, result, teamSubject("team-at-bound")).Fields, "owned_repository_count", ownershipBoundRows)
	})

	t.Run("one past the bound is rejected with the disclosed reason", func(t *testing.T) {
		_, err := read("team-over-bound")
		if err == nil {
			t.Fatalf("a team owning %d repositories must be rejected, got a served result", ownershipBoundRows+1)
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
		if got := readFailureCause(err); got != "query team owned repositories exceeded its bound" {
			t.Fatalf("Cause = %q, want the overflow reason", got)
		}
	})
}
