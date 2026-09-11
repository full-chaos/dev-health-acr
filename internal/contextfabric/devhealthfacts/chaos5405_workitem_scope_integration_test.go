package devhealthfacts_test

// CHAOS-5405 -- the work-item traversal against a REAL ClickHouse.
//
// WHY A CONTAINER ARM AND NOT ONLY THE FAKE CLIENT. The fake client returns
// whatever a test seeded, in whatever order, and ignores the statement
// entirely -- so it can prove the SCANNER contract and the authorization
// decisions in Go, and nothing at all about the SQL. Everything D-a actually
// pushes down is invisible to it:
//
//   - `count() OVER ()` -- the census aggregate. A fake client cannot tell a
//     window aggregate over the selection relation from a literal, which is
//     exactly the "count and page from two unrelated observations" the ruling
//     forbids.
//   - FINAL over a ReplacingMergeTree, so a re-synced work item is counted
//     once rather than once per version.
//   - the org-wide join-key ambiguity guard, which is a window function over
//     projects and must exclude an ambiguous project rather than guess.
//   - LIMIT 201 over that whole shape: whether the 201st row survives the
//     joins is a property of the plan, not of Go.
//
// This is also D-f acceptance gate 6's venue ("container-backed SQL proofs on
// bigboy through the oci-image recipe"), and gate 3's real-provider matrix.
//
// VENUE. bigboy, with the acr mirror prefixes exported -- the ops prefix
// makes every container test fail at image resolve:
//
//	export TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX=ghcr.io/full-chaos/dev-health-acr
//	export ACR_IMAGE_MIRROR_PREFIX=ghcr.io/full-chaos/dev-health-acr/
//
// RED-FIRST at 0945a53dfdad0e84ba8244c59e8e9d85a7d195f2: every work-item
// policy errors "does not implement policy" before the assertions are reached.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	chaos5405LiveProjectID   = "TITAN"
	chaos5405LiveProjectKey  = "TTN"
	chaos5405LiveTeamID      = "PLATFORM"
	chaos5405LiveRepoID      = "aa198fbc-1945-3717-05d8-eb78866b4e90"
	chaos5405LiveRepoSlug    = "acme/titan-api"
	chaos5405LiveOrphanRepo  = "bb298fbc-1945-3717-05d8-eb78866b4e91"
	chaos5405LiveZeroRepoID  = "00000000-0000-0000-0000-000000000000"
	chaos5405LiveDecoyProjID = "DECOY"

	// The providers each seeded project belongs to. Named rather than inlined
	// because the work items must be seeded with the SAME token as the
	// project they join to -- that agreement is the thing under test.
	chaos5405LiveProjectProvider = "linear"
	// THE DECOY SHARES THE MAIN PROJECT'S PROVIDER, deliberately. The
	// ambiguity guard resolves each (provider, join_key) independently, so a
	// decoy under a DIFFERENT provider would no longer collide, and this
	// fixture would stop exercising the guard at all -- the row would be
	// excluded by the provider condition instead, and the guard could be
	// deleted without a test noticing.
	chaos5405LiveDecoyProvider = chaos5405LiveProjectProvider
)

