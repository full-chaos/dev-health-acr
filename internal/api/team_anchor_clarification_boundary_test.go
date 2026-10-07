package api

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A team named in "which projects does team X own?" that does not commit as
// the anchor must never be answered as an organization-level project cohort.

type uncommittedTeamAnchorGraph struct {
	projectlessTeamGraph
	candidates []contextfabric.SubjectCandidate
}

func (g uncommittedTeamAnchorGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{Candidates: g.candidates, Committed: []contextfabric.SubjectRef{}}, contextfabric.StructureOfferMaterial{}, contextfabric.CommitBasisSet{}, contextfabric.CommitDecisionDigestSet{}, nil
}

func (g uncommittedTeamAnchorGraph) DiscoverContext(ctx context.Context, p storage.Principal, r contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	out, err := g.projectlessTeamGraph.DiscoverContext(ctx, p, r)
	out.Cohort = &contextfabric.Cohort{
		Kind: contextfabric.SubjectProject, Rationale: "org-level", Complete: true,
		Members: []contextfabric.CohortMember{{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:aaaa1111-0000-4000-8000-000000000001", Label: "Org Project"},
			Rank:    1, InclusionReasons: []string{"Graph retrieval associated this subject with the requested organization-level condition."},
		}},
	}
	out.CohortPopulation = 1
	return out, err
}

func anchorCandidate(kind contextfabric.SubjectKind, id, provider string, state contextfabric.ResolutionState) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_" + id, State: state, Provider: provider,
		Subject:      contextfabric.SubjectRef{Kind: kind, CanonicalID: string(kind) + ":" + id, Label: "platform"},
		MatchedTerms: []string{"platform"}, MatchReasons: []string{"exact"}, Confidence: 0.9,
		EvidenceRefIDs: []string{"evidence_" + id + "_1234"},
	}
}

func TestAnOwnershipQuestionOnCollidingTeamLabelsIsAClarificationNotAnOrgCohort(t *testing.T) {
	node := askTeamOwnership(t, uncommittedTeamAnchorGraph{candidates: []contextfabric.SubjectCandidate{
		anchorCandidate(contextfabric.SubjectTeam, "t1", "linear", contextfabric.ResolutionAmbiguous),
		anchorCandidate(contextfabric.SubjectTeam, "t2", "jira", contextfabric.ResolutionAmbiguous),
		anchorCandidate(contextfabric.SubjectProject, "p1", "linear", contextfabric.ResolutionAmbiguous),
	}})
	if node.Structured.Status != "clarification_required" {
		t.Fatalf("status = %q, want clarification_required", node.Structured.Status)
	}
	if node.Structured.Cohort != nil {
		t.Fatalf("an org-level cohort was served beside the ambiguity: %+v", node.Structured.Cohort)
	}
	if node.Structured.Clarification == nil || len(node.Structured.Clarification.Candidates) != 3 {
		t.Fatalf("clarification = %+v, want the three colliding candidates", node.Structured.Clarification)
	}
	for _, want := range []string{"team", "project"} {
		if !strings.Contains(node.Structured.Clarification.Prompt, want) {
			t.Fatalf("prompt %q lacks the kind cue %q", node.Structured.Clarification.Prompt, want)
		}
	}
}

func TestAnOwnershipQuestionOnALoneUncommittedTeamCandidateIsNotAnOrgCohort(t *testing.T) {
	node := askTeamOwnership(t, uncommittedTeamAnchorGraph{candidates: []contextfabric.SubjectCandidate{
		anchorCandidate(contextfabric.SubjectTeam, "t1", "linear", contextfabric.ResolutionAmbiguous),
	}})
	if node.Structured.Cohort != nil && node.Structured.Cohort.Kind == "project" && node.Structured.Cohort.Total > 0 {
		t.Fatalf("an org-level project cohort was served for a named team: status=%q cohort=%+v", node.Structured.Status, node.Structured.Cohort)
	}
}
