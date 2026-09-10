package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestChaos5405_AProjectOnlyScopesItsOwnProvidersWorkItems pins codex r2 F4
// against REAL ClickHouse, which is the only place it can be pinned: the
// defect is in a join condition, and a fake client that returns precomputed
// rows cannot execute a join.
//
// Two providers, ONE project id string. The work item belongs to provider
// "jira"; the only project carrying that join key belongs to provider
// "linear". Before the fix, asking for the LINEAR project returned the JIRA
// work item:
//
//	REPRO F4: targets=1 candidate_count=1 authorized=1
//	REPRO F4: admitted target work_item.v2:...:JIRA-1 (JIRA-1)
//
// The org-wide ambiguity guard does not fire: exactly one PROJECT row carries
// this join key, so key_resolution_count = 1. The row was unambiguous and it
// was still the wrong provider's, because `p.join_key = w.project_id` carried
// no `p.provider = w.provider`.
//
// The shape is not new to this codebase. CHAOS-4108's resolvedProjectsSubquery
// (devhealthsource/teams_projects_edges.go) already resolves project join keys
// PARTITIONED BY (provider, join_key) -- proved live there. The work-item
// chain was the arm that did not carry it.
func TestChaos5405_AProjectOnlyScopesItsOwnProvidersWorkItems(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Second)

	const sharedID = "SHARED-PROJ"
	must := func(label, stmt string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, stmt, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	must("linear project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sharedID, orgID, "Linear Titan", "LT", "linear", "active", "", uint8(1), at)
	must("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos5405LiveRepoID, orgID, chaos5405LiveRepoSlug, "github", at)
	// A JIRA work item whose project_id is the same STRING as the linear
	// project's id. In a single-provider world these are the same key; they
	// are not, and never were, the same project.
	must("jira work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"JIRA-1", chaos5405LiveRepoID, orgID, "jira", "jira issue", "open", "", "", sharedID, at)
	// The LINEAR work item that legitimately belongs to it. Without this the
	// assertion below is also satisfied by a join that matches nothing at all,
	// which is the failure mode a provider condition is most likely to cause.
	must("linear work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"LIN-1", chaos5405LiveRepoID, orgID, "linear", "linear issue", "open", "", "", sharedID, at)

	expandFor := func(t *testing.T, provider string) contextfabric.FactScopeExpansionResult {
		t.Helper()
		canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, sharedID}, nil)
		if err != nil || omitted {
			t.Fatalf("derive %s project canonical id: err=%v omitted=%v", provider, err, omitted)
		}
		result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
			Principal:       storage.Principal{OrgID: orgID},
			RequirementKind: contextfabric.FactStatus,
			Origins:         []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID, Label: provider + " project"}},
			Policy:          contextfabric.FactScopePolicyProjectWorkItemStatus,
			TargetKind:      contextfabric.SubjectWorkItem,
			TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Limit:           200,
		})
		if err != nil {
			t.Fatalf("ExpandFactScope(%s): %v", provider, err)
		}
		return result
	}

	admitted := func(result contextfabric.FactScopeExpansionResult) map[string]bool {
		byLabel := map[string]bool{}
		for _, target := range result.Targets {
			byLabel[target.Label] = true
		}
		return byLabel
	}

	t.Run("the linear project scopes the linear work item and NOT the jira one", func(t *testing.T) {
		result := expandFor(t, "linear")
		got := admitted(result)
		if got["JIRA-1"] {
			t.Errorf("a provider \"jira\" work item was admitted as scope for a provider \"linear\" project -- the join matched p.join_key = w.project_id with no provider condition")
		}
		if !got["LIN-1"] {
			t.Errorf("the provider \"linear\" work item was NOT admitted -- the provider condition is excluding the rows it exists to keep; targets=%v", got)
		}
		if result.Counts.CandidateCount != 1 {
			t.Errorf("candidate_count = %d, want 1 -- the census must describe the SAME provider-scoped relation the page came from, not the unfiltered join",
				result.Counts.CandidateCount)
		}
	})

	// THE OTHER DIRECTION. A fix that simply hard-coded one provider, or that
	// dropped every row whose provider differed from the ORIGIN's without
	// relating it to the work item, would pass the arm above and fail here.
	t.Run("the jira project scopes the jira work item and NOT the linear one", func(t *testing.T) {
		must("jira project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sharedID, orgID, "Jira Titan", "JT", "jira", "active", "", uint8(1), at)
		result := expandFor(t, "jira")
		got := admitted(result)
		if got["LIN-1"] {
			t.Errorf("a provider \"linear\" work item was admitted as scope for a provider \"jira\" project")
		}
		if !got["JIRA-1"] {
			t.Errorf("the provider \"jira\" work item was NOT admitted; targets=%v", got)
		}
	})

	// THE ORIGIN ROOT REACHES THE RESULT. origin_id is now
	// concat(p.provider, ':', p.id) and the Go map that resolves it is keyed
	// the same way; if those two ever disagree the lookup misses silently and
	// every target loses its origin attribution. Nothing asserted this before,
	// which is why the two could have drifted unnoticed.
	t.Run("each admitted target carries the origin project it was reached through", func(t *testing.T) {
		result := expandFor(t, "linear")
		if len(result.Targets) == 0 {
			t.Fatal("no targets -- the assertion below would be vacuous")
		}
		for _, target := range result.Targets {
			root, ok := result.TargetRoot[contextfabric.FactSubjectKey(target)]
			if !ok {
				t.Fatalf("target %s carries no origin root -- the SQL origin_id and the Go origin map disagree on their key shape", target.Label)
			}
			if root.Kind != contextfabric.SubjectProject {
				t.Fatalf("target %s origin root kind = %q, want project", target.Label, root.Kind)
			}
		}
	})
}

