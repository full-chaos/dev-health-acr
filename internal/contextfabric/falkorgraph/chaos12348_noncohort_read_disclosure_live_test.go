package falkorgraph

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12348 over real FalkorDB, the twin of the fake graph tests in
// chaos12348_noncohort_read_disclosure_test.go.

// A repository's 30 pull requests are more edges than the live adapter's budget
// of 25, so a single-subject read of it is cut; 3 are not.
func TestLiveANonCohortReadDisclosesACutHopWalk(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-owned", Label: "acme/owned"}
	code := contractsv1.ContextFabricCoverageDetailCode(hopWalkTruncatedCode)
	for _, tc := range []struct {
		name         string
		pullRequests int
		cut          bool
	}{{"over the budget", 30, true}, {"under the budget", 3, false}} {
		t.Run(tc.name, func(t *testing.T) {
			orgID := "live-12348-hop-walk-" + time.Now().UTC().Format("20060102T150405.000000000")
			t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
			entities, edges := teamStatusFloorFixture(orgID, time.Now().UTC(), "", tc.pullRequests)
			for _, b := range []contextfabric.ProjectionBatch{entities, edges} {
				if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
					t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
				}
			}
			got := discoverUnderTeams(t, ctx, adapter, orgID, repo, nil, nil)
			if got.partial != tc.cut || slices.Contains(got.details, code) != tc.cut {
				t.Fatalf("partial=%v details=%v, want partial and %s exactly when the read was cut (%v)", got.partial, got.details, code, tc.cut)
			}
		})
	}
}

// The fixtures are seedTeamNarrowingFixture's (CHAOS-12327). Project P is owned
// by team-C, and its github and linear issues each link R's two pull requests;
// R's only owner, team-B, ended an hour ago in the anchorOwnerEnded shape.
func TestLiveATeamNarrowingDisclosesAPathEdgeDroppedOnMissingOwnership(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: teamDiscoverProject, Label: "proj-p"}
	undecided := contractsv1.ContextFabricCoverageDetailCode(undecidedPathsCode)

	// team-A owns nothing. P is decided out of its narrowing, and P stays as
	// the committed subject. Each of P's issues links a pull request of R, whose
	// ownership is missing, so the issues and those pull requests are undecided:
	// four distinct endpoints (two issues and two pull requests), and every edge
	// between them has undecided ends only.
	ended := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{anchorOwnerEnded: true})
	got := discoverUnderTeams(t, ctx, adapter, ended, project, nil, []string{"team-A"})
	if !got.partial || !slices.Contains(got.details, undecided) || got.undecidedPaths != 4 {
		t.Fatalf("ended owner: partial=%v details=%v count=%d, want partial and %s with count 4", got.partial, got.details, got.undecidedPaths, undecided)
	}
	if len(got.nodes[contextfabric.SubjectWorkItem]) != 0 || len(got.nodes[contextfabric.SubjectPullRequest]) != 0 {
		t.Fatalf("ended owner: served %v, want no undecided endpoint", got.nodes)
	}

	// The same narrowing with R's owner current: every endpoint is decided out
	// of scope. That is correct narrowing, not missing data.
	owned := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{})
	if got := discoverUnderTeams(t, ctx, adapter, owned, project, nil, []string{"team-A"}); got.partial || slices.Contains(got.details, undecided) {
		t.Fatalf("current owner: partial=%v details=%v, want a complete read", got.partial, got.details)
	}
}

// seedProjectOnlyTeamNarrowing's fixture (CHAOS-12396): a project-only caller's
// path edges under a narrowing to a team that owns nothing. The project has an
// owner and its work items have no link, so every drop is decided, and the read
// stays complete with no row.
func TestLiveAProjectOnlyTeamNarrowingOfDecidedPathsDisclosesNothing(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := seedProjectOnlyTeamNarrowing(t, ctx, adapter)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectOnlyNarrowingP, Label: "ProjectP"}
	principal := storage.Principal{OrgID: orgID, ProjectScopes: []string{projectOnlyNarrowingP, projectOnlyNarrowingQ}}
	request := projectOnlyNarrowingRequest(t, ctx, adapter, principal, []string{"team-a"})
	request.Request.Question = "what is going on with ProjectP?"
	request.Interpretation = contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{project.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
	request.Resolution = contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{project}, Candidates: []contextfabric.SubjectCandidate{{
		ReceiptID: "receipt_subject", Subject: project, State: contextfabric.ResolutionCommitted,
		MatchedTerms: []string{project.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
	}}}
	gc, err := adapter.DiscoverContext(ctx, principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	assertNoCutReadDisclosure(t, gc)
	if gc.Coverage.Partial {
		t.Fatalf("coverage = %+v, want not partial: every drop was decided", gc.Coverage)
	}
}
