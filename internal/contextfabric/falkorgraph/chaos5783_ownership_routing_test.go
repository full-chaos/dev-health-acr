package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5783: "how many teams own repository R" is an OWNERSHIP question,
// answered from authorization_repositories -- a declared, already
// multi-owner-safe PROPERTY (teams_projects.go's ownedRepositoriesJoinSQL) --
// never from hopWalk's bounded graph-proximity traversal, which answers a
// different question (which subjects happen to be within two hops of the
// repository's own activity) that can under- or over-count real owners.
//
// repositoryAnchorFrame is a children_of_scope frame anchored on a
// repository, declaring team as the member kind -- the one pairing
// repository-anchored team cohorts route through ownership.
func repositoryAnchorFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"dev-health-acr"}, MemberKind: contextfabric.SubjectTeam,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
}

// ownershipRoutingRequest builds a request whose anchor is COMMITTED AND
// BOUND: the resolution also carries a candidate recording that anchor
// matched the frame's own anchor terms (frameAnchorBound's only recognized
// arm), exactly as a real subject resolution would for a scoped anchor that
// actually resolved.
func ownershipRoutingRequest(frame *contextfabric.QuestionFrame, anchor contextfabric.SubjectRef) contextfabric.GraphDiscoveryRequest {
	var candidates []contextfabric.SubjectCandidate
	// bases: a term match alone never binds (contextfabric.AnchorBound
	// requires an identity-proven basis on that branch) -- a real resolution
	// that bound this anchor via a scoped term always carries one, so a
	// fixture claiming to model "actually resolved" must too.
	bases := contextfabric.CommitBasisSet{}
	if frame != nil && frame.SubjectExpression.Scoped != nil {
		candidates = []contextfabric.SubjectCandidate{{
			ReceiptID: "receipt_anchor", Subject: anchor, State: contextfabric.ResolutionCommitted,
			MatchedTerms: frame.SubjectExpression.Scoped.AnchorTerms, MatchReasons: []string{"matched"},
			Confidence: 1, EvidenceRefIDs: []string{},
		}}
		bases.Record(anchor, contextfabric.CommitBasisAuthoritativeIdentity)
	}
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "repository team ownership cohort fixture",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape:       contextfabric.ShapeDiscoveredCohort,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: candidates},
		Frame:      frame,
		Bases:      bases,
	}
}

// TestDiscoverContextRoutesRepositoryAnchoredTeamCohortThroughOwnership is the
// red-then-green pin: two teams whose OWN authorization_repositories names the
// anchor repository both count, a team hopWalk would have reached through
// incidental adjacency but that does not own the repository does not, and a
// team with no ownership signal at all (the fail-closed sentinel value
// queryTeams stamps for a team with no current ownership row) does not
// either. hopWalk itself is never called for the routed anchor --
// the fake fails the test outright if it is.
func TestDiscoverContextRoutesRepositoryAnchoredTeamCohortThroughOwnership(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			inferredOwner := fakeSubjectNodeRow("team", "team:CHAOS", "Fullchaos")
			inferredOwner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr", "full-chaos/dev-health-ops"}
			providerOwner := fakeSubjectNodeRow("team", "team:gh:ops-team", "Ops Team")
			providerOwner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr"}
			proximateNonOwner := fakeSubjectNodeRow("team", "team:adjacent", "Adjacent")
			proximateNonOwner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/some-other-repo"}
			nullCarrying := fakeSubjectNodeRow("team", "team:null", "No Ownership Signal")
			nullCarrying["n"].(*node).Properties[propAuthzRepos] = []string{"acr-context-fabric:no-team-repository-ownership"}
			return []row{inferredOwner, providerOwner, proximateNonOwner, nullCarrying}, nil
		default:
			t.Fatalf("hopWalk must not run for a repository anchor whose member kind is team -- ownership routing serves it instead; got cypher: %s", cypher)
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(repositoryAnchorFrame(), anchor)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, want the two owning teams")
	}
	if got := len(result.Cohort.Members); got != 2 {
		t.Fatalf("Cohort.Members = %d, want exactly 2 (both real owners): %+v", got, result.Cohort.Members)
	}
	gotIDs := map[string]bool{}
	for _, m := range result.Cohort.Members {
		gotIDs[m.Subject.CanonicalID] = true
	}
	if !gotIDs["team:CHAOS"] || !gotIDs["team:gh:ops-team"] {
		t.Fatalf("Cohort.Members = %+v, want team:CHAOS and team:gh:ops-team, no others", result.Cohort.Members)
	}
	if gotIDs["team:adjacent"] {
		t.Fatal("Cohort.Members includes team:adjacent, a proximate but non-owning team -- ownership routing must exclude it")
	}
	if gotIDs["team:null"] {
		t.Fatal("Cohort.Members includes team:null, a team with no ownership signal (R179: null-carrying teams are not owners)")
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceOwnership {
		t.Fatalf("CohortMemberSource = %q, want %q", result.CohortMemberSource, contextfabric.CohortMemberSourceOwnership)
	}
}

