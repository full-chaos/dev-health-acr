package v1

import (
	"strings"
	"testing"
)

func TestTheRepositoryWorkItemSentencesAreRecognisedWholeAndOnlyWhole(t *testing.T) {
	t.Parallel()
	for _, sentence := range []string{
		ContextFabricWorkItemRepositoryFreshnessLimitation,
		ContextFabricWorkItemRepositoryNoPullRequestsLimitation,
		ContextFabricWorkItemRepositoryUnlinkedLimitation,
		ContextFabricWorkItemRepositoryPartialLimitation,
		ContextFabricWorkItemRepositoryStrongestFirstLimitation,
		ContextFabricWorkItemRepositoryPeriodRoleRefusalLimitation,
		ContextFabricWorkItemRepositoryHeuristicLimitation(1),
		ContextFabricWorkItemRepositoryHeuristicLimitation(200),
		ContextFabricWorkItemRepositoryNoMatchLimitationPrefix + "currently has status todo" + ContextFabricWorkItemRepositoryNoMatchLimitationSuffix,
		ContextFabricWorkItemRepositoryNoMatchLimitationPrefix + "was completed in that period and a current status of done" + ContextFabricWorkItemRepositoryNoMatchLimitationSuffix,
	} {
		if !IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Errorf("not recognised as service-authored: %q", sentence)
		}
	}
	for _, sentence := range []string{
		ContextFabricWorkItemRepositoryHeuristicLimitation(0),
		"x" + ContextFabricWorkItemRepositoryHeuristicLimitation(2),
		ContextFabricWorkItemRepositoryHeuristicLimitation(2) + " More.",
		strings.ReplaceAll(ContextFabricWorkItemRepositoryFreshnessLimitation, "lag", "run"),
		// A repository prefix with a project suffix is no sentence this service composes.
		ContextFabricWorkItemRepositoryNoMatchLimitationPrefix + "currently has status todo" + ContextFabricWorkItemNoMatchLimitationSuffix,
		ContextFabricWorkItemNoMatchLimitationPrefix + "currently has status todo" + ContextFabricWorkItemRepositoryNoMatchLimitationSuffix,
	} {
		if IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Errorf("a sentence this service does not compose is recognised: %q", sentence)
		}
	}
}

func TestTheMembershipReasonNamesTheTierAndNeverUpgradesAnUnknownOne(t *testing.T) {
	t.Parallel()
	for tier, suffix := range map[string]string{
		ContextFabricWorkItemRepositoryTierNative:       "(native link)",
		ContextFabricWorkItemRepositoryTierExplicitText: "(link stated in text)",
		ContextFabricWorkItemRepositoryTierHeuristic:    "(heuristic match)",
		"":                    "(heuristic match)",
		"native ":             "(heuristic match)",
		"something_new_later": "(heuristic match)",
	} {
		if got := ContextFabricWorkItemRepositoryMembershipReason(tier); !strings.HasSuffix(got, suffix) || !strings.HasPrefix(got, "Issue linked to a pull request of the named repository ") {
			t.Errorf("tier %q reason = %q, want suffix %q", tier, got, suffix)
		}
	}
}
