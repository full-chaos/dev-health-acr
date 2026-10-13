package falkorgraph

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func discoveredPullRequestFrame(status string) *contextfabric.QuestionFrame {
	d := &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectPullRequest}
	if status != "" {
		d.MemberQualifier = contextfabric.MemberQualifierStatus
		d.MemberQualifierValue = status
	}
	return &contextfabric.QuestionFrame{
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionDiscoveredKind, Discovered: d},
	}
}

func investigateDiscoveredPullRequests(t *testing.T, status string) contextfabric.InvestigationResult {
	t.Helper()
	pulls := organizationPullRequests(30, 20)
	result, _ := investigatePullRequestsFramed(t, countingSynthesizer{}, pulls, status, storage.Principal{OrgID: "org-1"}, false, discoveredPullRequestFrame)
	return result
}

func filterSentences(result contextfabric.InvestigationResult, status string) int {
	n := 0
	for _, l := range result.Limitations {
		if strings.Contains(l, contextfabric.PullRequestStatusFilterDisclosure(status)) {
			n++
		}
	}
	return n
}

// TestADiscoveredKindQuestionAppliesItsStatusWithoutAnAnchor: the same
// organization, awaiting_review and merged ask different questions and are
// served different sets, each naming its filter.
func TestADiscoveredKindQuestionAppliesItsStatusWithoutAnAnchor(t *testing.T) {
	awaiting := investigateDiscoveredPullRequests(t, "awaiting_review")
	merged := investigateDiscoveredPullRequests(t, "merged")
	a, m := servedCohortIDs(awaiting), servedCohortIDs(merged)
	if len(m) != 20 {
		t.Fatalf("merged served %d pull requests, want 20", len(m))
	}
	if len(a) == 0 || len(a) >= 10 {
		t.Fatalf("awaiting_review served %d pull requests, want the unreviewed open ones (fewer than 10)", len(a))
	}
	for name, r := range map[string]contextfabric.InvestigationResult{"awaiting_review": awaiting, "merged": merged} {
		if filterSentences(r, map[string]string{"awaiting_review": "awaiting_review", "merged": "merged"}[name]) == 0 {
			t.Fatalf("%s: no limitation names the status filter: %q", name, r.Limitations)
		}
	}
}

// TestADiscoveredKindStatusNoValueFitsIsStatedNotServedAsFiltered: a status the
// vocabulary cannot name is served unfiltered with the sentence that says the
// state was not applied, never as a silent list.
func TestADiscoveredKindStatusNoValueFitsIsStatedNotServedAsFiltered(t *testing.T) {
	pulls := organizationPullRequests(30, 20)
	result, _ := investigatePullRequestsFramed(t, countingSynthesizer{}, pulls, "", storage.Principal{OrgID: "org-1"}, false, func(string) *contextfabric.QuestionFrame {
		frame := discoveredPullRequestFrame("")
		frame.SubjectExpression.Discovered.MemberQualifier = contextfabric.MemberQualifierStatus
		return frame
	})
	if got := len(servedCohortIDs(result)); got != len(pulls) {
		t.Fatalf("served %d pull requests, want the unfiltered %d", got, len(pulls))
	}
	want := contractsv1.ContextFabricMemberStatusUnappliedLimitation("pull_request")
	for _, l := range result.Limitations {
		if l == want {
			return
		}
	}
	t.Fatalf("limitations %q lack the unapplied-state sentence %q", result.Limitations, want)
}