// TestDiscoverContextRepositoryAnchorWithNoSlugFallsBackToHopWalk is the
// boundary case the routing gate itself must refuse rather than guess: a
// committed repository anchor whose Label is empty carries no slug to narrow
// an ownership census by, so hopWalk still runs for it -- the same as a
// committed subject of any other kind.
func TestDiscoverContextRepositoryAnchorWithNoSlugFallsBackToHopWalk(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: ""}
	hopWalked := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("the ownership census must not run for a repository anchor with no slug to narrow by")
			return nil, nil
		default:
			hopWalked = true
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(repositoryAnchorFrame(), anchor)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !hopWalked {
		t.Fatal("hopWalk's own query was never reached")
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceHopWalk {
		t.Fatalf("CohortMemberSource = %q, want %q", result.CohortMemberSource, contextfabric.CohortMemberSourceHopWalk)
	}
}

// TestDiscoverContextTruncatedOwnershipCensusMakesTheCohortIncomplete proves
// the ownership census's own truncation reaches Cohort.Complete exactly like
// the subjectless kind-scoped census's truncation already does: an ownership
// census cut before it finished enumerating every team must never let the
// served cohort claim completeness over a population it did not finish
// reading.
func TestDiscoverContextTruncatedOwnershipCensusMakesTheCohortIncomplete(t *testing.T) {
	overLimitRows := make([]row, exactNameCandidateQueryLimit+1)
	for i := range overLimitRows {
		id := fmt.Sprintf("team:over_%d", i)
		overLimitRows[i] = fakeSubjectNodeRow("team", id, id)
		overLimitRows[i]["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr"}
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			return overLimitRows, nil
		default:
			t.Fatal("hopWalk must not run for the routed anchor")
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(repositoryAnchorFrame(), anchor)
	request.Request.Options.MaxCohortMembers = exactNameCandidateQueryLimit + 100

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, want members from the (truncated) ownership census")
	}
	if result.Cohort.Complete {
		t.Fatal("Cohort.Complete = true, want false -- the ownership census that built this cohort was truncated")
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want true -- a truncated ownership census is degradation, not an ordinary empty result")
	}
}

// TestDiscoverContextCompleteOwnershipCensusCoversAnUnrelatedTruncatedArm
// proves censusCoversThisCohort's ownership disjunct is load-bearing on its
// own: a call whose full-text arm is truncated (an arm this cohort's kind
// never draws members from) still reports Cohort.Complete when the
// ownership census -- the actual source of this cohort's team members --
// ran to completion and found some. Without that disjunct, an anchor whose
// own team population is exhaustively known would incorrectly inherit an
// unrelated arm's truncation.
func TestDiscoverContextCompleteOwnershipCensusCoversAnUnrelatedTruncatedArm(t *testing.T) {
	overFulltextRows := make([]row, 26) // adapter's MaxResults (25) + 1
	for i := range overFulltextRows {
		id := fmt.Sprintf("project:noise_%d", i)
		overFulltextRows[i] = row{"node": &node{Properties: map[string]interface{}{
			propKind: "project", propCanonicalID: id, propLabel: id,
		}}, "score": 1.0}
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return overFulltextRows, nil
		case strings.Contains(cypher, "$kinds"):
			owner := fakeSubjectNodeRow("team", "team:CHAOS", "Fullchaos")
			owner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr"}
			return []row{owner}, nil
		default:
			// The full-text arm's own adjacent-edge gathering (one call per
			// matched node) shares this fallback; hopWalk itself is proven
			// skipped by the dedicated tests above and is not re-asserted here.
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(repositoryAnchorFrame(), anchor)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("Cohort = %#v, want the one real owner", result.Cohort)
	}
	if !result.Cohort.Complete {
		t.Fatal("Cohort.Complete = false, want true -- the ownership census that produced this cohort's members ran to completion; the full-text arm's own truncation draws no team members and must not degrade this cohort")
	}
	if result.Coverage.Partial {
		t.Fatal("Coverage.Partial = true, want false -- a completed, non-empty ownership census covers the unrelated truncated arm")
	}
}

