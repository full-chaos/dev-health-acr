package api

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A scoped question whose anchor cannot be bound to exactly one subject is a
// clarification that names the candidates, never the generic cohort.

type committedNoTermMatchGraph struct {
	uncommittedTeamAnchorGraph
	kind  contextfabric.SubjectKind
	count int
}

func (g committedNoTermMatchGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	candidates := []contextfabric.SubjectCandidate{}
	committed := []contextfabric.SubjectRef{}
	bases := contextfabric.CommitBasisSet{}
	for i := 0; i < g.count; i++ {
		id := string(rune('a' + i))
		c := anchorCandidate(g.kind, id, "linear", contextfabric.ResolutionCommitted)
		c.Subject.Label = "alpha-" + id
		c.MatchedTerms = []string{"alpha-" + id}
		candidates = append(candidates, c)
		committed = append(committed, c.Subject)
		bases.Record(c.Subject, contextfabric.CommitBasisAuthoritativeIdentity)
	}
	return contextfabric.SubjectResolution{Candidates: candidates, Committed: committed}, contextfabric.StructureOfferMaterial{}, bases, contextfabric.CommitDecisionDigestSet{}, nil
}

func memberKindFor(anchor contextfabric.SubjectKind) contextfabric.SubjectKind {
	if anchor == contextfabric.SubjectProject {
		return contextfabric.SubjectRepository
	}
	return contextfabric.SubjectProject
}

func TestAnAnchorBoundToNoneOfSeveralCommittedSubjectsIsAClarificationNotAGenericCohort(t *testing.T) {
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectTeam, contextfabric.SubjectProject, contextfabric.SubjectRepository} {
		t.Run(string(kind), func(t *testing.T) {
			node := askScopedAnchoredOn(t, committedNoTermMatchGraph{kind: kind, count: 2}, kind, memberKindFor(kind))
			if node.Structured.Cohort != nil {
				t.Fatalf("generic cohort served with an unbound anchor: status=%q", node.Structured.Status)
			}
			if node.Structured.Status != "clarification_required" {
				t.Fatalf("status = %q, want clarification_required", node.Structured.Status)
			}
			if node.Structured.Clarification == nil || len(node.Structured.Clarification.Candidates) != 2 {
				t.Fatalf("clarification = %+v, want the two candidates", node.Structured.Clarification)
			}
		})
	}
}

func TestACandidateOfTheAnchorKindWithNoneCommittedIsAClarificationNotAGenericCohort(t *testing.T) {
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectRepository} {
		t.Run(string(kind), func(t *testing.T) {
			node := askScopedAnchoredOn(t, uncommittedTeamAnchorGraph{candidates: []contextfabric.SubjectCandidate{
				anchorCandidate(kind, "c1", "github", contextfabric.ResolutionAmbiguous),
			}}, kind, memberKindFor(kind))
			if node.Structured.Cohort != nil || node.Structured.Status != "clarification_required" {
				t.Fatalf("status=%q cohort=%+v, want clarification_required and no cohort", node.Structured.Status, node.Structured.Cohort)
			}
		})
	}
}

func TestOneCommittedSubjectOfTheAnchorKindStillAnchorsTheCohort(t *testing.T) {
	node := askTeamOwnership(t, committedNoTermMatchGraph{kind: contextfabric.SubjectTeam, count: 1})
	if node.Structured.Status == "clarification_required" || node.Structured.Clarification != nil {
		t.Fatalf("a sole committed anchor was clarified: %q", node.Structured.Status)
	}
}
