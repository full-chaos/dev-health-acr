package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var teamScopePullRequest = SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:github:api:1", Label: "PR #1"}

func teamPullRequestCapabilities() map[FactKind]FactCapability {
	return map[FactKind]FactCapability{
		FactPullRequests: planCapability(FactPullRequests, "pull_requests", SubjectTeam, SubjectProject, SubjectPullRequest),
	}
}

func TestTeamPullRequestsExpandOnlyWhenPerPullRequestFactsAreAsked(t *testing.T) {
	cases := []struct {
		name      string
		subject   SubjectRef
		perPR     bool
		wantCalls int
	}{
		{"team, default read: rollup stands alone", scopeTeam, false, 0},
		{"team, per pull request facts asked", scopeTeam, true, 1},
		{"project, default read keeps its additive expansion", scopeProject, false, 1},
	}
	for _, c := range cases {
		expander := &recordingScopeExpander{targets: []SubjectRef{teamScopePullRequest}}
		request := scopeRequest([]SubjectRef{c.subject}, []FactRequirement{{Kind: FactPullRequests}}, TemporalCurrent)
		request.PullRequestFacts = c.perPR
		scope := NewFactReadScopeResolver(expander).Resolve(
			context.Background(), storage.Principal{OrgID: "org_1"},
			newFactScopeResolveInput(request), teamPullRequestCapabilities(),
		)
		if expander.calls != c.wantCalls {
			t.Fatalf("%s: expander calls = %d, want %d (events %+v)", c.name, expander.calls, c.wantCalls, scope.Events)
		}
		if c.wantCalls == 0 && len(scope.DerivedSubjects) != 0 {
			t.Fatalf("%s: derived subjects admitted: %+v", c.name, scope.DerivedSubjects)
		}
	}
}

func TestPullRequestFactsWantedByFrame(t *testing.T) {
	if pullRequestFactsWanted(nil) {
		t.Fatal("no frame must not ask for per pull request facts")
	}
	if pullRequestFactsWanted(&QuestionFrame{}) {
		t.Fatal("a frame with no member kind must not ask for per pull request facts")
	}
	status := pullRequestStatusQuestionFrame(PullRequestStatusOpen)
	if !pullRequestFactsWanted(&status) {
		t.Fatal("a pull request status filter must ask for per pull request facts")
	}
	members := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"Vesper"}, MemberKind: SubjectPullRequest},
		},
	}
	if !pullRequestFactsWanted(&members) {
		t.Fatal("pull requests as the member kind must ask for per pull request facts")
	}
	repositories := members
	repositories.SubjectExpression.Scoped = &ScopedSetExpression{AnchorTerms: []string{"Vesper"}, MemberKind: SubjectRepository}
	if pullRequestFactsWanted(&repositories) {
		t.Fatal("repository members must not ask for per pull request facts")
	}
}