// TestDiscoverContextOwnershipRoutingRequiresTheBoundAnchorNotAnyRepository
// pins the invariant that a committed repository that is NOT the frame's own
// bound anchor (a project-anchored frame carries no matching candidate term
// for it) must never trigger ownership routing, even though a repository
// subject and a team member kind are both present. hopWalk runs for BOTH
// committed subjects, unaffected; the ownership census ($kinds) must never be
// called for this pairing.
func TestDiscoverContextOwnershipRoutingRequiresTheBoundAnchorNotAnyRepository(t *testing.T) {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:launchpad", Label: "Launchpad"}
	strayRepo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:other", Label: "full-chaos/other-repo"}
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"launchpad"}, MemberKind: contextfabric.SubjectTeam,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	hopWalked := map[string]bool{}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("the ownership census must not run for a stray committed repository that is not the frame's own bound anchor")
			return nil, nil
		default:
			hopWalked["any"] = true
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "repository team ownership cohort fixture",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{
			Committed: []contextfabric.SubjectRef{project, strayRepo},
			// The project matched the frame's own anchor term; strayRepo has
			// NO candidate at all, so it can never satisfy frameAnchorBound.
			Candidates: []contextfabric.SubjectCandidate{{
				ReceiptID: "receipt_anchor", Subject: project, State: contextfabric.ResolutionCommitted,
				MatchedTerms: []string{"launchpad"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
			}},
		},
		Frame: frame,
	}

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !hopWalked["any"] {
		t.Fatal("hopWalk was never reached for either committed subject")
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceHopWalk {
		t.Fatalf("CohortMemberSource = %q, want %q -- ownership must not have routed", result.CohortMemberSource, contextfabric.CohortMemberSourceHopWalk)
	}
}

// TestDiscoverContextOwnershipRoutingNeverWidensAnExplicitRepositoryScope
// pins the invariant that a caller whose own RequestedScope already
// restricts to a DIFFERENT repository must not have that restriction
// overwritten by the anchor's own slug. Membership still comes from the
// anchor's ownership signal, but the caller's own scope restriction applies
// on top via the unmodified AuthorizedAttributes check -- an owning team the
// caller's own scope excludes stays excluded.
func TestDiscoverContextOwnershipRoutingNeverWidensAnExplicitRepositoryScope(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			owner := fakeSubjectNodeRow("team", "team:CHAOS", "Fullchaos")
			owner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr"}
			return []row{owner}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(repositoryAnchorFrame(), anchor)
	// The caller's own request restricts results to a DIFFERENT repository
	// than the one this question is about.
	request.Request.RequestedScope.RepositorySlugs = []string{"full-chaos/unrelated-repo"}

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil {
		t.Fatalf("Cohort = %#v, want nil -- the caller's own repository scope excludes the anchor entirely", result.Cohort)
	}
}

// TestDiscoverContextStillHopWalksForANonRepositoryAnchor is the control:
// team members under a PROJECT anchor are unaffected by the ownership
// routing above and still route through hopWalk, never the ownership census.
func TestDiscoverContextStillHopWalksForANonRepositoryAnchor(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:launchpad", Label: "Launchpad"}
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"launchpad"}, MemberKind: contextfabric.SubjectTeam,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("the ownership census must not run for a non-repository anchor")
			return nil, nil
		default:
			// hopWalk's own edgesOfNode call -- no edges out of the origin,
			// which is a legitimate hopWalk outcome (an isolated node) and
			// proves the arm ran without needing an edge-shaped fake row.
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(frame, anchor)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceHopWalk {
		t.Fatalf("CohortMemberSource = %q, want %q", result.CohortMemberSource, contextfabric.CohortMemberSourceHopWalk)
	}
}

