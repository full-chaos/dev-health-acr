package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// grantClickHouseWorld is grantModelWorld on real ClickHouse. A subtest gets
// its own organization; a turn reads the rows written so far.
func grantClickHouseWorld(t *testing.T, name string) grantWorld {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour)
	query, direct := orgIsolationClickHouseFixture(t)
	org := sharedTestOrgID(t) + "-" + sanitizeOrgSuffix(strings.ReplaceAll(name, " ", "-"))
	repository := func(label, slug, provider string) grantRepository {
		t.Helper()
		id := o3UUID(org + "-" + label)
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, id, org, slug, provider, now); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
		return grantRepository{id, slug, provider}
	}
	return grantWorld{
		org:           org,
		insideGitHub:  repository("inside-github", "acme/alpha", "github"),
		insideGitLab:  repository("inside-gitlab", "group/beta", "gitlab"),
		outsideGitHub: repository("outside-github", "other/secret", "github"),
		outsideGitLab: repository("outside-gitlab", "elsewhere/hidden", "gitlab"),
		client:        query,
		add: func(t *testing.T, repository grantRepository) {
			t.Helper()
			if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, repository.id, org, grantNumber, fmt.Sprintf("PR %d", grantNumber), "open", now); err != nil {
				t.Fatalf("seed pull request: %v", err)
			}
		},
	}
}

func TestOutsideRowsChangeNothingForARestrictedCaller(t *testing.T) {
	outsideRowsChangeNothing(t, grantClickHouseWorld)
}

func TestAnOutsideSatisfierIsNeverCountedNamedOrOfferedOnClickHouse(t *testing.T) {
	anOutsideSatisfierIsNeverCountedNamedOrOffered(t, grantClickHouseWorld)
}

func TestAnInsideSatisfierBesideAnOutsideOneCommitsOnClickHouse(t *testing.T) {
	anInsideSatisfierBesideAnOutsideOneCommits(t, grantClickHouseWorld)
}
