package falkorgraph

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12715: the stored issue-to-PR fact, read the way a graph read serves it.
// The writer before CHAOS-12468 and CHAOS-12619 composed the fact from two raw
// ids; the nodes carry their titles, because an entity write reached them. The
// read rewrites the pull request end first (edgeProperties), and the fact's raw
// work item end must still read as its key. Live on askdev-1 at acr a29f4932 every
// such fact read "linear:CHAOS-2320 links pull request PR #872".
func TestAStoredIssuePullRequestFactNamesTheWorkItemByItsKey(t *testing.T) {
	t.Parallel()
	itemID, omitted, err := identity.Derive(identity.KindWorkItem, []string{"00000000-0000-0000-0000-000000000000", "linear:CHAOS-2320"}, nil)
	if err != nil || omitted {
		t.Fatalf("derive work item: %v %v", omitted, err)
	}
	const prID = "pull_request:920f9442-07df-4217-4dc4-c5833c0b8268:872"
	item := nodeProperties(map[string]interface{}{propKind: string(contractsv1.ContextFabricSubjectWorkItem), propCanonicalID: itemID, propLabel: "Generate taxonomy table in docs from investment_taxonomy.py"})
	pr := nodeProperties(map[string]interface{}{propKind: string(contractsv1.ContextFabricSubjectPullRequest), propCanonicalID: prID, propLabel: "ci(docs): add investment anti-drift guards"})
	subject := func(props map[string]interface{}) contextfabric.SubjectRef {
		kind, _ := props[propKind].(string)
		id, _ := props[propCanonicalID].(string)
		label, _ := props[propLabel].(string)
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(kind), CanonicalID: id, Label: label}
	}
	from, to := subject(item), subject(pr)
	for stored, want := range map[string]string{
		// The writer before CHAOS-12468: both ends raw.
		"linear:CHAOS-2320 links pull request " + prID: "CHAOS-2320 links pull request PR #872",
		// The writer between CHAOS-12468 and CHAOS-12619: the pull request end
		// already its label, the work item end raw.
		"linear:CHAOS-2320 links pull request PR #872": "CHAOS-2320 links pull request PR #872",
		// The writer after CHAOS-12619 stands.
		"CHAOS-2320 links pull request PR #872": "CHAOS-2320 links pull request PR #872",
		// Another work item's raw id is not this endpoint's and stays.
		"linear:CHAOS-9999 links pull request PR #872": "linear:CHAOS-9999 links pull request PR #872",
		// Another pull request's number is not this endpoint's and stays.
		"linear:CHAOS-2320 links pull request PR #873": "linear:CHAOS-2320 links pull request PR #873",
	} {
		fact, _ := edgeProperties(map[string]interface{}{propFact: stored})[propFact].(string)
		if got := contextfabric.RepairRawEdgeFact(fact, "LINKS_PULL_REQUEST", from, to); got != want {
			t.Errorf("stored %q served %q, want %q", stored, got, want)
		}
	}
}