// seedChaos5405Fixture builds ONE project and ONE team whose work items span
// every population D-c distinguishes, plus a re-synced duplicate (FINAL) and
// an ambiguous project join key (the org-wide guard).
func seedChaos5405Fixture(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, orgID string, at time.Time, repoBackedCount int) {
	t.Helper()
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}

	// EVERY work_items row carries its provider, because production rows do:
	// schema.go's own subject projection branches on w.provider against real
	// tokens ('gitlab', 'github'), which it could not do against a column that
	// is empty in practice. The first version of this fixture left it unset,
	// and that omission is what let the project chain join a work item to a
	// project from a DIFFERENT provider without any test noticing (codex r2
	// F4). A fixture that omits a column the production relation always fills
	// is not a smaller fixture, it is a different relation.
	mustSeed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos5405LiveProjectID, orgID, "Titan", chaos5405LiveProjectKey, chaos5405LiveProjectProvider, "active", "", uint8(1), at)
	mustSeed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos5405LiveRepoID, orgID, chaos5405LiveRepoSlug, "github", at)

	// ONE STATEMENT PER TABLE, not one per row.
	//
	// Every row below has a distinct sorting key for its table, so a
	// multi-row INSERT writes exactly the rows a row-at-a-time seed did.
	// It is the same relation, and the assertions read the same counts.
	//
	// What this is NOT is a smaller fixture: at repoBackedCount=200 the
	// row-at-a-time form issued 410 statements, each of which writes its own
	// part, and the parts are what the server then carries and merges. The
	// re-synced duplicate below is deliberately left OUT of these batches,
	// and its comment says why.
	const workItemsInsert = `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at)`
	var workItemRows, attributionRows [][]any
	workItem := func(id, repoID string) {
		workItemRows = append(workItemRows, []any{id, repoID, orgID, chaos5405LiveProjectProvider, "issue " + id, "open", "", "", chaos5405LiveProjectID, at})
	}
	attribution := func(_, workItemID, repoID, source string) {
		attributionRows = append(attributionRows, []any{orgID, repoID, workItemID, chaos5405LiveTeamID, source, uint8(1), "high", at})
	}
	flush := func(label, statement string, rows [][]any) {
		t.Helper()
		if len(rows) == 0 {
			return
		}
		placeholders := make([]string, 0, len(rows))
		args := make([]any, 0, len(rows)*len(rows[0]))
		for _, row := range rows {
			marks := make([]string, len(row))
			for i := range row {
				marks[i] = "?"
			}
			placeholders = append(placeholders, "("+strings.Join(marks, ", ")+")")
			args = append(args, row...)
		}
		mustSeed(label, statement+" VALUES "+strings.Join(placeholders, ", "), args...)
	}

	for i := 0; i < repoBackedCount; i++ {
		id := "WI-" + strconv.Itoa(i)
		workItem(id, chaos5405LiveRepoID)
		attribution("attribution "+id, id, chaos5405LiveRepoID, "native_team")
	}
	// Repo-less BY DESIGN (a Linear issue): a first-class target here, and
	// visible ONLY to an organization-wide principal.
	workItem("linear:CHAOS-9001", chaos5405LiveZeroRepoID)
	attribution("repo-less attribution", "linear:CHAOS-9001", chaos5405LiveZeroRepoID, "issue_project")
	// A nonzero repo_id that resolves to no repos row -- an ORPHAN, which
	// must stay distinguishable from repo-less and must never manufacture a
	// repository.
	workItem("WI-ORPHAN", chaos5405LiveOrphanRepo)
	attribution("orphan attribution", "WI-ORPHAN", chaos5405LiveOrphanRepo, "repo_ownership")
	// Seeded with the DECOY's provider so the ambiguity guard, not the
	// provider condition, is what excludes it -- otherwise this row would
	// start being dropped for the wrong reason and the guard would stop being
	// tested at all.
	workItemRows = append(workItemRows, []any{"WI-AMBIGUOUS", chaos5405LiveRepoID, orgID, chaos5405LiveDecoyProvider, "issue WI-AMBIGUOUS", "open", "", "", chaos5405LiveProjectKey, at})

	// A RE-SYNCED duplicate of WI-0: work_items is a ReplacingMergeTree, so
	// without FINAL the census double-counts it.
	//
	// IT IS FLUSHED SEPARATELY, and it must stay that way. ClickHouse applies
	// the engine's merging algorithm to the rows of a single insert block
	// (optimize_on_insert, on by default), so a duplicate batched WITH its
	// original collapses as the part is written and never reaches the table
	// as two rows. The duplicate this fixture needs is an ACROSS-PARTS one:
	// that is the state FINAL exists to resolve, and the state the census
	// assertions here distinguish. A second statement is what makes a second
	// part, and TestChaos5405_TheSeedBatchesItsRowsButNotTheResyncDuplicate
	// executes that.
	var resyncRows [][]any
	if repoBackedCount > 0 {
		resyncRows = append(resyncRows, []any{"WI-0", chaos5405LiveRepoID, orgID, chaos5405LiveProjectProvider, "issue WI-0 (resynced)", "in_progress", "", "", chaos5405LiveProjectID, at.Add(time.Minute)})
	}

	flush("work items", workItemsInsert, workItemRows)
	flush("work item team attributions", `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at)`, attributionRows)
	flush("work item WI-0 resync", workItemsInsert, resyncRows)
	// A DIFFERENT project whose own id equals this project's project_key:
	// the join key is ambiguous org-wide, so neither claim may be guessed.
	mustSeed("decoy project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos5405LiveProjectKey, orgID, "Decoy", chaos5405LiveDecoyProjID, chaos5405LiveDecoyProvider, "active", "", uint8(1), at)
}

