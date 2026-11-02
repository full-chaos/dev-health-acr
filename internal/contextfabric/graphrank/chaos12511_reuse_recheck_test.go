package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestAnAdmittedPathCarriesTheNodesTheReuseCapCounts (CHAOS-12511): the answer
// reuse recheck's cap counts contextfabric.AdmittedPathNodes per stored path,
// well under the contract's ceiling of 51. AdmitEdges is the one path builder.
// If it starts to mint longer paths, this fails and the cap must move with it.
func TestAnAdmittedPathCarriesTheNodesTheReuseCapCounts(t *testing.T) {
	t.Parallel()
	edge := ResolvedEdge{
		UUID: "edge-1", Name: string(contractsv1.ContextFabricRelationshipOwnedByTeam), Fact: "Owned by a team.",
		From:       contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_1", Label: "One"},
		To:         contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1", Label: "Team"},
		Attributes: map[string]interface{}{"epistemic_status": "observed", "evidence_refs": []string{"acr:v1:evidence:1"}},
	}
	options := contextfabric.InvestigationOptions{MaxSubjectCandidates: 50, MaxCohortMembers: 250, MaxRelationshipPaths: 250, MaxDrivers: 50, MaxEvidenceRefs: 500, MaxSerializedBytes: 1 << 20}
	admitted := AdmitEdges("org_1", []ResolvedEdge{edge}, options, func(contextfabric.SubjectRef) bool { return false })
	if len(admitted.Paths) != 1 {
		t.Fatalf("paths = %d, want 1", len(admitted.Paths))
	}
	if got := len(admitted.Paths[0].Nodes); got != contextfabric.AdmittedPathNodes {
		t.Fatalf("an admitted path carries %d nodes, the reuse cap counts %d", got, contextfabric.AdmittedPathNodes)
	}
}

// TestTheReuseRecheckHintCommitsNoMoreThanItsCandidateCap (CHAOS-12511) is the
// grounding for the engine double that models the recheck (recheckGraph), on the
// real resolver and under the recheck's own hint source: one call commits at most
// its candidate cap and drops the rest as it drops an unauthorized subject. That
// is why the recheck batches.
func TestTheReuseRecheckHintCommitsNoMoreThanItsCandidateCap(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ hints, wantCommitted int }{{hints: 50, wantCommitted: 50}, {hints: 51, wantCommitted: 50}, {hints: 54, wantCommitted: 50}} {
		req, backend := groupAuthorizationHintRequest(row.hints, contractsv1.ContextFabricMaxSubjectCandidatesLimit)
		for index := range req.RequestedScope.SubjectHints {
			req.RequestedScope.SubjectHints[index].Source = string(hintsource.AnswerReuseAuthorizationRecheck)
		}
		resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("teams"), backend.deps(), nil, nil, nil, "")
		if err != nil {
			t.Fatalf("hints=%d: %v", row.hints, err)
		}
		if len(resolution.Committed) != row.wantCommitted {
			t.Fatalf("hints=%d committed = %d, want %d", row.hints, len(resolution.Committed), row.wantCommitted)
		}
	}
}

// TestARecheckHintCanCommitASubjectItWasNotAsked (CHAOS-12511) is the grounding
// for reuseRecheckBatch.answeredBy, on the real resolver with no frame, as the
// recheck calls it: a repository hint beside a pull request the question names
// commits that pull request too. The recheck never asked about it, so a commit
// cannot be read as an answer to the question the recheck put.
func TestARecheckHintCanCommitASubjectItWasNotAsked(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(1, &calls)
	// A request that states handles never reaches the recheck: only the handle
	// the question text names does.
	request.SubjectHandles = nil
	request.RequestedScope.SubjectHints[0].Source = string(hintsource.AnswerReuseAuthorizationRecheck)
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("dev-health-acr"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Committed) != 2 || resolution.Committed[0].CanonicalID != explicitHandleRepoID || resolution.Committed[1].CanonicalID != explicitHandlePRID {
		t.Fatalf("Committed = %#v, want the hinted repository and the unasked pull request", resolution.Committed)
	}
}
