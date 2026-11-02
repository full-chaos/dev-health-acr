package falkorgraph

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The state an edge written before CHAOS-12468 left behind, read through both
// shapes a read can return a node or an edge in.
func TestAReadNeverServesARawPullRequestID(t *testing.T) {
	const id = "pull_request:920f9442-07df-4217-4dc4-c5833c0b8268:5063"
	raw := map[string]interface{}{propKind: string(contractsv1.ContextFabricSubjectPullRequest), propCanonicalID: id, propLabel: id}
	if got := nodeProperties(raw)[propLabel]; got != "PR #5063" {
		t.Errorf("node label = %v, want PR #5063", got)
	}
	if got := walkNode(raw).Properties[propLabel]; got != "PR #5063" {
		t.Errorf("walk node label = %v, want PR #5063", got)
	}
	if got := raw[propLabel]; got != id {
		t.Errorf("the read changed its input: %v", got)
	}
	for fact, want := range map[string]string{
		"linear:CHAOS-9999 links pull request " + id:                 "linear:CHAOS-9999 links pull request PR #5063",
		"PR #999 review belongs to pull request " + id:               "PR #999 review belongs to pull request PR #5063",
		"gh:acme/w#7 links pull request PR #5063":                    "gh:acme/w#7 links pull request PR #5063",
		"see pull_request:notanumber":                                "see pull_request:notanumber",
		"see pull_request:r:12abc":                                   "see pull_request:r:12abc",
		"notes on pull_request:r:1 links pull request " + id:         "notes on pull_request:r:1 links pull request PR #5063",
		"pull request pull_request:r:1 belongs to repository acme/a": "pull request pull_request:r:1 belongs to repository acme/a",
		"x links pull request pull_request:r:12abc":                  "x links pull request pull_request:r:12abc",
	} {
		edge := map[string]interface{}{propFact: fact}
		if got := edgeProperties(edge)[propFact]; got != want {
			t.Errorf("fact %q = %v, want %q", fact, got, want)
		}
		if got := walkEdge(edge).Properties[propFact]; got != want {
			t.Errorf("walk fact %q = %v, want %q", fact, got, want)
		}
	}
}

// Only a pull request whose label IS its id is repaired: a title stays, and
// another kind keeps what it holds.
func TestAReadLeavesEveryOtherLabelAlone(t *testing.T) {
	const id = "pull_request:r:5063"
	for name, node := range map[string]map[string]interface{}{
		"title":      {propKind: string(contractsv1.ContextFabricSubjectPullRequest), propCanonicalID: id, propLabel: "ci: one canary leg"},
		"short form": {propKind: string(contractsv1.ContextFabricSubjectPullRequest), propCanonicalID: id, propLabel: "PR #5063"},
		"other kind": {propKind: string(contractsv1.ContextFabricSubjectWorkItem), propCanonicalID: id, propLabel: id},
		"other id":   {propKind: string(contractsv1.ContextFabricSubjectPullRequest), propCanonicalID: id, propLabel: "pull_request:r:9"},
		"no number":  {propKind: string(contractsv1.ContextFabricSubjectPullRequest), propCanonicalID: "pull_request:r:x", propLabel: "pull_request:r:x"},
		"no label":   {propKind: string(contractsv1.ContextFabricSubjectPullRequest), propCanonicalID: id},
	} {
		if got, want := nodeProperties(node)[propLabel], node[propLabel]; got != want {
			t.Errorf("%s: label = %v, want %v", name, got, want)
		}
	}
}

// A property that is not a string still normalizes: the repair must not take
// the list handling of normalizeProperties with it.
func TestReadRepairKeepsNormalizedLists(t *testing.T) {
	got := nodeProperties(map[string]interface{}{propEvidenceRefs: []interface{}{"a"}})[propEvidenceRefs]
	if list, ok := got.([]string); !ok || len(list) != 1 {
		t.Fatalf("evidence refs = %#v, want []string", got)
	}
	got = edgeProperties(map[string]interface{}{propEvidenceRefs: []interface{}{"a"}})[propEvidenceRefs]
	if list, ok := got.([]string); !ok || len(list) != 1 {
		t.Fatalf("edge evidence refs = %#v, want []string", got)
	}
}