// TestChaos5405_ProjectWorkItemScopeAgainstRealClickHouse is the project arm.
func TestChaos5405_ProjectWorkItemScopeAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Second)
	seedChaos5405Fixture(t, ctx, direct, orgID, at, 3)

	expander := devhealthfacts.NewScopeExpander(query)
	projectCanonicalID, omitted, err := identity.Derive(identity.KindProject, []string{"linear", chaos5405LiveProjectID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project canonical id: err=%v omitted=%v", err, omitted)
	}
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectCanonicalID, Label: "Titan"}

	expand := func(t *testing.T, principal storage.Principal) contextfabric.FactScopeExpansionResult {
		t.Helper()
		result, expandErr := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
			Principal:       principal,
			RequirementKind: contextfabric.FactStatus,
			Origins:         []contextfabric.SubjectRef{origin},
			Policy:          "project_work_item_status_v1",
			TargetKind:      contextfabric.SubjectWorkItem,
			TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Limit:           200,
		})
		if expandErr != nil {
			t.Fatalf("ExpandFactScope: %v", expandErr)
		}
		return result
	}

	t.Run("organization_wide_sees_repo_backed_and_repo_less_but_never_the_orphan", func(t *testing.T) {
		result := expand(t, storage.Principal{OrgID: orgID})
		ids := map[string]bool{}
		for _, target := range result.Targets {
			if target.Kind != contextfabric.SubjectWorkItem {
				t.Fatalf("target kind = %q, want work_item", target.Kind)
			}
			ids[target.CanonicalID] = true
		}
		repoLess, _, _ := identity.Derive(identity.KindWorkItem, []string{chaos5405LiveZeroRepoID, "linear:CHAOS-9001"}, nil)
		if !ids[repoLess] {
			t.Fatalf("the repo-less work item was not admitted for an organization-wide principal: %v", ids)
		}
		// The ORPHAN is a target here, and that is the corrected reading: an
		// orphaned repo_id means the item named a repository that did not
		// resolve, not that the WORK ITEM is unreal. The repository policies
		// must refuse it because there is no repository ENTITY to admit; this
		// chain stops at the item. What must never happen is manufacturing a
		// repository for it -- which is why the count below is separate.
		orphan, _, _ := identity.Derive(identity.KindWorkItem, []string{chaos5405LiveOrphanRepo, "WI-ORPHAN"}, nil)
		if !ids[orphan] {
			t.Fatalf("the orphaned work item was dropped for an organization-wide principal: %v", ids)
		}
		if result.Counts.OrphanedRepositoryCount != 1 {
			t.Fatalf("OrphanedRepositoryCount = %d, want 1 -- an orphan is admitted AND counted, never silently merged with the repo-less population", result.Counts.OrphanedRepositoryCount)
		}
		// 3 repo-backed + 1 repo-less + 1 orphan. The re-synced WI-0
		// duplicate must collapse under FINAL, and the ambiguous-join-key
		// work item must be excluded, never guessed into this project.
		if len(result.Targets) != 5 {
			t.Fatalf("targets = %d, want 5 (3 repo-backed + 1 repo-less + 1 orphan; the resync collapses under FINAL, the ambiguous row is excluded): %v", len(result.Targets), ids)
		}
		if result.Counts.CandidateCount != 5 {
			t.Fatalf("CandidateCount = %d, want 5 -- the census is measured over the same relation the page came from", result.Counts.CandidateCount)
		}
		// THE REPO-LESS CANDIDATE POPULATION, ASSERTED AGAINST REAL SQL, and
		// asserted HERE specifically because this is the arm where the repo-less
		// item is AUTHORIZED.
		//
		// This is a battery survivor turned into a pin: replacing
		// `countIf(repo_less = 1)` with `countIf(repo_less = 1 AND authorized = 0)`
		// survived a whole battery. Every unit test of this count runs on the
		// fake client, which ignores the statement entirely, so no fake-backed
		// test can tell one window aggregate from another -- and the live tests
		// asserted the DENIED repo-less count without ever asserting the
		// candidate one. With the repo-less row authorized, the denied-subset
		// reading reports 0 and the correct reading reports 1, so this line is
		// the only place the two are distinguishable.
		if result.Counts.RepoLessCandidateCount != 1 {
			t.Fatalf("RepoLessCandidateCount = %d, want 1 -- the repo-less CANDIDATE population is pre-authorization and counts this admitted row; a count of 0 means the aggregate is reading the denied subset", result.Counts.RepoLessCandidateCount)
		}
		if result.Counts.RepoLessAuthorizationDroppedCount != 0 {
			t.Fatalf("RepoLessAuthorizationDroppedCount = %d, want 0 -- nothing is denied for an organization-wide principal, which is what makes the assertion above discriminating", result.Counts.RepoLessAuthorizationDroppedCount)
		}
		for id := range ids {
			if strings.Contains(id, "WI-AMBIGUOUS") {
				t.Fatalf("a work item whose project join key resolves ambiguously org-wide was admitted: %q", id)
			}
		}
	})

	t.Run("repository_restricted_never_reaches_the_repo_less_population", func(t *testing.T) {
		result := expand(t, storage.Principal{OrgID: orgID, RepositoryScopes: []string{chaos5405LiveRepoSlug}})
		for _, target := range result.Targets {
			if strings.Contains(target.CanonicalID, chaos5405LiveZeroRepoID) {
				t.Fatalf("a repository-restricted principal reached the repo-less population: %q", target.CanonicalID)
			}
		}
		if len(result.Targets) != 3 {
			t.Fatalf("targets = %d, want the 3 repo-backed items only", len(result.Targets))
		}
		// TWO denials, not one: a repository-restricted principal reaches
		// neither the repo-less item nor the orphaned one. Both are counted
		// from the census aggregates, which is the property the projection
		// mask exists to preserve -- a WHERE filter would have reported zero
		// here while denying exactly the same two rows.
		if result.Counts.AuthorizationDroppedCount != 2 {
			t.Fatalf("AuthorizationDroppedCount = %d, want 2 (the repo-less item and the orphan)", result.Counts.AuthorizationDroppedCount)
		}
		if result.Counts.RepoLessAuthorizationDroppedCount != 1 {
			t.Fatalf("RepoLessAuthorizationDroppedCount = %d, want 1 -- the repo-less denial is counted separately from the orphan's", result.Counts.RepoLessAuthorizationDroppedCount)
		}
		// The candidate population does not move with authorization: the same
		// one repo-less row exists in the relation whether or not this
		// principal may see it. Pinned in BOTH authorization states so the
		// pair cannot be satisfied by a count that happens to agree in one.
		if result.Counts.RepoLessCandidateCount != 1 {
			t.Fatalf("RepoLessCandidateCount = %d, want 1 -- the candidate population is pre-authorization and is the same relation the org-wide arm measured", result.Counts.RepoLessCandidateCount)
		}
	})
}

