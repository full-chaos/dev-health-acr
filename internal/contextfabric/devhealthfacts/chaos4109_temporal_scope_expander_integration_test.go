package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4109: the FactReadScopeResolver axis gate opens for
// TemporalValidTime/TemporalRange once the work_item -> project edge
// carries real validity intervals (devhealthsource/teams_projects_edges.go).
// This file proves the READ side end to end against a real ClickHouse:
// ScopeExpander.projectRepositoriesAsOf must attribute a work item's
// repository to WHICHEVER project it belonged to AT THE REQUESTED TIME,
// not merely its current project -- the exact "as-of project expansion"
// the ticket title names. A fake client cannot exercise this: the
// leadInFrame window function membershipTouchesAsOfSQL depends on has no
// meaningful canned-row equivalent (chaos4099_scope_expander_integration_test.go
// makes the identical argument for the current-axis join).

const chaos4109ScopeOrgID = "org-4109-scope-asof"
const chaos4109ScopeRepoID = "50000000-0000-4000-8000-000000000001"
const chaos4109ScopeRepoSlug = "acme/asof"
const chaos4109ScopeWorkItemID = "linear:CHAOS-9201"
const chaos4109ScopeProjectAID = "60000000-0000-4000-8000-00000000000a"
const chaos4109ScopeProjectBID = "60000000-0000-4000-8000-00000000000b"

// chaos4109ScopeSeed writes the SAME add(A) -> move(A,B) -> move(B,A)
// sequence the producer-side test (devhealthsource package) seeds, so the
// two halves of this ticket -- the edge the producer writes and the read
// this file proves -- are pinned against literally the same history.
func chaos4109ScopeSeed(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, t1, t2, t3 time.Time) {
	t.Helper()
	for _, statement := range devhealthschema.DDL("projects", "repos", "work_items", "project_membership_transitions") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustSeed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4109ScopeRepoID, chaos4109ScopeOrgID, chaos4109ScopeRepoSlug, "linear", t1)
	mustSeed("project A", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4109ScopeProjectAID, chaos4109ScopeOrgID, "Project A", "", "linear", "backlog", "", uint8(1), t1)
	mustSeed("project B", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4109ScopeProjectBID, chaos4109ScopeOrgID, "Project B", "", "linear", "backlog", "", uint8(1), t1)
	// project_id is seeded pointing at A -- a decoy, exactly like the
	// producer-side fixture: a subject WITH transition history must never
	// be resolved through this stale column, on any axis.
	mustSeed("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4109ScopeWorkItemID, chaos4109ScopeRepoID, chaos4109ScopeOrgID, "Temporal issue", "open", "", "", "linear", chaos4109ScopeProjectAID, t3)
	mustSeed("transition: add to A", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		chaos4109ScopeOrgID, chaos4109ScopeRepoID, chaos4109ScopeWorkItemID, chaos4109ScopeProjectAID, t1, t1, "ev-4109-scope-1")
	mustSeed("transition: move A->B", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		chaos4109ScopeOrgID, chaos4109ScopeRepoID, chaos4109ScopeWorkItemID, chaos4109ScopeProjectAID, chaos4109ScopeProjectBID, t2, t2, "ev-4109-scope-2")
	mustSeed("transition: move B->A", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		chaos4109ScopeOrgID, chaos4109ScopeRepoID, chaos4109ScopeWorkItemID, chaos4109ScopeProjectBID, chaos4109ScopeProjectAID, t3, t3, "ev-4109-scope-3")
}

func chaos4109ScopeProjectSubject(t *testing.T, id string) contextfabric.SubjectRef {
	t.Helper()
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{"linear", id}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project canonical id: err=%v omitted=%v", err, omitted)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID}
}

