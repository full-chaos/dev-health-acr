package devhealthfacts

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func scopeSubject(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: kind, CanonicalID: id}
}

func TestTeamOwnershipStateStaysOffTheKindWhenOtherSubjectKindsAreRead(t *testing.T) {
	none := scopeSubject(contextfabric.SubjectTeam, "team:none")
	other := scopeSubject(contextfabric.SubjectTeam, "team:other")
	repo := scopeSubject(contextfabric.SubjectRepository, "repository:r")

	t.Run("teams only, every team owns nothing: not_applicable with the all-teams reason", func(t *testing.T) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceNoData}
		applyTeamsOwningNothing(&result, []contextfabric.SubjectRef{none}, 1, []contextfabric.SubjectRef{none})
		if result.State != contextfabric.SourceNotApplicable || result.Reason != teamNoOwnedRepositoriesReason {
			t.Fatalf("state %q reason %q", result.State, result.Reason)
		}
	})
	t.Run("a repository is read beside the team: state and kind reason stay the provider's", func(t *testing.T) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceNoData}
		applyTeamsOwningNothing(&result, []contextfabric.SubjectRef{none, repo}, 1, []contextfabric.SubjectRef{none})
		if result.State != contextfabric.SourceNoData || result.Reason != "" {
			t.Fatalf("state %q reason %q, want the provider's own", result.State, result.Reason)
		}
		if !strings.Contains(result.RequestNote, "1 of 1 requested teams own no repository") {
			t.Fatalf("request note %q", result.RequestNote)
		}
		if got := result.SubjectReasons[contextfabric.FactSubjectKey(none)]; got != teamNoOwnedRepositoriesReason {
			t.Fatalf("own reason %q", got)
		}
		if _, has := result.SubjectReasons[contextfabric.FactSubjectKey(repo)]; has {
			t.Fatal("the repository carries a team reason")
		}
	})
	t.Run("some teams own repositories and no fact was served: no kind-wide not_applicable", func(t *testing.T) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceNoData}
		applyTeamsOwningNothing(&result, []contextfabric.SubjectRef{none, other}, 2, []contextfabric.SubjectRef{none})
		if result.State != contextfabric.SourceNoData || result.Reason != "" {
			t.Fatalf("state %q reason %q", result.State, result.Reason)
		}
	})
}
