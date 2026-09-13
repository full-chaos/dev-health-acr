package v1

import (
	"strings"
	"testing"
)

// The kind_census_truncated field rule, over its whole input domain
// (D47): Kind, Declared and Served are required together and only for this
// code, absent/zero/negative/served>declared cells all exercised through
// Validate itself.

func kindCensusTruncatedDetail() ContextFabricCoverageDetail {
	return validDetailForCode(ContextFabricCoverageDetailKindCensusTruncated)
}

func TestKindCensusTruncatedInputDomain(t *testing.T) {
	type cell struct {
		field, shape string
		mutate       func(*ContextFabricCoverageDetail)
		wantErr      string // "" = must validate
	}
	cells := []cell{
		{"all", "canonical", func(*ContextFabricCoverageDetail) {}, ""},
		// kind
		{"kind", "absent", func(d *ContextFabricCoverageDetail) { d.Kind = "" }, "requires kind"},
		{"kind", "out of vocabulary", func(d *ContextFabricCoverageDetail) { d.Kind = "made_up" }, "kind \"made_up\" is invalid"},
		{"kind", "case-shadowed", func(d *ContextFabricCoverageDetail) { d.Kind = "Team" }, "is invalid"},
		// declared
		{"declared", "absent", func(d *ContextFabricCoverageDetail) { d.Declared = nil }, "requires declared and served"},
		{"declared", "zero", func(d *ContextFabricCoverageDetail) { d.Declared = intPtr(0); d.Served = intPtr(0) }, ""},
		{"declared", "negative", func(d *ContextFabricCoverageDetail) { d.Declared = intPtr(-1) }, "non-negative"},
		// served
		{"served", "absent", func(d *ContextFabricCoverageDetail) { d.Served = nil }, "requires declared and served"},
		{"served", "zero", func(d *ContextFabricCoverageDetail) { d.Served = intPtr(0) }, ""},
		{"served", "negative", func(d *ContextFabricCoverageDetail) { d.Served = intPtr(-1) }, "non-negative"},
		// both absent together
		{"declared+served", "both absent", func(d *ContextFabricCoverageDetail) { d.Declared, d.Served = nil, nil }, "requires declared and served"},
		// boundary: served == declared is legal (an uncut-in-effect reading served in full)
		{"served vs declared", "equal (boundary)", func(d *ContextFabricCoverageDetail) { d.Declared, d.Served = intPtr(5), intPtr(5) }, ""},
		// served > declared is the one relation this code must refuse
		{"served vs declared", "served exceeds declared", func(d *ContextFabricCoverageDetail) { d.Declared, d.Served = intPtr(5), intPtr(6) }, "served must not exceed declared"},
		{"served vs declared", "served far exceeds declared", func(d *ContextFabricCoverageDetail) { d.Declared, d.Served = intPtr(0), intPtr(1) }, "served must not exceed declared"},
		// fields this code forbids
		{"fact_kind", "present", func(d *ContextFabricCoverageDetail) { d.FactKind = ContextFabricFactHealth }, "forbids fact_kind"},
		{"origin_kind", "present", func(d *ContextFabricCoverageDetail) { d.OriginKind = ContextFabricSubjectTeam }, "forbids scope fields"},
		{"scope_outcome", "present", func(d *ContextFabricCoverageDetail) { d.ScopeOutcome = "expanded" }, "forbids scope fields"},
		{"source_state", "present", func(d *ContextFabricCoverageDetail) { d.SourceState = ContextFabricSourceAvailable }, "forbids source_state"},
		{"count", "present", func(d *ContextFabricCoverageDetail) { d.Count = intPtr(1) }, "forbids count"},
		{"narrowed", "present", func(d *ContextFabricCoverageDetail) {
			d.Narrowed, d.SkippedKinds = true, []ContextFabricSubjectKind{ContextFabricSubjectProject}
		}, "forbids narrowing fields"},
		{"supported_kinds", "present", func(d *ContextFabricCoverageDetail) {
			d.SupportedKinds = []ContextFabricSubjectKind{ContextFabricSubjectProject}
		}, "forbids supported_kinds"},
		// degrading: this code MAY degrade (unlike fact_pruned/graph_validity_unbounded/fact_read_origin_state)
		{"degrading", "false", func(d *ContextFabricCoverageDetail) { d.Degrading = false }, ""},
		{"degrading", "true", func(d *ContextFabricCoverageDetail) { d.Degrading = true }, ""},
		// the common bounds still apply
		{"label", "empty", func(d *ContextFabricCoverageDetail) { d.Label = "" }, "label"},
		{"detail_id", "empty", func(d *ContextFabricCoverageDetail) { d.DetailID = "" }, "detail id"},
		{"source", "empty", func(d *ContextFabricCoverageDetail) { d.Source = " " }, "source"},
	}
	for _, c := range cells {
		d := kindCensusTruncatedDetail()
		c.mutate(&d)
		err := d.Validate()
		got := "ok"
		if err != nil {
			got = err.Error()
		}
		t.Logf("CELL field=%s shape=%s -> %s", c.field, c.shape, got)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s / %s: rejected, want accepted: %v", c.field, c.shape, err)
		case c.wantErr != "" && err == nil:
			t.Errorf("%s / %s: accepted, want rejection mentioning %q", c.field, c.shape, c.wantErr)
		case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
			t.Errorf("%s / %s: rejected for the wrong reason: %v (want %q)", c.field, c.shape, err, c.wantErr)
		}
	}

	// EVERY member of the subject-kind vocabulary validates as Kind, and
	// composes an in-bounds label -- read from the registry, not restated.
	for _, kind := range ContextFabricSubjectKindVocabulary() {
		d := kindCensusTruncatedDetail()
		d.Kind = kind
		if err := d.Validate(); err != nil {
			t.Errorf("kind=%s: rejected: %v", kind, err)
		}
		label := ComposeCoverageDetailLabel(d)
		if strings.TrimSpace(label) == "" || len([]rune(label)) > ContextFabricCoverageDetailLabelMaxLength {
			t.Errorf("kind=%s: label out of bounds: %q", kind, label)
		}
	}
}

// TestKindCensusTruncatedNeverJoinsThePopulationQualifyingAllowList pins that
// this code, though structurally similar to population_truncated (both name a
// count that is a floor), never joins coverageDetailCodeQualifiesPopulation:
// it carries its OWN Declared/Served pair, on Coverage.Details, never on a
// RequirementOutcomeRow's CauseCoverage -- the field that predicate serves.
func TestKindCensusTruncatedNeverJoinsThePopulationQualifyingAllowList(t *testing.T) {
	if coverageDetailCodeQualifiesPopulation(ContextFabricCoverageDetailKindCensusTruncated) {
		t.Error("kind_census_truncated must not qualify a population on the outcome-row allow-list")
	}
}

// TestKindCensusTruncatedLabelNamesKindAndBothNumbers pins the label's words
// with a fixture where kind, declared and served are all distinct, so a
// label that swapped declared/served, or dropped the kind, would not match.
func TestKindCensusTruncatedLabelNamesKindAndBothNumbers(t *testing.T) {
	d := kindCensusTruncatedDetail()
	d.Kind, d.Declared, d.Served = ContextFabricSubjectRepository, intPtr(777), intPtr(3)
	label := ComposeCoverageDetailLabel(d)
	if !strings.Contains(label, "777") {
		t.Errorf("label %q does not name Declared (777)", label)
	}
	if !strings.Contains(label, "3") {
		t.Errorf("label %q does not name Served (3)", label)
	}
	if !strings.Contains(label, "repository") {
		t.Errorf("label %q does not name Kind (repository)", label)
	}
}