// TestScopeExpanderAsOf_AttributesTheRepositoryToWhicheverProjectHeldItAtEachTimestamp
// is CHAOS-4109's own acceptance case: "seed add->remove->re-add
// transitions and assert as-of attribution at three timestamps." Probes
// BOTH projects at each of the three timestamps (six calls total) so the
// assertion is exclusivity, not just presence -- a resolver that admitted
// the repository from EVERY project at EVERY time would pass a
// presence-only check but fail this one.
func TestScopeExpanderAsOf_AttributesTheRepositoryToWhicheverProjectHeldItAtEachTimestamp(t *testing.T) {
	ctx := context.Background()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	chaos4109ScopeSeed(t, ctx, direct, t1, t2, t3)

	expander := devhealthfacts.NewScopeExpander(query)
	wantRepo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + chaos4109ScopeRepoID, Label: chaos4109ScopeRepoSlug}

	expand := func(t *testing.T, projectID string, asOf time.Time) contextfabric.FactScopeExpansionResult {
		t.Helper()
		result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
			Principal:       storage.Principal{OrgID: chaos4109ScopeOrgID, RepositoryScopes: []string{"*"}},
			RequirementKind: contextfabric.FactMetrics,
			Origins:         []contextfabric.SubjectRef{chaos4109ScopeProjectSubject(t, projectID)},
			Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
			TargetKind:      contextfabric.SubjectRepository,
			TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
			Limit:           20,
		})
		if err != nil {
			t.Fatalf("ExpandFactScope(project=%s, as_of=%s): %v", projectID, asOf, err)
		}
		return result
	}

	cases := []struct {
		name          string
		asOf          time.Time
		memberProject string
		otherProject  string
	}{
		{"during the first A stretch", t1.Add(15 * 24 * time.Hour), chaos4109ScopeProjectAID, chaos4109ScopeProjectBID},
		{"during the B stretch", t2.Add(15 * 24 * time.Hour), chaos4109ScopeProjectBID, chaos4109ScopeProjectAID},
		{"during the second A stretch", t3.Add(15 * 24 * time.Hour), chaos4109ScopeProjectAID, chaos4109ScopeProjectBID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			member := expand(t, c.memberProject, c.asOf)
			if len(member.Targets) != 1 || member.Targets[0] != wantRepo {
				t.Fatalf("as-of %s, member project %s: targets = %+v, want exactly [%+v]", c.asOf, c.memberProject, member.Targets, wantRepo)
			}
			other := expand(t, c.otherProject, c.asOf)
			if len(other.Targets) != 0 {
				t.Fatalf("as-of %s, non-member project %s: targets = %+v, want none -- the repository must not attribute to a project the item did not belong to at this time", c.asOf, c.otherProject, other.Targets)
			}
		})
	}
}