// TestChaos5405_TeamWorkItemScopeAgainstRealClickHouse is the team arm: the
// composite (org_id, repo_id, work_item_id) join and the per-target
// attribution basis, neither of which a fake client can execute.
func TestChaos5405_TeamWorkItemScopeAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Second)
	seedChaos5405Fixture(t, ctx, direct, orgID, at, 2)

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
	if len(result.Targets) != 4 {
		t.Fatalf("targets = %d, want 4 (2 native_team repo-backed + 1 issue_project repo-less + 1 repo_ownership orphan)", len(result.Targets))
	}
	nativeID, _, _ := identity.Derive(identity.KindWorkItem, []string{chaos5405LiveRepoID, "WI-0"}, nil)
	repoLessID, _, _ := identity.Derive(identity.KindWorkItem, []string{chaos5405LiveZeroRepoID, "linear:CHAOS-9001"}, nil)
	if got := result.TargetBasis[contextfabric.FactSubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: nativeID})]; got != contextfabric.FactScopeBasisDirect {
		t.Fatalf("native_team target basis = %q, want %q -- an asserted attribution is a direct edge (D-b)", got, contextfabric.FactScopeBasisDirect)
	}
	if got := result.TargetBasis[contextfabric.FactSubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: repoLessID})]; got != contextfabric.FactScopeBasisAttributedPrimaryTeam {
		t.Fatalf("issue_project target basis = %q, want %q -- a computed attribution is disclosed, never laundered", got, contextfabric.FactScopeBasisAttributedPrimaryTeam)
	}
	if got := result.TargetAttributionSource[contextfabric.FactSubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: repoLessID})]; got != "issue_project" {
		t.Fatalf("issue_project target attribution source = %q, want %q", got, "issue_project")
	}
}

// TestChaos5405_TheOverflowRowSurvivesTheRealPlan is the LIMIT 201 half
// against real SQL: whether a 201st row survives the joins, the FINAL and the
// window aggregate is a property of the query plan, not of Go.
func TestChaos5405_TheOverflowRowSurvivesTheRealPlan(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Second)
	// 200 repo-backed + the repo-less item = 201 authorized targets, so the
	// 201st exists ONLY if the statement actually reads Limit+1.
	seedChaos5405Fixture(t, ctx, direct, orgID, at, 200)

	projectCanonicalID, _, _ := identity.Derive(identity.KindProject, []string{"linear", chaos5405LiveProjectID}, nil)
	result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: orgID},
		RequirementKind: contextfabric.FactStatus,
		Origins:         []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: projectCanonicalID, Label: "Titan"}},
		Policy:          "project_work_item_status_v1",
		TargetKind:      contextfabric.SubjectWorkItem,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Limit:           200,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 201 {
		t.Fatalf("targets = %d, want 201 -- the expander reads Limit+1 and returns ALL of them so the resolver can enforce the cap from the overflow row", len(result.Targets))
	}
	if !result.Counts.Truncated {
		t.Fatalf("Truncated = false with a 201st authorized target present")
	}
}
