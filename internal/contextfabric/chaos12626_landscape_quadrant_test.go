package contextfabric

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12626: the rule that yields the landscape_position quadrant. The result
// here is the shape a served comparison of teams has: an explicit_comparison
// plan that authorizes a quadrant, a question that asks for landscape facts, the
// committed teams, and each team's claims on its landscape fact, which carry the
// fact's one area_breakdown row.

type quadrantTeamFixture struct {
	id, label, day           string
	identities, churn, units int64
}

func quadrantTeamSubject(id, label string) SubjectRef {
	return SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: id, Label: label}
}

// quadrantClaimsOf is one team's two claims, on one fact, each carrying the
// fact's one row, as attachCanonicalRows gives them.
func quadrantClaimsOf(team quadrantTeamFixture) []ClaimedFact {
	subject := quadrantTeamSubject(team.id, team.label)
	day := team.day
	row := contractsv1.ContextFabricClaimedFactRow{Fields: map[string]ScalarValue{
		"as_of_day": {String: &day}, "identity_count": tableInteger(team.identities),
		"churn_loc_30d": tableInteger(team.churn), "delivery_units_30d": tableInteger(team.units),
	}}
	table := &contractsv1.ContextFabricClaimedFactTable{
		Field: "area_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"as_of_day"},
		Measures: []string{"identity_count", "churn_loc_30d", "delivery_units_30d"},
	}
	name := strings.ReplaceAll(team.id, ":", "_")
	claim := func(field string, value int64) ClaimedFact {
		return ClaimedFact{
			ClaimID: "claim_" + name + "_" + field, FactID: "fact_landscape_" + name, Kind: FactLandscape, Subject: subject,
			Field: field, Value: tableInteger(value), Rows: []contractsv1.ContextFabricClaimedFactRow{row}, Table: table,
		}
	}
	return []ClaimedFact{claim("churn_loc_30d", team.churn), claim("delivery_units_30d", team.units)}
}

func quadrantTeamFixtures() []quadrantTeamFixture {
	return []quadrantTeamFixture{
		{"linear:CHAOS", "Chaos", "2026-11-02", 5, 2400, 31},
		{"gh:ops-team", "Ops", "2026-11-02", 3, 900, 12},
		{"gl:full.chaos", "Full Chaos", "2026-11-02", 2, 0, 4},
	}
}

// quadrantResult compares the teams on landscape facts.
func quadrantResult(teams ...quadrantTeamFixture) InvestigationResult {
	result := validInvestigationResult()
	result.Cohort = nil
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        QuestionFamilyExplicitComparison,
		FamilyVersion: QuestionFamilyTableVersion,
		RenderKinds:   []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindTable, contractsv1.ContextFabricRenderKindQuadrant},
	}
	result.Interpretation.FactRequirements = []FactRequirement{{Kind: FactLandscape}}
	result.SubjectResolution.Committed = nil
	result.ClaimedFacts = nil
	for _, team := range teams {
		result.SubjectResolution.Committed = append(result.SubjectResolution.Committed, quadrantTeamSubject(team.id, team.label))
		result.ClaimedFacts = append(result.ClaimedFacts, quadrantClaimsOf(team)...)
	}
	return result
}

var quadrantOn = RenderShapeOptions{LandscapePosition: true}

func quadrantOf(t *testing.T, shapes []contractsv1.ContextFabricRenderShape) *contractsv1.ContextFabricRenderShape {
	t.Helper()
	var found *contractsv1.ContextFabricRenderShape
	for index := range shapes {
		if shapes[index].SelectedBy == contractsv1.ContextFabricRenderRuleLandscapePosition {
			if found != nil {
				t.Fatal("two quadrants")
			}
			found = &shapes[index]
		}
	}
	return found
}

func quadrantSkipOf(t *testing.T, event RenderShapeSelectionEvent) RenderShapeSkipReason {
	t.Helper()
	if err := event.Accounted(); err != nil {
		t.Fatalf("Accounted: %v", err)
	}
	for _, skip := range event.Skipped {
		if skip.Rule == contractsv1.ContextFabricRenderRuleLandscapePosition {
			return skip.Reason
		}
	}
	return ""
}