// TestScopeExpanderRange_WindowSelectsByOverlapNotContainment is the
// window->interval selection-rule test team-lead asked for explicitly
// (2026-08-25 ruling): a TemporalRange query admits a repository whenever
// its requested [Start, End) window OVERLAPS a validity interval at all --
// `observed_at <= window_end AND (valid_to IS NULL OR valid_to >
// window_start)`, projectRepositoriesAsOf's own doc comment above and the
// identical predicate falkorgraph/temporal.go applies to graph reads -- NOT
// containment (the window need not sit entirely inside one continuous
// membership stretch). Reuses chaos4109ScopeSeed's add(A)->move(A,B)->
// move(B,A) history: first A interval [t1,t2), a REMOVE->ADD gap on
// project B [t2,t3), second A interval [t3,unbounded).
//
// Three branches, each its own red-first proof (a window/interval rule
// with only a positive case cannot tell overlap apart from "matches
// anything with history at all"):
//   - window starts inside the first A interval and ENDS inside the gap:
//     must still admit, via the first interval's partial overlap.
//   - window sits entirely inside the gap, touching neither A interval:
//     must NOT admit -- the negative case that rules out
//     "any history exists" as an accidental substitute for overlap.
//   - window starts inside the gap and ENDS inside the second, unbounded A
//     interval: must still admit, via the second interval's partial
//     overlap (valid_to IS NULL is not a barrier to a window that starts
//     before the interval opens).
func TestScopeExpanderRange_WindowSelectsByOverlapNotContainment(t *testing.T) {
	ctx := context.Background()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	chaos4109ScopeSeed(t, ctx, direct, t1, t2, t3)

	expander := devhealthfacts.NewScopeExpander(query)
	wantRepo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + chaos4109ScopeRepoID, Label: chaos4109ScopeRepoSlug}

	expandRange := func(t *testing.T, start, end time.Time) contextfabric.FactScopeExpansionResult {
		t.Helper()
		result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
			Principal:       storage.Principal{OrgID: chaos4109ScopeOrgID, RepositoryScopes: []string{"*"}},
			RequirementKind: contextfabric.FactMetrics,
			Origins:         []contextfabric.SubjectRef{chaos4109ScopeProjectSubject(t, chaos4109ScopeProjectAID)},
			Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
			TargetKind:      contextfabric.SubjectRepository,
			TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
			Limit:           20,
		})
		if err != nil {
			t.Fatalf("ExpandFactScope(start=%s, end=%s): %v", start, end, err)
		}
		return result
	}

	cases := []struct {
		name      string
		start     time.Time
		end       time.Time
		wantAdmit bool
	}{
		{
			name:      "window starts in the first A interval, ends in the gap",
			start:     t1.Add(14 * 24 * time.Hour),
			end:       t2.Add(14 * 24 * time.Hour),
			wantAdmit: true,
		},
		{
			name:      "window sits entirely inside the gap",
			start:     t2.Add(9 * 24 * time.Hour),
			end:       t2.Add(19 * 24 * time.Hour),
			wantAdmit: false,
		},
		{
			name:      "window starts in the gap, ends in the second A interval",
			start:     t2.Add(19 * 24 * time.Hour),
			end:       t3.Add(14 * 24 * time.Hour),
			wantAdmit: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := expandRange(t, c.start, c.end)
			admitted := len(result.Targets) == 1 && result.Targets[0] == wantRepo
			if admitted != c.wantAdmit {
				t.Fatalf("start=%s end=%s: targets = %+v, want admitted=%v", c.start, c.end, result.Targets, c.wantAdmit)
			}
		})
	}
}

// TestScopeExpanderAsOf_CurrentAxisStaysWorkItemColumnOnlyForNoHistorySubjects
// proves the fallback arm (UnboundedValidityCount's own trigger): a work
// item with NO transition history is admitted on a historical axis purely
// from its current-value column, unconditionally -- there is no genuine
// as-of precision for it to offer, so its repository is attributed at
// every requested time rather than at none.
func TestScopeExpanderAsOf_NoHistorySubjectFallsBackUnconditionally(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	longAgo := at.Add(-365 * 24 * time.Hour)
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "repos", "work_items", "project_membership_transitions") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4109ScopeProjectAID, chaos4109ScopeOrgID, "Project A", "", "linear", "backlog", "", uint8(1), at); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4109ScopeRepoID, chaos4109ScopeOrgID, chaos4109ScopeRepoSlug, "linear", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4109ScopeWorkItemID, chaos4109ScopeRepoID, chaos4109ScopeOrgID, "No-history issue", "open", "", "", "linear", chaos4109ScopeProjectAID, at); err != nil {
		t.Fatalf("seed work item: %v", err)
	}

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4109ScopeOrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4109ScopeProjectSubject(t, chaos4109ScopeProjectAID)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &longAgo},
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	want := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + chaos4109ScopeRepoID, Label: chaos4109ScopeRepoSlug}
	if len(result.Targets) != 1 || result.Targets[0] != want {
		t.Fatalf("targets = %+v, want exactly [%+v] (unconditional admission -- no history exists to bound it)", result.Targets, want)
	}
	if result.Counts.UnboundedValidityCount != 1 {
		t.Fatalf("UnboundedValidityCount = %d, want 1 -- the one admitted target came from the no-history fallback arm", result.Counts.UnboundedValidityCount)
	}
}

