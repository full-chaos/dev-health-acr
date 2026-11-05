package contextfabric

import (
	"context"
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-13009: the refused quadrant's offer is a window the user picks, not a
// sentence to retype. It is the window option the answer already knows how to
// serve (frozen bounds, a winr_ receipt), shown beside the answer by structure_needs.

func landscapeOfferOptions(result InvestigationResult) []contractsv1.ContextFabricWindowOption {
	var found []contractsv1.ContextFabricWindowOption
	if result.WindowClarification != nil {
		for _, option := range result.WindowClarification.Options {
			if contractsv1.IsContextFabricLandscapeWindowOptionLabel(option.Label) {
				found = append(found, option)
			}
		}
	}
	return found
}

func TestARefusedQuadrantOffersTheSharedDayAsAWindowOption(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	finalized := finalizeDaysDiffer(daysDifferEngine(t, true), quadrantResult(teams...), landscapeFactsWithCommonDay("2026-10-07", teams...))
	options := landscapeOfferOptions(finalized)
	if len(options) != 1 {
		t.Fatalf("%d landscape window options, want 1: %+v", len(options), finalized.WindowClarification)
	}
	option := options[0]
	if err := option.Validate(); err != nil {
		t.Fatalf("the option does not validate: %v", err)
	}
	if option.Label != contractsv1.ContextFabricLandscapeWindowOptionLabel("Oct 7, 2026") || option.RelativeID != "" {
		t.Fatalf("label %q relative %q", option.Label, option.RelativeID)
	}
	// Frozen bounds: the window ends the day after the shared day (exclusive), so
	// a dated read takes each team's latest row inside it, which is the shared day.
	wantEnd := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	if option.End == nil || !option.End.Equal(wantEnd) || option.Start == nil || !option.Start.Before(*option.End) {
		t.Fatalf("bounds %v .. %v", option.Start, option.End)
	}
	if err := finalized.WindowClarification.Validate(); err != nil {
		t.Fatalf("the clarification does not validate: %v", err)
	}
	needs := finalized.StructureNeeds
	if needs == nil || !slices.Contains(needs.Missing, contractsv1.ContextFabricStructureNeedWindow) {
		t.Fatalf("structure_needs %+v does not show the window choice", needs)
	}
	if !slices.ContainsFunc(needs.WindowOptions, func(each contractsv1.ContextFabricWindowOption) bool { return each.ReceiptID == option.ReceiptID }) {
		t.Fatalf("structure_needs shows %+v, not the offered receipt %s", needs.WindowOptions, option.ReceiptID)
	}
}

func TestTheOfferedWindowIsAsLongAsTheWindowTheTurnRead(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	result := quadrantResult(teams...)
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	result.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end, RelativeID: contractsv1.ContextFabricRelativeWindowTrailing30D, Provenance: WindowInferredDefault}
	// Assembly composed the relative window offers before finalization.
	result.WindowClarification = composeWindowClarification(result.EffectiveEvidenceWindow, result.ResultID, end)
	result.StructureNeeds = answeredWindowStructureNeeds(result.WindowClarification)
	relative := len(result.WindowClarification.Options)
	finalized := finalizeDaysDiffer(daysDifferEngine(t, true), result, landscapeFactsWithCommonDay("2026-10-07", teams...))
	options := landscapeOfferOptions(finalized)
	if len(options) != 1 {
		t.Fatalf("%d options", len(options))
	}
	if got, want := options[0].End.Sub(*options[0].Start), end.Sub(start); got != want {
		t.Fatalf("offered window is %v long, the turn read %v", got, want)
	}
	if len(finalized.WindowClarification.Options) != relative+1 || len(finalized.StructureNeeds.WindowOptions) != relative+1 {
		t.Fatalf("the relative window offers were not kept beside ours: clarification %d, structure_needs %d, relative %d", len(finalized.WindowClarification.Options), len(finalized.StructureNeeds.WindowOptions), relative)
	}
}

func TestNoWindowIsOfferedWhenThereIsNoSharedDayOrNoRefusal(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	for name, tc := range map[string]struct {
		engine bool
		result InvestigationResult
		facts  CanonicalFactBundle
	}{
		"no shared day":    {true, quadrantResult(teams...), landscapeFactsWithCommonDay("", teams...)},
		"the rule is off":  {false, quadrantResult(teams...), landscapeFactsWithCommonDay("2026-10-07", teams...)},
		"teams on one day": {true, quadrantResult(quadrantTeamFixtures()...), landscapeFactsWithCommonDay("2026-10-07", teams...)},
		"the facts disagree": {true, quadrantResult(teams...), func() CanonicalFactBundle {
			facts := landscapeFactsWithCommonDay("2026-10-07", teams...)
			facts.Facts[1].Fields["common_live_day"] = StringFactValue("2026-10-06")
			return facts
		}()},
	} {
		finalized := finalizeDaysDiffer(daysDifferEngine(t, tc.engine), tc.result, tc.facts)
		if options := landscapeOfferOptions(finalized); len(options) != 0 {
			t.Errorf("%s: offered %+v", name, options)
		}
	}
}

