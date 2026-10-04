package contextfabric

import "testing"

func TestOwnershipCohortAnchor(t *testing.T) {
	repo := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:github:acme/api", Label: "acme/api"}
	teamFrame := func() *QuestionFrame {
		expression := scopedExpression(SubjectTeam)
		expression.Scoped.AnchorTerms = []string{"Acme/API"}
		return frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, expression)
	}
	matched := func(subject SubjectRef, terms ...string) SubjectResolution {
		return SubjectResolution{
			Committed:  []SubjectRef{subject},
			Candidates: []SubjectCandidate{{Subject: subject, State: ResolutionCommitted, MatchedTerms: terms}},
		}
	}
	committed := func(subjects ...SubjectRef) SubjectResolution { return SubjectResolution{Committed: subjects} }

	cases := []struct {
		name       string
		frame      *QuestionFrame
		resolution SubjectResolution
		declared   SubjectKind
		accepted   bool
	}{
		{"declared repository, anchor term matched", teamFrame(), matched(repo, "acme/api"), SubjectRepository, true},
		{"declared repository, no term matched", teamFrame(), matched(repo, "other"), SubjectRepository, false},
		{"declared repository, no candidate", teamFrame(), committed(repo), SubjectRepository, false},
		{"declared none, anchor term matched", teamFrame(), matched(repo, "acme/api"), "", true},
		{"nil frame", nil, committed(repo), SubjectRepository, false},
		{"not children of scope", frameWithPointer(nil, discoveredExpression(SubjectTeam)), committed(repo), SubjectRepository, false},
		{"scoped payload under another expression kind", frameWithPointer(nil, SubjectExpression{Kind: SubjectExpressionDiscoveredKind, Scoped: scopedExpression(SubjectTeam).Scoped}), committed(repo), SubjectRepository, false},
		{"member kind not team", frameWithPointer(nil, scopedExpression(SubjectDeployment)), committed(repo), SubjectRepository, false},
		{"none committed", teamFrame(), committed(), SubjectRepository, false},
		{"two committed", teamFrame(), committed(repo, SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:github:acme/web", Label: "acme/web"}), SubjectRepository, false},
		{"committed project", teamFrame(), committed(SubjectRef{Kind: SubjectProject, CanonicalID: "project:p", Label: "p"}), SubjectRepository, false},
		{"empty label", teamFrame(), committed(SubjectRef{Kind: SubjectRepository, CanonicalID: repo.CanonicalID}), SubjectRepository, false},
		{"declared project", teamFrame(), matched(repo, "acme/api"), SubjectProject, false},
		{"declared none, no term matched", teamFrame(), matched(repo, "other"), "", false},
		{"declared none, no candidate", teamFrame(), committed(repo), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := OwnershipCohortAnchor(tc.frame, tc.resolution, tc.declared)
			if ok != tc.accepted {
				t.Fatalf("accepted = %v, want %v", ok, tc.accepted)
			}
			if tc.accepted && got != repo {
				t.Fatalf("anchor = %+v, want %+v", got, repo)
			}
			if !tc.accepted && got != (SubjectRef{}) {
				t.Fatalf("refused call returned anchor %+v", got)
			}
		})
	}
}

func TestOwnershipRoutingVersionMovedPastTheBoundOnlyRule(t *testing.T) {
	t.Parallel()
	const versionBeforeALabelNamedRepositoryRouted = "ownership-routing.v1"
	if OwnershipRoutingVersion == versionBeforeALabelNamedRepositoryRouted {
		t.Fatalf("OwnershipRoutingVersion = %q, want it moved past %q -- an answer saved when a repository named by its label never routed through ownership holds teams that do not own it and must not be reused", OwnershipRoutingVersion, versionBeforeALabelNamedRepositoryRouted)
	}
}