// TestScopeExpanderAsOf_TemporalDroppedCountReachesThePullRequestPolicy is
// codex xhigh review R1's MEDIUM finding on the CHAOS-4109 PR, fixed:
// ExpandFactScope's pull_request/review branches were discarding the
// intermediate repository hop's own TemporalDroppedCount/
// UnboundedValidityCount when merging repoCounts into prCounts/reviewCounts
// -- a historical pull_request request could answer correctly (the right
// repositories, the right pull requests) while its OWN telemetry falsely
// reported zero interval misses, even though the repository hop it was
// built from genuinely had one.
//
// Two repositories on project A: repo1 stays a member throughout the query
// window (admitted, carries a pull request); repo2 was a member of A only
// BEFORE the window and moved away, so its history is "ever matched" but
// not "window matched" -- exactly one interval-miss.
func TestScopeExpanderAsOf_TemporalDroppedCountReachesThePullRequestPolicy(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-4109-pr-temporal-dropped"
	const projectAID = "70000000-0000-4000-8000-00000000000a"
	const repo1ID = "80000000-0000-4000-8000-000000000001"
	const repo1Slug = "acme/pr-stays"
	const wi1ID = "linear:CHAOS-9301"
	const repo2ID = "80000000-0000-4000-8000-000000000002"
	const repo2Slug = "acme/pr-leaves"
	const projectBID = "70000000-0000-4000-8000-00000000000b"
	const wi2ID = "linear:CHAOS-9302"

	tEarly := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tLeave := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	tStay := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "repos", "work_items", "project_membership_transitions", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	mustExec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustExec("project A", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectAID, orgID, "Project A", "", "linear", "backlog", "", uint8(1), tEarly)
	mustExec("project B", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectBID, orgID, "Project B", "", "linear", "backlog", "", uint8(1), tEarly)
	mustExec("repo1 (stays)", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repo1ID, orgID, repo1Slug, "linear", tStay)
	mustExec("repo2 (leaves)", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repo2ID, orgID, repo2Slug, "linear", tEarly)
	mustExec("wi1 on repo1", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wi1ID, repo1ID, orgID, "Stays on A", "open", "", "", "linear", projectAID, tStay)
	mustExec("wi2 on repo2", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wi2ID, repo2ID, orgID, "Leaves A before the window", "open", "", "", "linear", projectAID, tLeave)
	mustExec("pull request on repo1", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		repo1ID, orgID, uint32(1), "Widget", "open", tStay, tStay, "feat/widget", "")
	// wi1: added to A at tStay, OPEN -- covers the query window (asOf).
	mustExec("transition wi1 add A", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		orgID, repo1ID, wi1ID, projectAID, tStay, tStay, "ev-pr-1")
	// wi2: added to A at tEarly, moved to B at tLeave -- CLOSED before the
	// query window, so repo2 is "ever matched" but not "window matched".
	mustExec("transition wi2 add A", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		orgID, repo2ID, wi2ID, projectAID, tEarly, tEarly, "ev-pr-2")
	mustExec("transition wi2 leave A", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		orgID, repo2ID, wi2ID, projectAID, projectBID, tLeave, tLeave, "ev-pr-3")

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactPullRequests,
		Origins:         []contextfabric.SubjectRef{chaos4109ScopeProjectSubject(t, projectAID)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemPullRequest,
		TargetKind:      contextfabric.SubjectPullRequest,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 1 {
		t.Fatalf("targets = %+v, want exactly the one pull request on repo1 (repo2 left project A before the window)", result.Targets)
	}
	if result.Counts.TemporalDroppedCount != 1 {
		t.Fatalf("TemporalDroppedCount = %d, want 1 -- repo2's history reaches project A but not during this window, and that signal must survive onto the pull_request policy's own counts, not just the intermediate repository hop's", result.Counts.TemporalDroppedCount)
	}
}

// TestScopeExpanderAsOf_ZeroUUIDHistoryNeverInflatesTemporalDropped is codex
// xhigh review R1's LOW finding on the CHAOS-4109 PR, fixed:
// historicalDropCount's own aggregate query, unlike the main answer query,
// did not exclude the zero-UUID repo-less sentinel (or an orphaned
// repo_id) from its "ever matched" side. A repo-less-by-design Linear work
// item's out-of-window history to the requested project is never a real
// repository target -- the main query's own window predicate already
// excludes it before the zero-UUID/orphan classification is ever reached
// (it never appears there at all, in or out of window) -- but without this
// fix, historicalDropCount's UNfiltered ever_matched side still counted
// it, reporting a spurious interval-miss (TemporalDroppedCount=1) for a
// repository that could never have been admitted at ANY requested time.
func TestScopeExpanderAsOf_ZeroUUIDHistoryNeverInflatesTemporalDropped(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-4109-zero-uuid-drop"
	const projectAID = "70000000-0000-4000-8000-00000000000e"
	const projectBID = "70000000-0000-4000-8000-00000000000f"
	const zeroUUIDWorkItemID = "linear:CHAOS-9303"
	const zeroRepositoryID = "00000000-0000-0000-0000-000000000000"

	tEarly := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tLeave := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "repos", "work_items", "project_membership_transitions") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	mustExec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustExec("project A", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectAID, orgID, "Project A", "", "linear", "backlog", "", uint8(1), tEarly)
	mustExec("project B", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectBID, orgID, "Project B", "", "linear", "backlog", "", uint8(1), tEarly)
	// A repo-less-by-design Linear work item: repo_id is the zero-UUID
	// sentinel, never a real repos row.
	mustExec("zero-uuid work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		zeroUUIDWorkItemID, zeroRepositoryID, orgID, "Repo-less issue", "open", "", "", "linear", projectAID, tLeave)
	mustExec("transition add A", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		orgID, zeroRepositoryID, zeroUUIDWorkItemID, projectAID, tEarly, tEarly, "ev-zu-1")
	mustExec("transition leave A", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		orgID, zeroRepositoryID, zeroUUIDWorkItemID, projectAID, projectBID, tLeave, tLeave, "ev-zu-2")

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4109ScopeProjectSubject(t, projectAID)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %+v, want none -- the only history is the zero-UUID sentinel, never a real repository", result.Targets)
	}
	if result.Counts.MissingNextHopCount != 0 {
		t.Fatalf("MissingNextHopCount = %d, want 0 -- the zero-UUID row never reaches the main query's output at all (excluded by the window predicate before any repo-validity classification), so it cannot be MissingNextHopCount here either", result.Counts.MissingNextHopCount)
	}
	if result.Counts.TemporalDroppedCount != 0 {
		t.Fatalf("TemporalDroppedCount = %d, want 0 -- the zero-UUID sentinel is never a real repository target, so it must not ALSO be counted as an interval-miss on top of MissingNextHopCount", result.Counts.TemporalDroppedCount)
	}
}

