package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The one-named-subject rule is decided on candidates this package builds, so
// the test builds them with NodeCandidate from seeded graph nodes: every
// candidate carries the term that retrieved it, and only the identity ones
// carry an identity mechanism.

func searchNode(kind contextfabric.SubjectKind, id, label string, relevance float64, aliases ...string) CandidateNode {
	node := candidateNode(kind, id, label, relevance, nil)
	if len(aliases) > 0 {
		node.Attributes["aliases"] = aliases
	}
	node.Mechanism = contextfabric.MatchLexical
	return node
}

func retrieved(t *testing.T, term string, nodes ...CandidateNode) contextfabric.SubjectResolution {
	t.Helper()
	principal := storage.Principal{OrgID: "org_1"}
	resolution := contextfabric.SubjectResolution{}
	for _, node := range nodes {
		candidate, ok := NodeCandidate(principal, contextfabric.RequestedScope{}, term, node, noInternalSubjects, true, nil, "")
		if !ok {
			t.Fatalf("NodeCandidate refused %s", node.Name)
		}
		if len(candidate.MatchedTerms) == 0 || len(candidate.MatchMechanisms) == 0 {
			t.Fatalf("NodeCandidate built %s with no term or no mechanism: %+v", node.Name, candidate)
		}
		resolution.Candidates = append(resolution.Candidates, candidate)
	}
	return resolution
}

func TestOneNamedRepositoryWithNeighboursOfItsNamingFamilyIsOneSubject(t *testing.T) {
	t.Parallel()
	frame := &contextfabric.QuestionFrame{SubjectExpression: contextfabric.SubjectExpression{
		Kind:   contextfabric.SubjectExpressionChildrenOfScope,
		Scoped: &contextfabric.ScopedSetExpression{MemberKind: contextfabric.SubjectRepository},
	}}
	repository := func(id, label string, relevance float64, aliases ...string) CandidateNode {
		return searchNode(contextfabric.SubjectRepository, id, label, relevance, aliases...)
	}
	named := repository("repository:NAMED", "acme/alpha-billing-api", 1, "alpha-billing-api")
	neighbours := []CandidateNode{
		repository("repository:N1", "acme/alpha-billing-web", 0.75),
		repository("repository:N2", "acme/alpha-billing-worker", 0.75),
		repository("repository:N3", "acme/billing-docs", 0.667),
		repository("repository:N4", "acme/alpha-gateway", 0.667),
	}
	project := searchNode(contextfabric.SubjectProject, "project:SAME_ALIAS", "Alpha Billing", 1, "alpha-billing-api")
	twin := repository("repository:TWIN", "acme/alpha-billing-api", 1)

	rows := []struct {
		name  string
		term  string
		nodes []CandidateNode
		want  contextfabric.CountPopulationScopeDecision
		terms int
		ident int
	}{
		{"short label, by alias", "alpha-billing-api", append([]CandidateNode{named}, neighbours...), contextfabric.CountPopulationScopeSingleSubject, 5, 1},
		{"full label, by exact", "acme/alpha-billing-api", append([]CandidateNode{named}, neighbours...), contextfabric.CountPopulationScopeSingleSubject, 5, 1},
		{"a project holds the same alias", "alpha-billing-api", append([]CandidateNode{named, project}, neighbours...), contextfabric.CountPopulationScopeAnchorUnresolved, 6, 2},
		{"a second repository has the same exact name", "acme/alpha-billing-api", append([]CandidateNode{named, twin}, neighbours...), contextfabric.CountPopulationScopeAnchorUnresolved, 6, 2},
	}
	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			resolution := retrieved(t, row.term, row.nodes...)
			resolution.Committed = []contextfabric.SubjectRef{resolution.Candidates[0].Subject}
			frameCopy := *frame
			scoped := *frame.SubjectExpression.Scoped
			scoped.AnchorTerms = []string{row.term}
			frameCopy.SubjectExpression.Scoped = &scoped

			scope := contextfabric.DecideCountPopulationScope(&frameCopy, "", resolution, nil, contextfabric.CohortMemberSourceNotApplicable)
			if scope.Decision != row.want || scope.AnchorTermMatches != row.terms || scope.AnchorIdentityMatches != row.ident {
				t.Fatalf("decision=%q term_matches=%d identity_matches=%d, want %q %d %d", scope.Decision, scope.AnchorTermMatches, scope.AnchorIdentityMatches, row.want, row.terms, row.ident)
			}
		})
	}
}
