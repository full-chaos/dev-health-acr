package falkorgraph

import (
	"context"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12319: a requested team narrowing on the repository work-item walk.
// Under scope.team_ids an issue is admitted when (i) it is an issue of the
// anchor (an actual link row) and the anchor is currently OWNED_BY_TEAM a
// requested team, or (ii) its own project (BELONGS_TO_PROJECT) is currently
// OWNED_BY_TEAM a requested team. An exclusion is scope, never a denial; the
// walk is floored (truncated) only where the ownership it needed is absent,
// not current, or cut; and when (i) fails the anchor's own totals are not the
// team's (AnchorOutsideTeamNarrowing).
func TestLiveATeamNarrowingDecidesTheRepositoryWalkByOwnership(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	assertTeamNarrowingInTheRepositoryWalk(t, ctx, adapter)
}

type teamNarrowingFixture struct {
	// anchorOwnerEnded: the anchor's only ownership edge ended an hour ago.
	anchorOwnerEnded bool
	// unownedProject: the gitlab issue belongs to a project no team owns.
	unownedProject bool
}

func seedTeamNarrowingFixture(t *testing.T, ctx context.Context, adapter *Adapter, f teamNarrowingFixture) string {
	t.Helper()
	orgID := "live-12319-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Now().UTC()
	entities, relationships := newLiveMemberFixture(orgID, observed, func(add func(contextfabric.SubjectRef, contextfabric.AuthorizationScope, string), relate func(string, contractsv1.ContextFabricRelationshipType, contextfabric.SubjectRef, contextfabric.SubjectRef, contextfabric.AuthorizationScope, string)) {
		repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
		team := func(id string) contextfabric.SubjectRef {
			ref := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + id, Label: id}
			add(ref, contextfabric.AuthorizationScope{TeamIDs: []string{id}}, "")
			return ref
		}
		project := func(id string) contextfabric.SubjectRef {
			ref := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:" + id, Label: id}
			add(ref, contextfabric.AuthorizationScope{ProjectIDs: []string{id}}, "")
			return ref
		}
		issue := func(id string) contextfabric.SubjectRef {
			return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id, Label: "issue"}
		}
		teamB, teamC := team("team-B"), team("team-C")
		team("team-A")
		p := project("proj-p")
		relate("anchor_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, repo, teamB, contextfabric.AuthorizationScope{RepositorySlugs: []string{memberAnchorSlug}, TeamIDs: []string{"team-B"}}, "")
		relate("p_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, p, teamC, contextfabric.AuthorizationScope{ProjectIDs: []string{"proj-p"}, TeamIDs: []string{"team-C"}}, "")
		// (ii) for a repository-less issue and for an issue with its own repository.
		relate("linear_p", contractsv1.ContextFabricRelationshipBelongsToProject, issue(memberLinearIssue), p, contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}, ProjectIDs: []string{"proj-p"}}, "")
		relate("github_p", contractsv1.ContextFabricRelationshipBelongsToProject, issue(memberGithubIssue), p, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/gh-issues"}, ProjectIDs: []string{"proj-p"}}, "")
		if f.unownedProject {
			p2 := project("proj-unowned")
			relate("gitlab_p2", contractsv1.ContextFabricRelationshipBelongsToProject, issue(memberGitlabIssue), p2, contextfabric.AuthorizationScope{RepositorySlugs: []string{"grp/gl-issues"}, ProjectIDs: []string{"proj-unowned"}}, "")
		}
	}, liveMemberLinks())
	if f.anchorOwnerEnded {
		for i := range relationships.Relationships {
			if relationships.Relationships[i].RelationshipID == "relationship_tree_members_anchor_owner" {
				from, to := observed.Add(-48*time.Hour), observed.Add(-time.Hour)
				relationships.Relationships[i].ValidFrom, relationships.Relationships[i].ValidTo = &from, &to
			}
		}
	}
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	return orgID
}