// TestScopeExpanderAsOf_DuplicateAddIsCountedAndKeepsAttributing is codex
// xhigh review R1's MEDIUM finding on the CHAOS-4109 PR (renamed and
// re-scoped per team-lead ruling 2026-08-25: a repeated ADD touch with no
// intervening REMOVE is NOT malformed -- #1896's discard-and-replay path
// and an ordinary re-sync of an unchanged board membership both
// legitimately produce it for a subject that never left). The original
// finding stands under the new name: this signal was previously invisible
// with NO telemetry counter at all -- unlike devhealthsource's own
// producer-side presenceTelemetryLedger, which counts it. The interval
// this touch belongs to must still resolve, attributed through the FIRST
// ADD, not the duplicate.
func TestScopeExpanderAsOf_DuplicateAddIsCountedAndKeepsAttributing(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-4109-duplicate-add-touch"
	const projectAID = "70000000-0000-4000-8000-000000000c"
	const repoID = "80000000-0000-4000-8000-000000000003"
	const repoSlug = "acme/duplicate-add"
	const workItemID = "linear:CHAOS-9304"

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "repos", "work_items", "project_membership_transitions") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	mustExec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustExec("project A", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectAID, orgID, "Project A", "", "linear", "backlog", "", uint8(1), t1)
	mustExec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, repoSlug, "linear", t1)
	mustExec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workItemID, repoID, orgID, "Duplicate-add history", "open", "", "", "linear", projectAID, t2)
	// TWO ADD touches of the same (work_item, project A) pair, no REMOVE in
	// between -- the continuation membershipTouchesAsOfSQL flags via
	// is_duplicate_add.
	mustExec("transition add A (1)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		orgID, repoID, workItemID, projectAID, t1, t1, "ev-dup-1")
	mustExec("transition add A (2, duplicate)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		orgID, repoID, workItemID, projectAID, t2, t2, "ev-dup-2")

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4109ScopeProjectSubject(t, projectAID)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	// The interval is admitted exactly once, attributed through the FIRST
	// ADD (ev-dup-1): the duplicate (ev-dup-2) collapses into it rather
	// than minting a second, independent match.
	want := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoID, Label: repoSlug}
	if len(result.Targets) != 1 || result.Targets[0] != want {
		t.Fatalf("targets = %+v, want exactly [%+v] (one match, attributed through the original interval)", result.Targets, want)
	}
	if result.Counts.DuplicateAddCount == 0 {
		t.Fatal("DuplicateAddCount = 0, want > 0 -- the duplicate ADD touch was collapsed with no telemetry signal that it happened")
	}
	if result.Counts.MalformedTouchCount != 0 {
		t.Fatalf("MalformedTouchCount = %d, want 0 -- a duplicate ADD is a legitimate continuation, not malformed", result.Counts.MalformedTouchCount)
	}
}