// TestDiscoverContextOwnershipRoutingExcludesATeamReachedOnlyThroughAnotherCommittedSubject
// pins the invariant that once a call is ownership-routed for the team
// member kind, that kind's member pool is the ownership census ONLY: a
// SECOND committed subject (a comparison operand, a carried hint -- anything
// other than the bound anchor itself) still hop-walks as before -- proven
// here by the walk actually firing -- but a team it reaches through
// incidental graph proximity must never re-enter the pool through that
// OTHER subject's walk. Ownership routing already skips the anchor's own
// walk entirely; this is the same exclusion applied to every other
// committed subject's walk, not just the anchor's.
func TestDiscoverContextOwnershipRoutingExcludesATeamReachedOnlyThroughAnotherCommittedSubject(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	stray := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:other", Label: "full-chaos/other-repo"}
	strayWalked := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			owner := fakeSubjectNodeRow("team", "team:CHAOS", "Fullchaos")
			owner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr"}
			return []row{owner}, nil
		case strings.Contains(cypher, "UNION"):
			// edgesOfNode: only the STRAY subject's walk should ever reach
			// here -- the anchor's own walk is skipped entirely.
			if params["id"] != stray.CanonicalID {
				return nil, nil
			}
			strayWalked = true
			return []row{{
				"r": &edge{Properties: map[string]interface{}{
					propRelationType: "BLOCKS", propRelationshipID: "rel_stray_adjacent",
					propEvidenceRefs: []string{"evidence_stray"},
				}},
				"srcKind": "repository", "srcId": stray.CanonicalID, "dstKind": "team", "dstId": "team:adjacent-via-stray",
			}}, nil
		default: // nodeByKindID: resolveEdge's endpoint fetches
			switch params["kind"] {
			case "repository":
				if params["id"] == stray.CanonicalID {
					return []row{fakeSubjectNodeRow("repository", stray.CanonicalID, stray.Label)}, nil
				}
			case "team":
				if params["id"] == "team:adjacent-via-stray" {
					return []row{fakeSubjectNodeRow("team", "team:adjacent-via-stray", "Adjacent Via Stray")}, nil
				}
			}
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := ownershipRoutingRequest(repositoryAnchorFrame(), anchor)
	request.Resolution.Committed = append(request.Resolution.Committed, stray)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !strayWalked {
		t.Fatal("the stray committed subject's own hop walk never ran -- ownership routing must not blanket-suppress hop walk for OTHER committed subjects")
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, want the one real owner")
	}
	if got := len(result.Cohort.Members); got != 1 {
		t.Fatalf("Cohort.Members = %d, want exactly 1 (the real owner, never the stray's proximate team): %+v", got, result.Cohort.Members)
	}
	if result.Cohort.Members[0].Subject.CanonicalID != "team:CHAOS" {
		t.Fatalf("Cohort.Members = %+v, want only team:CHAOS", result.Cohort.Members)
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceOwnership {
		t.Fatalf("CohortMemberSource = %q, want %q", result.CohortMemberSource, contextfabric.CohortMemberSourceOwnership)
	}
}

// TestDiscoverContextOwnershipRoutingRecognizesACanonicalIDCommit pins the
// invariant that a committed anchor bound the SAME way the count decision's
// own anchorBound recognizes it -- via CommitBasisSet, never only via a
// matched term -- routes through ownership. Before this pin, a caller who
// named the repository by its own canonical id (never producing a matched
// term) would certify anchor_committed on the served line while this
// package silently served a hop-walk-computed member set: a proximity count
// presented as the exact declared population.
func TestDiscoverContextOwnershipRoutingRecognizesACanonicalIDCommit(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			owner := fakeSubjectNodeRow("team", "team:CHAOS", "Fullchaos")
			owner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr"}
			return []row{owner}, nil
		default:
			t.Fatalf("hopWalk must not run for a canonical-id-committed repository anchor -- ownership routing serves it exactly as it would a term-matched one; got cypher: %s", cypher)
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	// A frame carrying anchor terms that DO NOT match anything -- the only
	// route to anchor-bound here is the commit basis, never a matched term.
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"unrelated-term"}, MemberKind: contextfabric.SubjectTeam,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	request := ownershipRoutingRequest(frame, anchor)
	request.Resolution.Candidates = nil
	request.Bases = contextfabric.CommitBasisSet{contextfabric.SubjectMapKey(anchor): contextfabric.CommitBasisCallerCanonicalID}

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceOwnership {
		t.Fatalf("CohortMemberSource = %q, want %q", result.CohortMemberSource, contextfabric.CohortMemberSourceOwnership)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 || result.Cohort.Members[0].Subject.CanonicalID != "team:CHAOS" {
		t.Fatalf("Cohort = %+v, want exactly team:CHAOS", result.Cohort)
	}
}

// TestDiscoverContextOwnershipRoutingUnboundCommitStillHopWalks is the
// control for the canonical-id pin above: a repository committed on NEITHER
// a matched term NOR a recorded commit basis is not anchor-bound by any
// construction and still falls back to hopWalk, exactly like the existing
// stray-repository control.
func TestDiscoverContextOwnershipRoutingUnboundCommitStillHopWalks(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	hopWalked := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("the ownership census must not run for a repository commit that is bound by neither a matched term nor a commit basis")
			return nil, nil
		default:
			hopWalked = true
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"unrelated-term"}, MemberKind: contextfabric.SubjectTeam,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	request := ownershipRoutingRequest(frame, anchor)
	request.Resolution.Candidates = nil

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !hopWalked {
		t.Fatal("hopWalk's own query was never reached")
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceHopWalk {
		t.Fatalf("CohortMemberSource = %q, want %q", result.CohortMemberSource, contextfabric.CohortMemberSourceHopWalk)
	}
}
