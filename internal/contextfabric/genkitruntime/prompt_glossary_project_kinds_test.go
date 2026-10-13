package genkitruntime

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

func TestInterpretationPromptNamesProjectForBlockersAndPullRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind       contextfabric.FactKind
		capability contextfabric.FactCapability
		want       string
	}{
		{contextfabric.FactBlockers, (&devhealthfacts.BlockersProvider{}).Capability(), "for a team or a project, its blocked-item counts (work_item, team, project)."},
		{contextfabric.FactPullRequests, (&devhealthfacts.PullRequestsProvider{}).Capability(), "for a team or a project, its pull request counts (pull_request, team, project)."},
	} {
		if !strings.Contains(interpretationSystemPrompt, "\n- "+string(tc.kind)+": ") || !strings.Contains(interpretationSystemPrompt, tc.want) {
			t.Errorf("rendered prompt lacks %q for %s", tc.want, tc.kind)
		}
		served := false
		for _, subject := range tc.capability.SupportedSubjectKinds {
			served = served || subject == contextfabric.SubjectProject
		}
		if !served {
			t.Errorf("%s names project in the glossary but its provider does not serve project", tc.kind)
		}
	}
}
