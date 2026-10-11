package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestServedKindLabelsUseDisplayNouns(t *testing.T) {
	kind := contractsv1.ContextFabricSubjectPullRequestReview
	attention := cohortAttentionScoreShape(kind, nil, nil)
	for name, label := range map[string]string{"attention title": attention.Title, "attention axis": attention.AxisLabel} {
		if strings.Contains(label, "_") {
			t.Errorf("%s carries a wire token: %q", name, label)
		}
	}
	if attention.Title != "Attention score by pull request review" || attention.AxisLabel != "pull request review" {
		t.Errorf("attention labels = %q / %q", attention.Title, attention.AxisLabel)
	}
	prompt := uncommittedTeamAnchorPrompt(SubjectResolution{Candidates: []SubjectCandidate{{Subject: SubjectRef{Kind: kind, CanonicalID: "x", Label: "Alpha"}}}})
	if prompt != "Did you mean pull request review Alpha?" {
		t.Errorf("anchor prompt = %q", prompt)
	}

	score := 1.0
	ranked := []CohortMember{{Subject: SubjectRef{Kind: kind, CanonicalID: "x", Label: "Alpha"}, Score: &score,
		Drivers: []contractsv1.ContextFabricCohortMemberDriver{{Signal: "investment_mix", WeightContributed: 1}}}}
	drivers, skip := cohortDriverContributionShape(kind, ranked, map[string]string{"x": "Alpha"})
	if drivers == nil {
		t.Fatalf("driver shape skipped: %v", skip)
	}
	if drivers.Title != "Score contribution by driver, per pull request review" || drivers.AxisLabel != "pull request review" {
		t.Errorf("driver labels = %q / %q", drivers.Title, drivers.AxisLabel)
	}
	if got := renderKindNoun(contractsv1.ContextFabricSubjectKind("squad")); got != "squad" {
		t.Errorf("a kind outside the vocabulary = %q, want its token kept", got)
	}

	cue := ClarificationPrompt([]SubjectCandidate{
		{Subject: SubjectRef{Kind: kind, CanonicalID: "a", Label: "Alpha"}},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "b", Label: "Alpha"}},
	})
	if strings.Contains(cue, "_") || !strings.Contains(cue, "pull request review") {
		t.Errorf("clarification prompt = %q", cue)
	}
}