func TestTeamsOnOneDayGetTheirQuadrant(t *testing.T) {
	t.Parallel()
	result := quadrantResult(quadrantTeamFixtures()...)
	shapes, event := selectRenderShapes(result, nil, quadrantOn)
	quadrant := quadrantOf(t, shapes)
	if quadrant == nil {
		t.Fatalf("no quadrant; skip = %q", quadrantSkipOf(t, event))
	}
	if event.LandscapeSidesLeftOut != 0 {
		t.Fatalf("left out %d, want 0", event.LandscapeSidesLeftOut)
	}
	xWord, yWord := tableWord(t, "churn_loc_30d"), tableWord(t, "delivery_units_30d")
	if quadrant.Kind != contractsv1.ContextFabricRenderKindQuadrant || quadrant.AxisKind != contractsv1.ContextFabricRenderAxisCategory || quadrant.ValueUnit != "" ||
		quadrant.AxisLabel != xWord || quadrant.ValueLabel != yWord || quadrant.Title != "Churn and delivery units, 30 days to Nov 2, 2026" {
		t.Fatalf("shape %+v", quadrant)
	}
	if len(quadrant.Series) != 2 || quadrant.Series[0].Key != "churn_loc_30d" || quadrant.Series[0].Label != xWord ||
		quadrant.Series[1].Key != "delivery_units_30d" || quadrant.Series[1].Label != yWord {
		t.Fatalf("series %+v", quadrant.Series)
	}
	// Committed order, which is the order of the question's operands; each value
	// the team claim's own, citing it.
	want := []struct {
		label        string
		churn, units float64
	}{{"Chaos", 2400, 31}, {"Ops", 900, 12}, {"Full Chaos", 0, 4}}
	for index, team := range want {
		x, y := quadrant.Series[0].Points[index], quadrant.Series[1].Points[index]
		if x.Label != team.label || y.Label != team.label || x.Value != team.churn || y.Value != team.units {
			t.Fatalf("point %d = %+v / %+v, want %+v", index, x, y, team)
		}
		if x.Source.Kind != contractsv1.ContextFabricRenderSourceClaimedFactValue || x.Source.Field != "churn_loc_30d" ||
			y.Source.Field != "delivery_units_30d" || !strings.HasSuffix(x.Source.ClaimID, "_churn_loc_30d") {
			t.Fatalf("point %d sources %+v / %+v", index, x.Source, y.Source)
		}
	}
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the served quadrant does not validate: %v", err)
	}
}

func TestTheQuadrantIsOffWithItsFlag(t *testing.T) {
	t.Parallel()
	shapes, event := selectRenderShapes(quadrantResult(quadrantTeamFixtures()...), nil, RenderShapeOptions{})
	if quadrantOf(t, shapes) != nil || quadrantSkipOf(t, event) != RenderShapeSkipRuleDisabled {
		t.Fatalf("flag off: skip %q", quadrantSkipOf(t, event))
	}
}

