package v1

import (
	"strings"
	"testing"
)

// CHAOS-12348: the two rows a non-cohort graph read files when its candidate
// set lost something. The wire values are pinned here as literals.

func TestTheCutGraphReadCodesHaveTheirWireValues(t *testing.T) {
	if got := ContextFabricCoverageDetailGraphHopWalkTruncated; got != "graph_hop_walk_truncated" {
		t.Fatalf("hop walk code = %q", got)
	}
	if got := ContextFabricCoverageDetailGraphTeamNarrowingUndecidedPaths; got != "graph_team_narrowing_undecided_paths" {
		t.Fatalf("undecided paths code = %q", got)
	}
}

func TestAHopWalkTruncatedDetailCarriesNoCount(t *testing.T) {
	code := ContextFabricCoverageDetailGraphHopWalkTruncated
	for name, mutate := range map[string]func(*ContextFabricCoverageDetail){
		"count":     func(d *ContextFabricCoverageDetail) { d.Count = intPtr(3) },
		"zero":      func(d *ContextFabricCoverageDetail) { d.Count = intPtr(0) },
		"kind":      func(d *ContextFabricCoverageDetail) { d.Kind = ContextFabricSubjectTeam },
		"fact kind": func(d *ContextFabricCoverageDetail) { d.FactKind = ContextFabricFactHealth },
	} {
		d := validDetailForCode(code)
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("%s: a detail with %s was accepted", code, name)
		}
	}
}

func TestAnUndecidedPathsDetailRequiresItsCount(t *testing.T) {
	code := ContextFabricCoverageDetailGraphTeamNarrowingUndecidedPaths
	for name, mutate := range map[string]func(*ContextFabricCoverageDetail){
		"no count":  func(d *ContextFabricCoverageDetail) { d.Count = nil },
		"negative":  func(d *ContextFabricCoverageDetail) { d.Count = intPtr(-1) },
		"kind":      func(d *ContextFabricCoverageDetail) { d.Kind = ContextFabricSubjectTeam },
		"fact kind": func(d *ContextFabricCoverageDetail) { d.FactKind = ContextFabricFactHealth },
	} {
		d := validDetailForCode(code)
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("%s: a detail with %s was accepted", code, name)
		}
	}
}

func TestTheCutGraphReadLabelsStateTheLoss(t *testing.T) {
	label := ComposeCoverageDetailLabel(validDetailForCode(ContextFabricCoverageDetailGraphHopWalkTruncated))
	if !strings.Contains(label, "cut at its limit") || !strings.Contains(label, "may exist") {
		t.Errorf("hop walk label %q does not say the read was cut and more may exist", label)
	}
	d := validDetailForCode(ContextFabricCoverageDetailGraphTeamNarrowingUndecidedPaths)
	if got, want := ComposeCoverageDetailLabel(d), "3 linked subjects were left out because their team ownership could not be decided"; got != want {
		t.Errorf("label = %q, want %q", got, want)
	}
	d.Count = intPtr(1)
	if got, want := ComposeCoverageDetailLabel(d), "1 linked subject was left out because its team ownership could not be decided"; got != want {
		t.Errorf("label = %q, want %q", got, want)
	}
}
