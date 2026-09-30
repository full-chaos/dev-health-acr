package devhealthsource_test

// CHAOS-7130: a team's authorization_repositories list is built from RESOLVED
// ownership (ownershipresolve, the rule the repository->team edge and the fact
// reads share), not from raw repo_full_name values. Asserted on what
// NextProjectionBatch hands the graph, on a real ClickHouse.

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func chaos7130Repo(t *testing.T, ctx context.Context, f *ownershipFixture, id, name, provider string, syncedAt time.Time) {
	t.Helper()
	mustExec(t, ctx, f.direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`,
		id, name, nil, syncedAt, nil, syncedAt, f.orgID, provider)
}

func chaos7130List(res chaos7139Result, team string) []string {
	got := append([]string(nil), res.entities[team].Authorization.RepositorySlugs...)
	sort.Strings(got)
	return got
}

// Seeds: repos github acme/repo-k, acme/repo-b; gitlab acme/gl-only.
//
//	team-ghost     "acme/ghost" (no repos row) + "acme/repo-k"   -> [acme/repo-k]
//	team-glob      "acme/*" + "acme/repo-b"                       -> [acme/repo-b]
//	team-case      "ACME/Repo-K" (NULL repo_id)                   -> [acme/repo-k] (canonical)
//	team-dup       "ACME/REPO-B" NULL id + "acme/repo-b" id row   -> [acme/repo-b] ONCE
//	team-provider  gitlab "acme/repo-k" (github repo)             -> sentinel (no resolved ownership)
//	team-ghostonly "acme/ghost2"                                  -> sentinel
func subCHAOS7130TeamListUsesResolvedOwnership(t *testing.T, ctx context.Context, f *ownershipFixture) {
	at := time.Now().UTC().Truncate(time.Second)
	chaos7130Repo(t, ctx, f, chaos7119RepoK, "acme/repo-k", "github", at)
	chaos7130Repo(t, ctx, f, chaos7119RepoB, "acme/repo-b", "github", at)
	for _, team := range []string{"team-ghost", "team-glob", "team-case", "team-dup", "team-provider", "team-ghostonly"} {
		chaos7119Team(t, ctx, f, team, at)
	}
	chaos7119Own(t, ctx, f, "team-ghost", "github", "acme/ghost", nil, at)
	chaos7119Own(t, ctx, f, "team-ghost", "github", "acme/repo-k", nil, at)
	chaos7119Own(t, ctx, f, "team-glob", "github", "acme/*", nil, at)
	chaos7119Own(t, ctx, f, "team-glob", "github", "acme/repo-b", nil, at)
	chaos7119Own(t, ctx, f, "team-case", "github", "ACME/Repo-K", nil, at)
	chaos7119Own(t, ctx, f, "team-dup", "github", "ACME/REPO-B", nil, at)
	chaos7119Own(t, ctx, f, "team-dup", "github", "acme/repo-b", chaos7119RepoB, at)
	chaos7119Own(t, ctx, f, "team-provider", "gitlab", "acme/repo-k", nil, at)
	chaos7119Own(t, ctx, f, "team-ghostonly", "github", "acme/ghost2", nil, at)
	// Source conflict (CHAOS-2600: a later manual close never cancels an open
	// native assertion): native open + a LATER manual close for one repository.
	chaos7119Team(t, ctx, f, "team-conflict", at)
	chaos7119Own(t, ctx, f, "team-conflict", "github", "acme/repo-b", nil, at.Add(-time.Hour))
	mustExec(t, ctx, f.direct,
		`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.orgID, "github", "team-conflict", nil, "acme/repo-b", "exact", "manual", uint8(1), uint16(100), int32(0), at.Add(-time.Minute), at.Add(-30*time.Second), at)
	// Orphan repo_id (no repos row): the edge keeps a sentinel edge, the list drops it.
	chaos7119Team(t, ctx, f, "team-orphan", at)
	chaos7119Own(t, ctx, f, "team-orphan", "github", "acme/orphan", chaos7119Orphan, at)
	// Future-valid assertion: not currently owned, so not listed.
	chaos7119Team(t, ctx, f, "team-future", at)
	mustExec(t, ctx, f.direct,
		`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.orgID, "github", "team-future", nil, "acme/repo-k", "exact", "native", uint8(1), uint16(100), int32(0), at.Add(24*time.Hour), nil, at)

	res := chaos7139Run(t, ctx, f)
	sentinel := []string{devhealthsource.NoTeamOwnershipSentinelForTest()}
	for team, want := range map[string][]string{
		"team-ghost":     {"acme/repo-k"},
		"team-glob":      {"acme/repo-b"},
		"team-case":      {"acme/repo-k"},
		"team-dup":       {"acme/repo-b"},
		"team-provider":  sentinel,
		"team-ghostonly": sentinel,
		"team-conflict":  {"acme/repo-b"},
		"team-orphan":    sentinel,
		"team-future":    sentinel,
	} {
		if _, ok := res.entities[team]; !ok {
			t.Fatalf("%s: team entity not projected", team)
		}
		if got := chaos7130List(res, team); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: authorization repositories %v, want %v", team, got, want)
		}
	}
	// PARITY ORACLE: for every team, the authorization list EQUALS the set of
	// repositories the edge producer resolves as OPEN ownership for it (a
	// sentinel list equals the empty edge set). Neither side can drift.
	for team := range res.entities {
		got := chaos7130List(res, team)
		if len(got) == 1 && (got[0] == devhealthsource.NoTeamOwnershipSentinelForTest() || got[0] == devhealthsource.OverBoundTeamOwnershipSentinelForTest()) {
			got = nil
		}
		want := append([]string(nil), res.openEdgeSlugs[team]...)
		sort.Strings(want)
		dedup := want[:0]
		for i, s := range want {
			if i == 0 || s != want[i-1] {
				dedup = append(dedup, s)
			}
		}
		if len(got) == 0 && len(dedup) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, dedup) {
			t.Errorf("%s: team list %v != open resolved edge repositories %v (list and edge disagree)", team, got, dedup)
		}
	}
}

// A repos row arriving AFTER the ownership row (ownership row untouched)
// re-projects the team with the resolved slug: the watermark folds in the
// repos row's last_synced.
func subCHAOS7130LateReposRowReprojectsTheTeam(t *testing.T, ctx context.Context, f *ownershipFixture) {
	ownedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	chaos7119Team(t, ctx, f, "team-late", ownedAt)
	chaos7119Own(t, ctx, f, "team-late", "github", "Acme/Late", nil, ownedAt)
	first := chaos7139Run(t, ctx, f)
	if got := chaos7130List(first, "team-late"); !reflect.DeepEqual(got, []string{devhealthsource.NoTeamOwnershipSentinelForTest()}) {
		t.Fatalf("before the repos row exists the team must be denied by the sentinel, got %v", got)
	}
	chaos7130Repo(t, ctx, f, "71300000-0000-4000-8000-0000000000aa", "acme/late", "github", time.Now().UTC().Add(-time.Minute).Truncate(time.Second))
	second := chaos7139RunFrom(t, ctx, f, first.cursor)
	if _, ok := second.entities["team-late"]; !ok {
		t.Fatalf("team-late was not re-projected after its repos row arrived (watermark misses repos.last_synced)")
	}
	if got := chaos7130List(second, "team-late"); !reflect.DeepEqual(got, []string{"acme/late"}) {
		t.Errorf("after the repos row: %v, want [acme/late]", got)
	}
}