// Check 10: a quadrant answers only a comparison of teams that asks for landscape
// facts. Each case plants what one clause exists to catch.
func TestTheQuadrantIsSkipped(t *testing.T) {
	t.Parallel()
	teams := quadrantTeamFixtures
	for name, tc := range map[string]struct {
		edit    func(*InvestigationResult)
		want    RenderShapeSkipReason
		leftOut int
	}{
		"a plain comparison of other measures": {func(r *InvestigationResult) {
			r.Interpretation.FactRequirements = []FactRequirement{{Kind: FactFlow}, {Kind: FactMetrics}}
		}, RenderShapeSkipNoLandscapeRequirement, 0},
		"a status answer that reads landscape by composition": {func(r *InvestigationResult) {
			r.Interpretation.FactRequirements = []FactRequirement{{Kind: FactStatus}}
			r.AnswerPlan.FactKinds = []FactKind{FactHealth, FactLandscape}
		}, RenderShapeSkipNoLandscapeRequirement, 0},
		"a plan with no quadrant": {func(r *InvestigationResult) {
			r.AnswerPlan.RenderKinds = []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindTable}
		}, RenderShapeSkipNotPlanAuthorized, 0},
		"no plan": {func(r *InvestigationResult) { r.AnswerPlan = nil }, RenderShapeSkipNotPlanAuthorized, 0},
		"one team": {func(r *InvestigationResult) {
			r.SubjectResolution.Committed = r.SubjectResolution.Committed[:1]
		}, RenderShapeSkipFewerThanTwoSides, 0},
		"a project side": {func(r *InvestigationResult) {
			r.SubjectResolution.Committed[1].Kind = contractsv1.ContextFabricSubjectProject
		}, RenderShapeSkipSubjectKindNotCharted, 0},
		"a repository side": {func(r *InvestigationResult) {
			r.SubjectResolution.Committed[0].Kind = contractsv1.ContextFabricSubjectRepository
		}, RenderShapeSkipSubjectKindNotCharted, 0},
		"a team on another day": {func(r *InvestigationResult) {
			*r = quadrantResult(append(teams()[:2], quadrantTeamFixture{"gl:full.chaos", "Full Chaos", "2026-07-12", 2, 0, 4})...)
		}, RenderShapeSkipLandscapeDaysDiffer, 0},
		"two teams, one of one identity": {func(r *InvestigationResult) {
			*r = quadrantResult(teams()[0], quadrantTeamFixture{"gh:ops-team", "Ops", "2026-11-02", 1, 900, 12})
		}, RenderShapeSkipFewerThanTwoPoints, 1},
		// Two teams can share a word, and a point's label is the team's label, so
		// the chart cannot tell them apart: the contract refuses it.
		"two teams of one word": {func(r *InvestigationResult) {
			*r = quadrantResult(teams()[0], quadrantTeamFixture{"gh:ops-team", "Chaos", "2026-11-02", 3, 900, 12})
		}, RenderShapeSkipQuadrantUnprovable, 0},
		"more teams than a series holds": {func(r *InvestigationResult) {
			var many []quadrantTeamFixture
			for index := range contractsv1.ContextFabricRenderPointsMaxCount + 1 {
				many = append(many, quadrantTeamFixture{fmt.Sprintf("gh:team-%d", index), fmt.Sprintf("Team %d", index), "2026-11-02", 2, 10, 1})
			}
			*r = quadrantResult(many...)
		}, RenderShapeSkipTooManySides, 0},
		"a team label past the render bound": {func(r *InvestigationResult) {
			long := strings.Repeat("x", contractsv1.ContextFabricRenderLabelMaxLength+1)
			*r = quadrantResult(teams()[0], quadrantTeamFixture{"gh:ops-team", long, "2026-11-02", 3, 900, 12})
		}, RenderShapeSkipQuadrantUnprovable, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := quadrantResult(teams()...)
			tc.edit(&result)
			shapes, event := selectRenderShapes(result, nil, quadrantOn)
			if quadrantOf(t, shapes) != nil {
				t.Fatal("a quadrant was drawn")
			}
			if got := quadrantSkipOf(t, event); got != tc.want || event.LandscapeSidesLeftOut != tc.leftOut {
				t.Fatalf("skip %q, left out %d; want %q, %d", got, event.LandscapeSidesLeftOut, tc.want, tc.leftOut)
			}
		})
	}
}

