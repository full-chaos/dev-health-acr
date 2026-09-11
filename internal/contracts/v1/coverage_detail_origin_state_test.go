package v1

import (
	"sort"
	"strings"
	"testing"
)

// The fact_read_origin_state field rule, over its whole input domain.
//
// Every field of a coverage detail, crossed with the shapes it can arrive in,
// against the one code that requires OriginKind OUTSIDE the scope quartet.
// The cells are executed through Validate itself, and the vocabularies are
// read from their registries (the state label registry, the kind
// vocabularies), never restated here.

func originStateDetail() ContextFabricCoverageDetail {
	return validDetailForCode(ContextFabricCoverageDetailFactReadOriginState)
}

func TestFactReadOriginStateInputDomain(t *testing.T) {
	type cell struct {
		field, shape string
		mutate       func(*ContextFabricCoverageDetail)
		wantErr      string // "" = must validate
	}
	cells := []cell{
		{"all", "canonical", func(*ContextFabricCoverageDetail) {}, ""},
		// fact_kind
		{"fact_kind", "absent", func(d *ContextFabricCoverageDetail) { d.FactKind = "" }, "requires fact_kind"},
		{"fact_kind", "out of vocabulary", func(d *ContextFabricCoverageDetail) { d.FactKind = "made_up" }, "fact kind \"made_up\" is invalid"},
		{"fact_kind", "case-shadowed", func(d *ContextFabricCoverageDetail) { d.FactKind = "Health" }, "is invalid"},
		// source_state
		{"source_state", "absent", func(d *ContextFabricCoverageDetail) { d.SourceState = "" }, "requires source_state"},
		{"source_state", "out of vocabulary", func(d *ContextFabricCoverageDetail) { d.SourceState = "broken" }, "source state \"broken\" is invalid"},
		// origin_kind
		{"origin_kind", "absent", func(d *ContextFabricCoverageDetail) { d.OriginKind = "" }, "requires origin_kind"},
		{"origin_kind", "out of vocabulary", func(d *ContextFabricCoverageDetail) { d.OriginKind = "tribe" }, "origin kind \"tribe\" is invalid"},
		// the scope quartet's other three, each alone and all together
		{"scope_outcome", "present", func(d *ContextFabricCoverageDetail) { d.ScopeOutcome = "expanded" }, "forbids scope fields"},
		{"policy", "present", func(d *ContextFabricCoverageDetail) { d.Policy = "none" }, "forbids scope fields"},
		{"basis", "present", func(d *ContextFabricCoverageDetail) { d.Basis = "direct" }, "forbids scope fields"},
		{"scope quartet", "complete", func(d *ContextFabricCoverageDetail) {
			d.ScopeOutcome, d.Policy, d.Basis = "expanded", "none", "direct"
		}, "forbids scope fields"},
		// count
		{"count", "zero", func(d *ContextFabricCoverageDetail) { d.Count = intPtr(0) }, "forbids count"},
		{"count", "one", func(d *ContextFabricCoverageDetail) { d.Count = intPtr(1) }, "forbids count"},
		{"count", "negative", func(d *ContextFabricCoverageDetail) { d.Count = intPtr(-1) }, "non-negative"},
		// narrowing
		{"narrowed+skipped_kinds", "present", func(d *ContextFabricCoverageDetail) {
			d.Narrowed, d.SkippedKinds = true, []ContextFabricSubjectKind{ContextFabricSubjectProject}
		}, "forbids narrowing fields"},
		{"skipped_kinds", "without narrowed", func(d *ContextFabricCoverageDetail) {
			d.SkippedKinds = []ContextFabricSubjectKind{ContextFabricSubjectProject}
		}, "forbids narrowing fields"},
		{"supported_kinds", "present", func(d *ContextFabricCoverageDetail) {
			d.SupportedKinds = []ContextFabricSubjectKind{ContextFabricSubjectProject}
		}, "forbids supported_kinds"},
		{"supported_kinds", "empty container", func(d *ContextFabricCoverageDetail) { d.SupportedKinds = []ContextFabricSubjectKind{} }, ""},
		// degrading
		{"degrading", "true", func(d *ContextFabricCoverageDetail) { d.Degrading = true }, "can never degrade"},
		// the common bounds still apply
		{"label", "empty", func(d *ContextFabricCoverageDetail) { d.Label = "" }, "label"},
		{"detail_id", "empty", func(d *ContextFabricCoverageDetail) { d.DetailID = "" }, "detail id"},
		{"source", "empty", func(d *ContextFabricCoverageDetail) { d.Source = " " }, "source"},
		{"raw", "present (non-degrading)", func(d *ContextFabricCoverageDetail) { d.Raw = "health: unavailable" }, ""},
	}
	for _, c := range cells {
		d := originStateDetail()
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

	// EVERY member of each required vocabulary validates, and composes an
	// in-bounds label -- read from the registries, not restated.
	states := make([]ContextFabricSourceState, 0, len(contextFabricSourceStateLabels))
	for state := range contextFabricSourceStateLabels {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i] < states[j] })
	combos := 0
	for _, kind := range ContextFabricFactKindVocabulary() {
		for _, state := range states {
			for _, origin := range ContextFabricSubjectKindVocabulary() {
				d := originStateDetail()
				d.FactKind, d.SourceState, d.OriginKind = kind, state, origin
				d.Source = "canonical_fact:" + string(kind)
				if err := d.Validate(); err != nil {
					t.Errorf("kind=%s state=%s origin=%s: rejected: %v", kind, state, origin, err)
				}
				label := ComposeCoverageDetailLabel(d)
				if strings.TrimSpace(label) == "" || len([]rune(label)) > ContextFabricCoverageDetailLabelMaxLength {
					t.Errorf("kind=%s state=%s origin=%s: label out of bounds: %q", kind, state, origin, label)
				}
				combos++
			}
		}
	}
	t.Logf("vocabulary sweep: %d fact kinds x %d source states x %d origin kinds = %d details validated",
		ContextFabricFactKindCount, len(states), ContextFabricSubjectKindCount, combos)
}

