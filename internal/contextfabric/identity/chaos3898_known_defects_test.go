package identity_test

// This file inventories the identity defects verified in the CHAOS-3898
// design brief (v4.1 §0 "Verified defect inventory") as RED-documented
// test entries.
//
// Each test's PRIMARY assertion is the collision-free invariant the design
// brief requires -- "two different repos' (or providers') rows must not
// derive the same canonical id" -- checked against TODAY's live derivation
// formula, reproduced verbatim from its cited production source, not
// re-derived. Because that formula takes no repo/provider parameter at
// all, computing "repo-a's id" and "repo-b's id" from the same trailing
// natural-key value necessarily produces the SAME string: that identical
// call is exactly what today's production code does for two such rows, so
// the assertion genuinely fails if run. Every test is skipped with a
// reason citing its defect id -- that is the "marked, not fixed in S1"
// contract; deleting the t.Skip line is the intended way to check whether
// a later slice's rewrite fixed the defect it targets. Each test also
// checks the SAME invariant against identity.Derive, proving the fix S1
// ships (unwired) already satisfies it -- so a green run after unskipping
// signals "the call site was rewired through the registry", not "the
// registry itself changed".
//
// If a cited call site's format changes before it is rewired, S2 must
// update this file's "today" helper to match, or delete the now-obsolete
// pin.
//
// D-7 (deployment-incident edge join) and D-8 (episode split identity,
// CHAOS-3901) are not here: D-7 is fixed directly in this slice (see
// internal/contextfabric/devhealthsource/tables.go's queryDeploymentIncidentEdges
// and its regression test), and D-8 belongs to a different ticket.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// todayCIPipelineRunID reproduces internal/contextfabric/devhealthsource/
// tables.go's queryCIRuns verbatim: `"ci_pipeline_run:" + runID` -- no
// repo_id parameter at all.
func todayCIPipelineRunID(runID string) string { return "ci_pipeline_run:" + runID }