// A side that cannot give a point is left out and counted, never drawn as zero
// (check 12); the other teams still get their quadrant.
func TestASideWithNoPointIsLeftOut(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*InvestigationResult){
		"one identity": func(r *InvestigationResult) {
			*r = quadrantResult(append(quadrantTeamFixtures()[:2], quadrantTeamFixture{"gl:full.chaos", "Full Chaos", "2026-11-02", 1, 0, 4})...)
		},
		"no y claim": func(r *InvestigationResult) { r.ClaimedFacts = r.ClaimedFacts[:5] },
		"no x claim": func(r *InvestigationResult) { r.ClaimedFacts = slices.Delete(r.ClaimedFacts, 4, 5) },
		"two x claims": func(r *InvestigationResult) {
			second := r.ClaimedFacts[4]
			second.ClaimID += "_again"
			r.ClaimedFacts = append(r.ClaimedFacts, second)
		},
		"two y claims": func(r *InvestigationResult) {
			second := r.ClaimedFacts[5]
			second.ClaimID += "_again"
			r.ClaimedFacts = append(r.ClaimedFacts, second)
		},
		"x and y of two facts": func(r *InvestigationResult) { r.ClaimedFacts[5].FactID = "fact_landscape_other" },
		"an x value that is not the row's": func(r *InvestigationResult) {
			r.ClaimedFacts[4].Value = tableInteger(7)
		},
		"a value that is not the row's": func(r *InvestigationResult) {
			r.ClaimedFacts[5].Value = tableInteger(5)
		},
		"a number value": func(r *InvestigationResult) { r.ClaimedFacts[5].Value = tableNumber(4) },
		"a derived claim": func(r *InvestigationResult) {
			r.ClaimedFacts[4].DerivedFrom = []contractsv1.ContextFabricDerivationInput{{FactID: "fact_one", Field: "churn_loc_30d"}}
		},
		"a claim on another kind": func(r *InvestigationResult) {
			r.ClaimedFacts[4].Kind = FactFlow
		},
		"no row": func(r *InvestigationResult) {
			r.ClaimedFacts[4].Rows, r.ClaimedFacts[5].Rows = nil, nil
		},
		"another table": func(r *InvestigationResult) {
			for _, index := range []int{4, 5} {
				table := *r.ClaimedFacts[index].Table
				table.Field = "map_coordinates"
				r.ClaimedFacts[index].Table = &table
			}
		},
		"a table keyed otherwise": func(r *InvestigationResult) {
			for _, index := range []int{4, 5} {
				table := *r.ClaimedFacts[index].Table
				table.Key = []string{"team_id"}
				r.ClaimedFacts[index].Table = &table
			}
		},
		"a table keyed by two columns": func(r *InvestigationResult) {
			for _, index := range []int{4, 5} {
				table := *r.ClaimedFacts[index].Table
				table.Key = []string{"as_of_day", "team_id"}
				r.ClaimedFacts[index].Table = &table
			}
		},
		"two rows": func(r *InvestigationResult) {
			rows := append(slices.Clone(r.ClaimedFacts[4].Rows), r.ClaimedFacts[4].Rows[0])
			r.ClaimedFacts[4].Rows, r.ClaimedFacts[5].Rows = rows, rows
		},
		"a row with an empty day": func(r *InvestigationResult) {
			empty := ""
			r.ClaimedFacts[4].Rows[0].Fields["as_of_day"] = ScalarValue{String: &empty}
		},
		"a number cell in the row": func(r *InvestigationResult) {
			r.ClaimedFacts[4].Rows[0].Fields["churn_loc_30d"] = tableNumber(0)
		},
		"a row with no identity count": func(r *InvestigationResult) {
			fields := map[string]ScalarValue{}
			for name, cell := range r.ClaimedFacts[4].Rows[0].Fields {
				if name != "identity_count" {
					fields[name] = cell
				}
			}
			row := []contractsv1.ContextFabricClaimedFactRow{{Fields: fields}}
			r.ClaimedFacts[4].Rows, r.ClaimedFacts[5].Rows = row, row
		},
		"a table that does not measure delivery units": func(r *InvestigationResult) {
			for _, index := range []int{4, 5} {
				table := *r.ClaimedFacts[index].Table
				table.Measures = []string{"identity_count", "churn_loc_30d"}
				r.ClaimedFacts[index].Table = &table
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := quadrantResult(quadrantTeamFixtures()...)
			edit(&result)
			shapes, event := selectRenderShapes(result, nil, quadrantOn)
			quadrant := quadrantOf(t, shapes)
			if quadrant == nil {
				t.Fatalf("no quadrant; skip %q", quadrantSkipOf(t, event))
			}
			if event.LandscapeSidesLeftOut != 1 || len(quadrant.Series[0].Points) != 2 || len(quadrant.Series[1].Points) != 2 {
				t.Fatalf("left out %d, points %d/%d; want the third team left out", event.LandscapeSidesLeftOut, len(quadrant.Series[0].Points), len(quadrant.Series[1].Points))
			}
			for _, point := range quadrant.Series[0].Points {
				if point.Label == "Full Chaos" {
					t.Fatal("the team with no point was drawn")
				}
			}
		})
	}
}