// TestScopeExpanderAsOf_DanglingRemoveIsCountedNotSilentlyDropped is the
// scope-expander sibling of devhealthsource's identically-named producer
// test: the ONE case still reserved as malformed (team-lead ruling
// 2026-08-25) is a REMOVE touch with no prior ADD to close. The work item
// never resolves to the removed-from project (nothing to fabricate a
// ValidFrom from), but the anomaly must still be visible in telemetry.
func TestScopeExpanderAsOf_DanglingRemoveIsCountedNotSilentlyDropped(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-4109-dangling-remove-touch"
	const projectAID = "70000000-0000-4000-8000-000000000d"
	const projectBID = "70000000-0000-4000-8000-000000000011"
	const workItemID = "linear:CHAOS-9305"

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "repos", "work_items", "project_membership_transitions") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	mustExec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustExec("project A", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectAID, orgID, "Project A", "", "linear", "backlog", "", uint8(1), t1)
	mustExec("project B", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectBID, orgID, "Project B", "", "linear", "backlog", "", uint8(1), t1)
	mustExec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workItemID, chaos4109ScopeRepoID, orgID, "Dangling-remove history", "open", "", "", "linear", projectBID, t1)
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4109ScopeRepoID, orgID, chaos4109ScopeRepoSlug, "linear", t1); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	// The ONLY tracked touch of projectAID for this subject is a REMOVE
	// (moving to projectBID) -- no ADD ever recorded.
	mustExec("transition: dangling remove", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		orgID, chaos4109ScopeRepoID, workItemID, projectAID, projectBID, t1, t1, "ev-dangle-1")

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4109ScopeProjectSubject(t, projectAID)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %+v, want none -- a dangling REMOVE must never fabricate a match to the project it removed from", result.Targets)
	}
	if result.Counts.MalformedTouchCount == 0 {
		t.Fatal("MalformedTouchCount = 0, want > 0 -- the dangling remove was excluded with no telemetry signal that anything was skipped")
	}
	if result.Counts.DuplicateAddCount != 0 {
		t.Fatalf("DuplicateAddCount = %d, want 0 -- a dangling remove must never also be counted as a duplicate add", result.Counts.DuplicateAddCount)
	}
}