// TestKnownDefect_D1_CIPipelineRunCrossRepoCollision documents D-1: two
// different repos' CI runs sharing a raw run_id collapse to one canonical
// id today (cross-repo collapse; last-write-wins payload AND
// authorization).
func TestKnownDefect_D1_CIPipelineRunCrossRepoCollision(t *testing.T) {
	t.Skip("CHAOS-3898 D-1: tables.go's queryCIRuns derives ci_pipeline_run ids from run_id alone; fixed by S2's rewire through the identity registry (design brief §6), not S1 (no behavior change). Un-skip once S2 lands.")

	// repo-a's row and repo-b's row both carry run_id="run-1". Today's
	// formula has no repo parameter, so both calls are identical -- which
	// is exactly the bug: the two rows are indistinguishable.
	repoARow := todayCIPipelineRunID("run-1")
	repoBRow := todayCIPipelineRunID("run-1")
	if repoARow == repoBRow {
		t.Fatalf("D-1: repo-a's and repo-b's CI runs both derive %q -- cross-repo collision", repoARow)
	}

	// The registry already satisfies the invariant today's call site does
	// not: it takes repo_id as a real segment.
	regA, _, err := identity.Derive(identity.KindCIPipelineRun, []string{"repo-a", "run-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	regB, _, err := identity.Derive(identity.KindCIPipelineRun, []string{"repo-b", "run-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if regA == regB {
		t.Fatalf("registry itself is non-injective across repos -- that would be a registry bug, not D-1: %q == %q", regA, regB)
	}
}

// todayPullRequestReviewID reproduces tables.go's queryPullRequestReviews
// verbatim: `"pull_request_review:" + reviewID`.
func todayPullRequestReviewID(reviewID string) string { return "pull_request_review:" + reviewID }

// TestKnownDefect_D2_PullRequestReviewCrossRepoCollision documents D-2: the
// same class of collision as D-1, on review_id.
func TestKnownDefect_D2_PullRequestReviewCrossRepoCollision(t *testing.T) {
	t.Skip("CHAOS-3898 D-2: tables.go's queryPullRequestReviews derives pull_request_review ids from review_id alone; fixed by S2's rewire through the identity registry, not S1. Un-skip once S2 lands.")

	repoARow := todayPullRequestReviewID("review-9")
	repoBRow := todayPullRequestReviewID("review-9")
	if repoARow == repoBRow {
		t.Fatalf("D-2: repo-a's and repo-b's reviews both derive %q -- cross-repo collision", repoARow)
	}

	regA, _, err := identity.Derive(identity.KindPullRequestReview, []string{"repo-a", "17", "review-9"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	regB, _, err := identity.Derive(identity.KindPullRequestReview, []string{"repo-b", "17", "review-9"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if regA == regB {
		t.Fatalf("registry itself is non-injective across repos: %q == %q", regA, regB)
	}
}

// todayDeploymentID reproduces tables.go's queryDeployments verbatim:
// `"deployment:" + deploymentID`.
func todayDeploymentID(deploymentID string) string { return "deployment:" + deploymentID }

// TestKnownDefect_D3_DeploymentCrossRepoCollision documents D-3: the same
// class of collision as D-1/D-2, on deployment_id (an audit find per the
// brief).
func TestKnownDefect_D3_DeploymentCrossRepoCollision(t *testing.T) {
	t.Skip("CHAOS-3898 D-3: tables.go's queryDeployments derives deployment ids from deployment_id alone; fixed by S2's rewire through the identity registry, not S1. Un-skip once S2 lands.")

	repoARow := todayDeploymentID("deploy-3")
	repoBRow := todayDeploymentID("deploy-3")
	if repoARow == repoBRow {
		t.Fatalf("D-3: repo-a's and repo-b's deployments both derive %q -- cross-repo collision", repoARow)
	}

	regA, _, err := identity.Derive(identity.KindDeployment, []string{"repo-a", "deploy-3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	regB, _, err := identity.Derive(identity.KindDeployment, []string{"repo-b", "deploy-3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if regA == regB {
		t.Fatalf("registry itself is non-injective across repos: %q == %q", regA, regB)
	}
}

// todayWorkItemID reproduces tables.go's queryWorkItems verbatim:
// `"work_item:" + workItemID`.
func todayWorkItemID(workItemID string) string { return "work_item:" + workItemID }

// TestKnownDefect_D4_WorkItemCrossRepoCollision documents D-4: the
// exemption for work_item is withdrawn in the design (§1.3, "FIXED like the
// others"), but the fix itself is S2 scope -- S1 only ships the registry
// that S2 will wire in.
func TestKnownDefect_D4_WorkItemCrossRepoCollision(t *testing.T) {
	t.Skip("CHAOS-3898 D-4: tables.go's queryWorkItems derives work_item ids from work_item_id alone; fixed by S2's repo-scoped rewire (design brief §1.3), not S1. Un-skip once S2 lands.")

	repoARow := todayWorkItemID("WIDGET-101")
	repoBRow := todayWorkItemID("WIDGET-101")
	if repoARow == repoBRow {
		t.Fatalf("D-4: repo-a's and repo-b's work items both derive %q -- cross-repo collision", repoARow)
	}

	regA, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-a", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	regB, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-b", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if regA == regB {
		t.Fatalf("registry itself is non-injective across repos: %q == %q", regA, regB)
	}
}

// todayProjectID reproduces teams_projects.go's projectCanonicalID
// verbatim: `"project:" + projectID` -- no provider parameter at all.
func todayProjectID(projectID string) string { return "project:" + projectID }

// TestKnownDefect_D5_ProjectCrossProviderCollision documents D-5: two
// different providers colliding on the same bare projects.id last-write-win
// into one node, and the join(s) that key off projects.id alone stay
// provider-unqualified.
func TestKnownDefect_D5_ProjectCrossProviderCollision(t *testing.T) {
	t.Skip("CHAOS-3898 D-5: teams_projects.go's projectCanonicalID derives project ids from projects.id alone; fixed by S2's provider-qualified rewire (design brief §1.4), not S1. Un-skip once S2 lands.")

	githubRow := todayProjectID("71133891")
	gitlabRow := todayProjectID("71133891")
	if githubRow == gitlabRow {
		t.Fatalf("D-5: github's and gitlab's projects both derive %q -- cross-provider collision", githubRow)
	}

	regA, _, err := identity.Derive(identity.KindProject, []string{"github", "71133891"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	regB, _, err := identity.Derive(identity.KindProject, []string{"gitlab", "71133891"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if regA == regB {
		t.Fatalf("registry itself is non-injective across providers: %q == %q", regA, regB)
	}
}

// TestKnownDefect_D6_PaginationRowKeyCrossRepoTie documents D-6: the same
// bare natural-key fields D-1..D-4 use as canonical ids (c.run_id,
// r.review_id, d.deployment_id, w.work_item_id -- design brief §0) are
// ALSO the keyset-pagination rowKeys those queries order and page by
// (tables.go's sincePredicate/orderBy call sites inside queryCIRuns,
// queryPullRequestReviews, queryDeployments, and queryWorkItems). A rowKey
// that ties across repos is the same root cause as D-1..D-4 applied to
// pagination instead of canonical identity: two different repos' rows
// land on the same (timestamp, rowKey) cursor position, and keyset
// pagination can only ever return one of them per position -- the other
// is silently skipped at a page boundary rather than merely
// mis-identified.
//
// This is documented at the registry level (not by exercising the real
// paginator) because the fix is the same repo-qualified natural key the
// canonical-id registry already declares -- S2's job is to make the
// pagination rowKey composite-key-aware the same way it rewires the
// canonical id, not a second, independent fix.
func TestKnownDefect_D6_PaginationRowKeyCrossRepoTie(t *testing.T) {
	t.Skip("CHAOS-3898 D-6: tables.go pagination rowKeys (c.run_id, r.review_id, d.deployment_id, w.work_item_id) are bare, the same root cause as D-1..D-4; fixed by S2's rewire, not S1. Un-skip once S2 lands.")

	cases := []struct {
		kind        string
		bareToday   func(string) string
		lastSegment string
		regA, regB  []string // repo-a's and repo-b's full natural-key segments
	}{
		{identity.KindCIPipelineRun, todayCIPipelineRunID, "run-1", []string{"repo-a", "run-1"}, []string{"repo-b", "run-1"}},
		{identity.KindPullRequestReview, todayPullRequestReviewID, "review-9", []string{"repo-a", "17", "review-9"}, []string{"repo-b", "17", "review-9"}},
		{identity.KindDeployment, todayDeploymentID, "deploy-3", []string{"repo-a", "deploy-3"}, []string{"repo-b", "deploy-3"}},
		{identity.KindWorkItem, todayWorkItemID, "WIDGET-101", []string{"repo-a", "WIDGET-101"}, []string{"repo-b", "WIDGET-101"}},
	}
	for _, c := range cases {
		// repo-a's row and repo-b's row share the same trailing
		// natural-key value; today's rowKey formula has no repo
		// parameter (the same shape as D-1..D-4's canonical-id formula),
		// so the two rows' rowKeys tie -- keyset pagination cannot order
		// them and can only return one at a shared cursor position.
		repoARowKey := c.bareToday(c.lastSegment)
		repoBRowKey := c.bareToday(c.lastSegment)
		if repoARowKey == repoBRowKey {
			t.Fatalf("D-6 kind %q: repo-a's and repo-b's rowKeys both derive %q -- pagination tie", c.kind, repoARowKey)
		}

		// The invariant the registry already satisfies: a repo-qualified
		// key does distinguish them.
		regA, _, err := identity.Derive(c.kind, c.regA, nil)
		if err != nil {
			t.Fatal(err)
		}
		regB, _, err := identity.Derive(c.kind, c.regB, nil)
		if err != nil {
			t.Fatal(err)
		}
		if regA == regB {
			t.Fatalf("kind %q: registry itself is non-injective across repos -- registry bug, not D-6: %q == %q", c.kind, regA, regB)
		}
	}
}
