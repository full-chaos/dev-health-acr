package v1

import "testing"

func TestTheNotAppliedStatusSentenceIsRecognisedOnlyWhenGenerated(t *testing.T) {
	for _, kind := range []string{"repository", "pull_request", "deployment"} {
		for _, value := range []string{"", "active", `has "quotes"`} {
			sentence := ContextFabricMemberStatusNotAppliedLimitation(kind, value)
			if !IsContextFabricMemberStatusNotAppliedLimitation(sentence) || !IsContextFabricMemberStatusLimitation(sentence) {
				t.Errorf("%q is not recognised", sentence)
			}
		}
	}
	for _, forged := range []string{
		`The status filter "active" was not applied: repositorys have no status.`,
		`The status filter "active" was not applied: widgets have no status.`,
		`The status filter active was not applied: repositories have no status.`,
		`Note: The status filter "active" was not applied: repositories have no status.`,
	} {
		if IsContextFabricMemberStatusNotAppliedLimitation(forged) {
			t.Errorf("forged sentence %q is recognised", forged)
		}
	}
}