// TestFactReadOriginStateLabelNamesKindOriginAndState pins the label's words,
// with a fixture in which the kind, the origin and the state are all
// distinct tokens, so a label that swapped any two would not match.
func TestFactReadOriginStateLabelNamesKindOriginAndState(t *testing.T) {
	cases := []struct {
		kind   ContextFabricFactKind
		state  ContextFabricSourceState
		origin ContextFabricSubjectKind
		want   string
	}{
		{ContextFabricFactHealth, ContextFabricSourceUnavailable, ContextFabricSubjectTeam,
			"Health facts, read for each team: " + ContextFabricSourceStateLabel(ContextFabricSourceUnavailable)},
		{ContextFabricFactStatus, ContextFabricSourceAvailable, ContextFabricSubjectPullRequest,
			"Status facts, read for each pull request: " + ContextFabricSourceStateLabel(ContextFabricSourceAvailable)},
	}
	for _, c := range cases {
		d := originStateDetail()
		d.FactKind, d.SourceState, d.OriginKind = c.kind, c.state, c.origin
		got := ComposeCoverageDetailLabel(d)
		t.Logf("label: %q", got)
		if got != c.want {
			t.Errorf("label = %q, want %q", got, c.want)
		}
	}
}

// TestFactReadOriginStateClassification pins the two predicates the result
// validator consults: never degrading, never population-qualifying.
func TestFactReadOriginStateClassification(t *testing.T) {
	if coverageDetailCodeMayDegrade(ContextFabricCoverageDetailFactReadOriginState) {
		t.Error("fact_read_origin_state may degrade; it is a disclosure and must never count a gap twice")
	}
	if coverageDetailCodeQualifiesPopulation(ContextFabricCoverageDetailFactReadOriginState) {
		t.Error("fact_read_origin_state qualifies a population; it describes a read, not a value over a population")
	}
}

// TestOriginKindOutsideTheQuartetIsOnlyForTheCodeThatRequiresIt is the
// sibling sweep for the scope check this code changed: every OTHER code keeps
// OriginKind inside the all-or-nothing scope quartet.
func TestOriginKindOutsideTheQuartetIsOnlyForTheCodeThatRequiresIt(t *testing.T) {
	for _, code := range ContextFabricCoverageDetailCodeVocabulary() {
		rule := coverageDetailFieldRules[code]
		d := validDetailForCode(code)
		if rule.requireOriginKind {
			continue
		}
		if rule.requireScope {
			// The quartet code: OriginKind alone (the other three cleared)
			// must still be refused as an incomplete quartet.
			d.ScopeOutcome, d.Policy, d.Basis = "", "", ""
			err := d.Validate()
			t.Logf("CELL code=%s origin-alone -> %v", code, err)
			if err == nil || !strings.Contains(err.Error(), "requires scope_outcome") {
				t.Errorf("%s: origin_kind alone accepted or refused for the wrong reason: %v", code, err)
			}
			continue
		}
		d.OriginKind = ContextFabricSubjectTeam
		err := d.Validate()
		t.Logf("CELL code=%s origin-alone -> %v", code, err)
		if err == nil || !strings.Contains(err.Error(), "forbids scope fields") {
			t.Errorf("%s: origin_kind alone accepted or refused for the wrong reason: %v", code, err)
		}
	}
}
