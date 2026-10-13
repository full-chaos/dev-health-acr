package contextfabric

import (
	"slices"
	"testing"
)

func TestDegradedReasonOrderFollowsTheMachineKeyNotTheKindLabel(t *testing.T) {
	census := "kind_census_truncated:work_item:2000:1"
	status := factKindPrefix(FactStatus) + ": the read failed"
	facts := Coverage{Partial: true, DegradedReasons: []string{status}, Details: []CoverageDetail{{
		DetailID: "a", Source: "canonical_fact:status", Code: "fact_read_failed", Degrading: true, FactKind: FactStatus, Raw: status}}}
	graph := Coverage{Partial: true, DegradedReasons: []string{census}, Details: []CoverageDetail{{
		DetailID: "b", Source: "context-fabric:graph", Code: "kind_census_truncated", Degrading: true, Raw: census}}}
	for name, groups := range map[string][]Coverage{"facts first": {facts, graph}, "graph first": {graph, facts}} {
		merged := MergeCoverage("org_test", groups...)
		if want := []string{census, status}; !slices.Equal(merged.DegradedReasons, want) {
			t.Errorf("%s: reasons %q, want %q: the machine key \"status\" sorts after \"kind_census\" whatever the capital letter of the label does", name, merged.DegradedReasons, want)
		}
		if len(merged.Details) != 2 || merged.Details[0].Raw != census || merged.Details[1].Raw != status {
			t.Errorf("%s: details %+v, want the same order as the reasons", name, merged.Details)
		}
	}
}