// A day that is not a date is stated as stored.
func TestTheQuadrantTitleStatesTheDay(t *testing.T) {
	t.Parallel()
	for day, want := range map[string]string{"2026-11-02": "Nov 2, 2026", "2026-07-12": "Jul 12, 2026", "not-a-date": "not-a-date"} {
		if got := landscapeDayWords(day); got != want {
			t.Errorf("landscapeDayWords(%q) = %q, want %q", day, got, want)
		}
	}
}

// The quadrant's served line goes with the quadrant, on every pass.
func TestTheQuadrantLineGoesWithTheQuadrant(t *testing.T) {
	t.Parallel()
	line := contractsv1.ContextFabricLandscapePositionLimitation
	shapes, _ := selectRenderShapes(quadrantResult(quadrantTeamFixtures()...), nil, quadrantOn)
	limitations, displaced := withLandscapeLimitation([]string{"A model caveat."}, shapes)
	if displaced != 0 || !slices.Equal(limitations, []string{"A model caveat.", line}) {
		t.Fatalf("drawn: %v (displaced %d)", limitations, displaced)
	}
	again, _ := withLandscapeLimitation(limitations, shapes)
	if !slices.Equal(again, limitations) {
		t.Fatalf("a second pass changed the line: %v", again)
	}
	gone, _ := withLandscapeLimitation(limitations, nil)
	if !slices.Equal(gone, []string{"A model caveat."}) {
		t.Fatalf("a pass with no quadrant kept the line: %v", gone)
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(line) {
		t.Fatal("the line is not service-authored, so a model caveat could displace it")
	}
}

// Both doors remove a stored quadrant and its line when the flag is off.
func TestADisabledQuadrantLeavesTheServedAnswer(t *testing.T) {
	t.Parallel()
	result := quadrantResult(quadrantTeamFixtures()...)
	shapes, _ := selectRenderShapes(result, nil, quadrantOn)
	result.RenderShapes = shapes
	result.Limitations, _ = withLandscapeLimitation([]string{"A model caveat."}, shapes)
	off := WithoutDisabledRenderShapes(result, RenderShapeOptions{InvestmentThemeShare: true, ComparisonSideBySide: true})
	if quadrantOf(t, off.RenderShapes) != nil || slices.Contains(off.Limitations, contractsv1.ContextFabricLandscapePositionLimitation) {
		t.Fatalf("flag off: shapes %d, limitations %v", len(off.RenderShapes), off.Limitations)
	}
	if !slices.Contains(off.Limitations, "A model caveat.") {
		t.Fatalf("flag off removed another line: %v", off.Limitations)
	}
	on := WithoutDisabledRenderShapes(result, RenderShapeOptions{LandscapePosition: true})
	if quadrantOf(t, on.RenderShapes) == nil || !slices.Contains(on.Limitations, contractsv1.ContextFabricLandscapePositionLimitation) {
		t.Fatalf("flag on: shapes %d, limitations %v", len(on.RenderShapes), on.Limitations)
	}
}

// Every reason the rule can skip with is a value its log line may carry.
func TestEveryQuadrantSkipReasonIsInTheLineVocabulary(t *testing.T) {
	t.Parallel()
	vocabulary := RenderShapeSkipReasonLineVocabulary()
	for _, reason := range []RenderShapeSkipReason{
		RenderShapeSkipRuleDisabled, RenderShapeSkipNotPlanAuthorized, RenderShapeSkipNoLandscapeRequirement,
		RenderShapeSkipFewerThanTwoSides, RenderShapeSkipTooManySides, RenderShapeSkipSubjectKindNotCharted,
		RenderShapeSkipFewerThanTwoPoints, RenderShapeSkipLandscapeDaysDiffer, RenderShapeSkipQuadrantUnprovable,
	} {
		if !slices.Contains(vocabulary, string(reason)) {
			t.Errorf("skip reason %q is not in the line vocabulary", reason)
		}
	}
	if !slices.Contains(RenderShapeRuleLineVocabulary(), string(contractsv1.ContextFabricRenderRuleLandscapePosition)) {
		t.Error("the rule is not in the line vocabulary")
	}
}
