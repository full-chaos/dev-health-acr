package contextfabric

import (
	"context"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The refusal of a quadrant for teams on different days is said in the served
// answer, with the latest day the teams share offered as the window to ask for.

func daysDifferTeams() []quadrantTeamFixture {
	return []quadrantTeamFixture{
		{"gh:ops-team", "Ops Team", "2026-10-07", 5, 441951, 397},
		{"linear:CHAOS", "Fullchaos", "2026-11-04", 7, 2094394, 2217},
	}
}

// landscapeFactsWithCommonDay is the facts the read gave: each team's landscape
// fact carries the common day, or none when the teams share no day.
func landscapeFactsWithCommonDay(common string, teams ...quadrantTeamFixture) CanonicalFactBundle {
	var bundle CanonicalFactBundle
	for _, team := range teams {
		fields := map[string]FactValue{}
		if common != "" {
			fields["common_live_day"] = StringFactValue(common)
		}
		bundle.Facts = append(bundle.Facts, CanonicalFact{Kind: FactLandscape, Subject: quadrantTeamSubject(team.id, team.label), Fields: fields})
	}
	return bundle
}

func daysDifferEngine(t *testing.T, enabled bool) *Engine {
	t.Helper()
	engine, err := NewEngine(tableEngineDependencies(), EngineOptions{ServiceVersion: "days-differ", NewResultID: resultIDSequence(), LandscapeQuadrantEnabled: enabled})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

func daysDifferLines(limitations []string) []string {
	var found []string
	for _, limitation := range limitations {
		if contractsv1.IsContextFabricLandscapeDaysDifferLimitation(limitation) {
			found = append(found, limitation)
		}
	}
	return found
}

func finalizeDaysDiffer(engine *Engine, result InvestigationResult, facts CanonicalFactBundle) InvestigationResult {
	plan := *result.AnswerPlan
	return engine.finalizeResult(context.Background(), storage.Principal{}, result, plan, nil, facts, &assemblyTelemetry{}, answerPassFirst, MembershipCardinality{})
}

func TestADaysDifferRefusalOffersTheLatestSharedDay(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	finalized := finalizeDaysDiffer(daysDifferEngine(t, true), quadrantResult(teams...), landscapeFactsWithCommonDay("2026-10-07", teams...))
	if quadrantOf(t, finalized.RenderShapes) != nil {
		t.Fatal("a quadrant was drawn for teams on different days")
	}
	lines := daysDifferLines(finalized.Limitations)
	want := contractsv1.ContextFabricLandscapeDaysDifferLimitation([]string{"Ops Team", "Fullchaos"}, []string{"Oct 7, 2026", "Nov 4, 2026"}, "Oct 7, 2026")
	const plain = "No quadrant: the latest data for Ops Team is Oct 7, 2026, and for Fullchaos it is Nov 4, 2026. Ask for a window that ends Oct 7, 2026 to compare them."
	if want != plain {
		t.Fatalf("wording %q; want %q", want, plain)
	}
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("limitations %q; want the one line %q", finalized.Limitations, want)
	}
	for _, part := range []string{"Ops Team", "Oct 7, 2026", "Fullchaos", "Nov 4, 2026", "window that ends Oct 7, 2026"} {
		if !strings.Contains(lines[0], part) {
			t.Errorf("the line does not state %q: %s", part, lines[0])
		}
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(lines[0]) {
		t.Fatal("the line is not service-authored, so a model caveat could displace it")
	}
	if slices.Contains(finalized.Limitations, contractsv1.ContextFabricLandscapePositionLimitation) {
		t.Fatal("the quadrant's line is served without a quadrant")
	}
}

func TestADaysDifferRefusalWithNoSharedDayOffersNoWindow(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	finalized := finalizeDaysDiffer(daysDifferEngine(t, true), quadrantResult(teams...), landscapeFactsWithCommonDay("", teams...))
	lines := daysDifferLines(finalized.Limitations)
	want := contractsv1.ContextFabricLandscapeDaysDifferLimitation([]string{"Ops Team", "Fullchaos"}, []string{"Oct 7, 2026", "Nov 4, 2026"}, "")
	const plain = "No quadrant: the latest data for Ops Team is Oct 7, 2026, and for Fullchaos it is Nov 4, 2026. There is no day on which all of them have data."
	if want != plain {
		t.Fatalf("wording %q; want %q", want, plain)
	}
	if len(lines) != 1 || lines[0] != want || strings.Contains(lines[0], "Ask for a window") {
		t.Fatalf("limitations %q; want the one line %q", finalized.Limitations, want)
	}
}

// A second pass neither repeats the line nor keeps it once the teams are on one day.
func TestTheDaysDifferLineIsReplacedOnEveryPass(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	engine := daysDifferEngine(t, true)
	first := finalizeDaysDiffer(engine, quadrantResult(teams...), landscapeFactsWithCommonDay("2026-10-07", teams...))
	again := quadrantResult(teams...)
	again.Limitations = first.Limitations
	second := finalizeDaysDiffer(engine, again, landscapeFactsWithCommonDay("2026-10-07", teams...))
	if got := daysDifferLines(second.Limitations); len(got) != 1 {
		t.Fatalf("a second pass has %d days-differ lines: %q", len(got), second.Limitations)
	}
	sameDay := quadrantResult(quadrantTeamFixtures()...)
	sameDay.Limitations = first.Limitations
	third := finalizeDaysDiffer(engine, sameDay, CanonicalFactBundle{})
	if got := daysDifferLines(third.Limitations); len(got) != 0 {
		t.Fatalf("teams on one day keep the refusal line: %q", third.Limitations)
	}
	if quadrantOf(t, third.RenderShapes) == nil {
		t.Fatal("teams on one day draw no quadrant")
	}
}

// Check 12: a refusal for another reason is not this refusal.
func TestOnlyADaysDifferRefusalServesTheLine(t *testing.T) {
	t.Parallel()
	teams := quadrantTeamFixtures()
	for name, edit := range map[string]func(*InvestigationResult){
		"teams on one day":     func(*InvestigationResult) {},
		"one team":             func(r *InvestigationResult) { r.SubjectResolution.Committed = r.SubjectResolution.Committed[:1] },
		"no landscape asked":   func(r *InvestigationResult) { r.Interpretation.FactRequirements = []FactRequirement{{Kind: FactFlow}} },
		"a plan with no chart": func(r *InvestigationResult) { r.AnswerPlan.RenderKinds = nil },
		"a project side": func(r *InvestigationResult) {
			r.SubjectResolution.Committed[1].Kind = contractsv1.ContextFabricSubjectProject
		},
	} {
		result := quadrantResult(teams...)
		edit(&result)
		finalized := finalizeDaysDiffer(daysDifferEngine(t, true), result, landscapeFactsWithCommonDay("2026-10-07", daysDifferTeams()...))
		if lines := daysDifferLines(finalized.Limitations); len(lines) != 0 {
			t.Errorf("%s: served %q", name, lines)
		}
	}
}

func TestADisabledQuadrantServesNoDaysDifferLine(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	finalized := finalizeDaysDiffer(daysDifferEngine(t, false), quadrantResult(teams...), landscapeFactsWithCommonDay("2026-10-07", teams...))
	if lines := daysDifferLines(finalized.Limitations); len(lines) != 0 {
		t.Fatalf("flag off: %q", lines)
	}
	on := finalizeDaysDiffer(daysDifferEngine(t, true), quadrantResult(teams...), landscapeFactsWithCommonDay("2026-10-07", teams...))
	stored := WithoutDisabledRenderShapes(on, RenderShapeOptions{InvestmentThemeShare: true, ComparisonSideBySide: true})
	if lines := daysDifferLines(stored.Limitations); len(lines) != 0 {
		t.Fatalf("a stored answer read with the flag off keeps %q", lines)
	}
	kept := WithoutDisabledRenderShapes(on, RenderShapeOptions{LandscapePosition: true})
	if lines := daysDifferLines(kept.Limitations); len(lines) != 1 {
		t.Fatalf("a stored answer read with the flag on lost the line: %q", kept.Limitations)
	}
}

func TestTheDaysDifferSentenceIsRecognisedOnlyWhole(t *testing.T) {
	t.Parallel()
	offer := contractsv1.ContextFabricLandscapeDaysDifferLimitation([]string{"A", "B"}, []string{"Oct 7, 2026", "Nov 4, 2026"}, "Oct 7, 2026")
	none := contractsv1.ContextFabricLandscapeDaysDifferLimitation([]string{"A", "B"}, []string{"Oct 7, 2026", "Nov 4, 2026"}, "")
	three := contractsv1.ContextFabricLandscapeDaysDifferLimitation([]string{"A", "B", "C"}, []string{"Oct 7, 2026", "Nov 4, 2026", "Nov 2, 2026"}, "Oct 7, 2026")
	if want := "No quadrant: the latest data for A is Oct 7, 2026, for B it is Nov 4, 2026, and for C it is Nov 2, 2026. Ask for a window that ends Oct 7, 2026 to compare them."; three != want {
		t.Fatalf("three teams: %q; want %q", three, want)
	}
	for _, sentence := range []string{offer, none, three} {
		if !contractsv1.IsContextFabricLandscapeDaysDifferLimitation(sentence) {
			t.Errorf("not recognised: %q", sentence)
		}
	}
	for _, sentence := range []string{"", "No quadrant: the latest data for A is Oct 7, 2026.", "No quadrant is drawn.", "The quadrant shows where each team stood", offer + " Extra.", strings.TrimSuffix(offer, ".")} {
		if contractsv1.IsContextFabricLandscapeDaysDifferLimitation(sentence) {
			t.Errorf("recognised: %q", sentence)
		}
	}
}

// An offer needs every placed team's fact to name the same day: two days, or a
// team whose fact names none, offer nothing (check 12: an unread day is not a day).
func TestADaysDifferRefusalOffersNoWindowTheFactsDoNotAgreeOn(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	disagree := landscapeFactsWithCommonDay("2026-10-07", teams...)
	disagree.Facts[1].Fields["common_live_day"] = StringFactValue("2026-10-06")
	missing := landscapeFactsWithCommonDay("2026-10-07", teams...)
	delete(missing.Facts[1].Fields, "common_live_day")
	onlyOne := landscapeFactsWithCommonDay("2026-10-07", teams[:1]...)
	for name, facts := range map[string]CanonicalFactBundle{"two days": disagree, "one team names none": missing, "one fact": onlyOne} {
		finalized := finalizeDaysDiffer(daysDifferEngine(t, true), quadrantResult(teams...), facts)
		lines := daysDifferLines(finalized.Limitations)
		if len(lines) != 1 || strings.Contains(lines[0], "Ask for a window") {
			t.Errorf("%s: %q", name, lines)
		}
	}
}

// The day the read offers and the quadrant's own person floor are one number.
func TestTheOfferedDayUsesTheQuadrantsPersonFloor(t *testing.T) {
	t.Parallel()
	floor := int64(LandscapeMinIdentities)
	day := "2026-11-02"
	at := quadrantResult(quadrantTeamFixture{"gh:ops-team", "Ops", day, floor, 900, 12}, quadrantTeamFixture{"linear:CHAOS", "Chaos", day, floor, 2400, 31})
	if shapes, _ := selectRenderShapes(at, nil, quadrantOn); quadrantOf(t, shapes) == nil {
		t.Fatalf("teams of exactly %d identities draw no quadrant", floor)
	}
	below := quadrantResult(quadrantTeamFixture{"gh:ops-team", "Ops", day, floor - 1, 900, 12}, quadrantTeamFixture{"linear:CHAOS", "Chaos", day, floor, 2400, 31})
	shapes, event := selectRenderShapes(below, nil, quadrantOn)
	if quadrantOf(t, shapes) != nil || quadrantSkipOf(t, event) != RenderShapeSkipFewerThanTwoPoints {
		t.Fatalf("a team of %d identities is placed", floor-1)
	}
}