// TestChaos5405_TheTeamTwinJoinsOnEntityIdentityNotAFreeTextKey is the TEAM
// arm's audit of the same question F4 asked of the project arm, pinned rather
// than asserted in a comment (team-lead: "team twin audited and pinned either
// way").
//
// The audit's answer is that the team chain has NO cross-provider collision
// surface, and the reason is structural rather than lucky: it joins
// work_item_team_attributions to work_items on the composite
// (org_id, repo_id, work_item_id) -- the work item's own entity identity --
// not on a free-text key one provider minted and another may reuse. There is
// no `w.project_id`-shaped string in that join for two providers to collide
// on.
//
// So this test does not assert "the provider condition works" -- there is no
// provider condition to assert, and adding one would be a change with no
// defect behind it. It asserts the property that MAKES one unnecessary: two
// work items sharing a work_item_id across providers are distinguished by
// their repo_id, and the team's attribution reaches only the one it names. If
// the join is ever narrowed to work_item_id alone, this goes red, which is the
// mode in which the audit stays true.
func TestChaos5405_TheTeamTwinJoinsOnEntityIdentityNotAFreeTextKey(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Second)

	const collidingID = "ISSUE-7"
	const otherRepoID = "cc398fbc-1945-3717-05d8-eb78866b4e92"
	must := func(label, stmt string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, stmt, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	must("repo A", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos5405LiveRepoID, orgID, chaos5405LiveRepoSlug, "github", at)
	must("repo B", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		otherRepoID, orgID, "example-org/other-service", "github", at)
	// SAME work_item_id, DIFFERENT providers, DIFFERENT repositories. They can
	// only coexist because repo_id is part of the sort key -- under one repo
	// they would collapse under ReplacingMergeTree, which is itself why a bare
	// work_item_id is not an identity.
	must("jira work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		collidingID, chaos5405LiveRepoID, orgID, "jira", "the attributed one", "open", "", "", "", at)
	must("linear work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		collidingID, otherRepoID, orgID, "linear", "the one nobody attributed", "open", "", "", "", at)
	// The team is attributed to the JIRA one only.
	must("attribution", `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, chaos5405LiveRepoID, collidingID, chaos5405LiveTeamID, "native_team", uint8(1), "high", at)

	result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: orgID},
		RequirementKind: contextfabric.FactStatus,
		Origins:         []contextfabric.SubjectRef{{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + chaos5405LiveTeamID, Label: "Platform"}},
		Policy:          "team_primary_attribution_work_item_status_v1",
		TargetKind:      contextfabric.SubjectWorkItem,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Limit:           200,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	attributed, _, _ := identity.Derive(identity.KindWorkItem, []string{chaos5405LiveRepoID, collidingID}, nil)
	unattributed, _, _ := identity.Derive(identity.KindWorkItem, []string{otherRepoID, collidingID}, nil)
	admitted := map[string]bool{}
	for _, target := range result.Targets {
		admitted[target.CanonicalID] = true
	}
	if !admitted[attributed] {
		t.Errorf("the attributed work item was not admitted; targets=%v", admitted)
	}
	if admitted[unattributed] {
		t.Errorf("a DIFFERENT provider's work item sharing the work_item_id was admitted -- the join has been narrowed to work_item_id alone and the team arm now has the collision surface the project arm had")
	}
	if len(result.Targets) != 1 {
		t.Errorf("targets = %d, want exactly 1 -- the census must describe the attributed population only", len(result.Targets))
	}
}
