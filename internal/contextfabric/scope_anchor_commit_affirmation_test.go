package contextfabric

import "testing"

func TestCommitAffirmationKeepsTheCensusAttestedPullRequestBesideAScopeAnchor(t *testing.T) {
	repo := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:r-1", Label: "full-chaos/dev-health-acr"}
	pr := SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:r-1:747", Label: "PR #747"}
	build := func(withPRFact bool) (InvestigationResult, affirmationInputs) {
		result := affirmationResult()
		result.SubjectResolution.Committed = []SubjectRef{repo, pr}
		result.SubjectResolution.Candidates = []SubjectCandidate{
			{Subject: repo, State: ResolutionCommitted, Confidence: 1, MatchMechanisms: []MatchMechanism{MatchAlias}},
			{Subject: pr, State: ResolutionCommitted, Confidence: 0.755, MatchMechanisms: []MatchMechanism{MatchLexical, MatchVector}},
		}
		facts := emptyAffirmationFacts()
		if withPRFact {
			facts = factsForSubject(pr)
			result.ClaimedFacts = []ClaimedFact{{ClaimID: "claim-pr", Kind: FactStatus, Subject: pr, Field: "state", Value: ScalarValue{}}}
		}
		bases := CommitBasisSet{}
		bases.Record(repo, CommitBasisStatistical)
		bases.Record(pr, CommitBasisStatistical)
		return result, affirmationInputs{Bases: bases, Candidates: result.SubjectResolution.Candidates, Graph: emptyAffirmationGraph(), Facts: facts}
	}

	result, inputs := build(true)
	applyCommitAffirmation(&result, inputs)
	if got := result.SubjectResolution.Committed; len(got) != 1 || got[0] != pr {
		t.Fatalf("committed = %v, want only the pull request: it stands on its own canonical fact, the ungrounded repository is retracted", got)
	}

	result, inputs = build(false)
	applyCommitAffirmation(&result, inputs)
	if got := result.SubjectResolution.Committed; len(got) != 0 {
		t.Fatalf("committed = %v, want both retracted when the answer stands on neither", got)
	}
}