// A second pass replaces the option, a pass with no refusal removes it, and a
// window choice that was not ours stays.
func TestTheLandscapeWindowOptionIsReplacedOnEveryPass(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	engine := daysDifferEngine(t, true)
	first := finalizeDaysDiffer(engine, quadrantResult(teams...), landscapeFactsWithCommonDay("2026-10-07", teams...))
	again := quadrantResult(teams...)
	again.ResultID = first.ResultID
	again.WindowClarification, again.StructureNeeds = first.WindowClarification, first.StructureNeeds
	second := finalizeDaysDiffer(engine, again, landscapeFactsWithCommonDay("2026-10-07", teams...))
	if options := landscapeOfferOptions(second); len(options) != 1 {
		t.Fatalf("a second pass has %d options", len(options))
	}
	if second.StructureNeeds == nil || len(second.StructureNeeds.WindowOptions) != 1 {
		t.Fatalf("structure_needs lists %+v after a second pass", second.StructureNeeds)
	}
	sameDay := quadrantResult(quadrantTeamFixtures()...)
	sameDay.WindowClarification, sameDay.StructureNeeds = first.WindowClarification, first.StructureNeeds
	third := finalizeDaysDiffer(engine, sameDay, CanonicalFactBundle{})
	if options := landscapeOfferOptions(third); len(options) != 0 || third.WindowClarification != nil || third.StructureNeeds != nil {
		t.Fatalf("teams on one day keep the offer: %+v %+v", third.WindowClarification, third.StructureNeeds)
	}
}

// The offered receipt is redeemable on the next turn: it becomes that turn's
// evidence window, with the frozen bounds the option carried, and the fact read
// is asked for that window.
func TestTheOfferedLandscapeWindowIsRedeemedAsTheTurnsEvidenceWindow(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	finalized := finalizeDaysDiffer(daysDifferEngine(t, true), quadrantResult(teams...), landscapeFactsWithCommonDay("2026-10-07", teams...))
	options := landscapeOfferOptions(finalized)
	if len(options) != 1 {
		t.Fatalf("%d options", len(options))
	}
	prior := validInvestigationResult()
	prior.ResultID = finalized.ResultID
	prior.WindowClarification, prior.StructureNeeds = finalized.WindowClarification, finalized.StructureNeeds
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	engine, probe := mustHistoricalEngineWithStore(t, TimeContext{Axis: TemporalCurrent}, now, store)
	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: options[0].ReceiptID}}
	request.ParentResultID = prior.ResultID
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	window := probe.factContext.EvidenceWindow
	if window == nil || window.Start == nil || window.End == nil || !window.Start.Equal(*options[0].Start) || !window.End.Equal(*options[0].End) {
		t.Fatalf("the fact read was asked for %+v, want the offered bounds %v .. %v", window, options[0].Start, options[0].End)
	}
}

// A stored answer read with the quadrant rule off loses the offer with the line.
func TestADisabledQuadrantLeavesNoLandscapeWindowOffer(t *testing.T) {
	t.Parallel()
	teams := daysDifferTeams()
	on := finalizeDaysDiffer(daysDifferEngine(t, true), quadrantResult(teams...), landscapeFactsWithCommonDay("2026-10-07", teams...))
	if len(landscapeOfferOptions(on)) != 1 {
		t.Fatal("premise: the offer is served with the rule on")
	}
	off := WithoutDisabledRenderShapes(on, RenderShapeOptions{InvestmentThemeShare: true, ComparisonSideBySide: true})
	if options := landscapeOfferOptions(off); len(options) != 0 || off.WindowClarification != nil || off.StructureNeeds != nil {
		t.Fatalf("rule off: %+v %+v", off.WindowClarification, off.StructureNeeds)
	}
	kept := WithoutDisabledRenderShapes(on, RenderShapeOptions{LandscapePosition: true})
	if len(landscapeOfferOptions(kept)) != 1 {
		t.Fatal("rule on: the stored offer was removed")
	}
}
