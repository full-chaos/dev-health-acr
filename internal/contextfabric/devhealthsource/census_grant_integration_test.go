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
		nestedGitLab:  repository("nested-gitlab", "acme/group/secret", "gitlab"),
		malformed:     repository("malformed", "acme/trailing-", "github"),
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

func TestAnOwnerGrantReadsOnlyWhatTheGateAdmitsOnClickHouse(t *testing.T) {
	anOwnerGrantReadsOnlyWhatTheGateAdmits(t, grantClickHouseWorld)
}

// grantedItemClickHouseWorld is grantedItemModelWorld on real ClickHouse. A native link reads
// from the link table of record; a pull request row is not needed for the census.
func grantedItemClickHouseWorld(t *testing.T, name string) grantedItemWorld {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour)
	_, direct := orgIsolationClickHouseFixture(t)
	base := grantClickHouseWorld(t, name)
	links, items := &[]issueLink{}, &[]linkItem{}
	world := grantedItemWorld{grantWorld: base, links: links, items: items}
	world.addItem = func(t *testing.T, item linkItem) {
		t.Helper()
		*items = append(*items, item)
		repo := repoLessRepoID
		if item.repository.id != "" {
			repo = item.repository.id
		}
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, toUUID(?), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.id, repo, base.org, "item", "open", "", "", item.provider, "", now, now); err != nil {
			t.Fatalf("seed work item: %v", err)
		}
	}
	world.addLink = func(t *testing.T, link issueLink) {
		t.Helper()
		*links = append(*links, link)
		if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (toUUID(?), ?, ?, 0.9, ?, '', ?, ?)`,
			link.repository.id, link.item, grantNumber, link.tier, now, base.org); err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}
	return world
}

func TestOutsideLinksChangeNothingForARestrictedCaller(t *testing.T) {
	outsideLinksChangeNothing(t, grantedItemClickHouseWorld)
}

func TestAnIdWithARowOutsideTheArmIsNotCountedOnClickHouse(t *testing.T) {
	anIdWithARowOutsideTheArmIsNotCounted(t, grantedItemClickHouseWorld)
}

func TestALowerTierLinkNeverGrantsOnClickHouse(t *testing.T) {
	aLowerTierLinkNeverGrants(t, grantedItemClickHouseWorld)
}

func TestAProjectOwnedItemWithNoNativeLinkStaysDeniedOnClickHouse(t *testing.T) {
	aProjectOwnedItemWithNoNativeLinkStaysDenied(t, grantedItemClickHouseWorld)
}

func TestANativeLinkBesideAnOutsideOneAdmitsOnClickHouse(t *testing.T) {
	aNativeLinkBesideAnOutsideOneAdmits(t, grantedItemClickHouseWorld)
}

func TestAWorkItemOwnerGrantAdmitsWhatTheGateAdmitsOnClickHouse(t *testing.T) {
	aWorkItemOwnerGrantAdmitsWhatTheGateAdmits(t, grantedItemClickHouseWorld)
}