func assertTeamNarrowingInTheRepositoryWalk(t *testing.T, ctx context.Context, adapter *Adapter) {
	t.Helper()
	all := []string{memberGithubIssue, memberGitlabIssue, memberLinearIssue, memberJiraIssue, "work_item.v2:a:both", "work_item.v2:a:text", "work_item.v2:a:text-less", "work_item.v2:a:weak"}
	sort.Strings(all)
	type want struct {
		members   []string
		outside   bool
		truncated bool
		// The decision basis the walk's Info line carries.
		basis    contextfabric.TeamNarrowingBasis
		excluded int
		floor    contextfabric.TeamNarrowingFloor
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	check := func(t *testing.T, orgID string, grants []string, teams []string, w want) {
		t.Helper()
		principal := storage.Principal{OrgID: orgID, RepositoryScopes: grants}
		binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
		if err != nil {
			t.Fatalf("ResolveInvestigationBinding() error = %v", err)
		}
		walk, err := adapter.TreeWorkItemMembers(ctx, principal, binding, contextfabric.RequestedScope{TeamIDs: teams}, anchor, 25)
		if err != nil {
			t.Fatalf("teams %v: TreeWorkItemMembers() error = %v", teams, err)
		}
		got := []string{}
		for _, m := range walk.Members {
			got = append(got, m.Subject.CanonicalID)
		}
		sort.Strings(got)
		sort.Strings(w.members)
		if !slices.Equal(got, w.members) {
			t.Fatalf("grants %v teams %v: members = %v, want %v", grants, teams, got, w.members)
		}
		// An exclusion is scope: the authorization count is what the grants hid.
		if walk.Denied != 0 {
			t.Fatalf("grants %v teams %v: denied = %d, want 0 (a narrowing exclusion is not a denial)", grants, teams, walk.Denied)
		}
		if walk.AnchorOutsideTeamNarrowing != w.outside || walk.Truncated != w.truncated {
			t.Fatalf("grants %v teams %v: outside=%v truncated=%v, want outside=%v truncated=%v", grants, teams, walk.AnchorOutsideTeamNarrowing, walk.Truncated, w.outside, w.truncated)
		}
		if walk.TeamNarrowing != w.basis || walk.TeamExcluded != w.excluded || walk.TeamFloor != w.floor {
			t.Fatalf("grants %v teams %v: basis=%q excluded=%d floor=%q, want %q %d %q", grants, teams, walk.TeamNarrowing, walk.TeamExcluded, walk.TeamFloor, w.basis, w.excluded, w.floor)
		}
	}
	t.Run("the anchor is owned by team-B", func(t *testing.T) {
		org := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{})
		check(t, org, nil, nil, want{members: all})
		check(t, org, nil, []string{"team-B"}, want{members: all, basis: contextfabric.TeamNarrowingAnchorOwned})
		check(t, org, nil, []string{"team-B", "team-C"}, want{members: all, basis: contextfabric.TeamNarrowingAnchorOwned})
		check(t, org, nil, []string{"*"}, want{members: all})
		// (i) fails; (ii) admits the issues of team-C's project, a repository-less
		// one and one with its own repository. The jira issue has no project:
		// decided, not floored.
		check(t, org, nil, []string{"team-C"}, want{members: []string{memberGithubIssue, memberLinearIssue}, outside: true, basis: contextfabric.TeamNarrowingIssueProject, excluded: 6})
		check(t, org, nil, []string{"team-A"}, want{members: []string{}, outside: true, basis: contextfabric.TeamNarrowingIssueProject, excluded: 8})
		// A repository-restricted caller: the grants still decide first.
		check(t, org, []string{memberAnchorSlug}, []string{"team-C"}, want{members: []string{memberLinearIssue}, outside: true, basis: contextfabric.TeamNarrowingIssueProject, excluded: 4})
	})
	t.Run("an issue's project has no owner", func(t *testing.T) {
		org := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{unownedProject: true})
		// The ownership (ii) needed is absent: floored, and still not admitted.
		check(t, org, nil, []string{"team-C"}, want{members: []string{memberGithubIssue, memberLinearIssue}, outside: true, truncated: true, basis: contextfabric.TeamNarrowingIssueProject, excluded: 6, floor: contextfabric.TeamNarrowingFloorProjectOwnerAbsent})
		// (i) decides every issue; nothing else is read.
		check(t, org, nil, []string{"team-B"}, want{members: all, basis: contextfabric.TeamNarrowingAnchorOwned})
	})
	t.Run("the anchor's ownership is not current", func(t *testing.T) {
		org := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{anchorOwnerEnded: true})
		check(t, org, nil, []string{"team-B"}, want{members: []string{}, outside: true, truncated: true, basis: contextfabric.TeamNarrowingIssueProject, excluded: 8, floor: contextfabric.TeamNarrowingFloorAnchorOwnershipUnknown})
		check(t, org, nil, []string{"team-C"}, want{members: []string{memberGithubIssue, memberLinearIssue}, outside: true, truncated: true, basis: contextfabric.TeamNarrowingIssueProject, excluded: 6, floor: contextfabric.TeamNarrowingFloorAnchorOwnershipUnknown})
	})
}
