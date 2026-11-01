package contextfabric

import (
	"slices"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12473: the legend of the cohort driver chart showed the signal token
// ("Health.compounding risk") while the same result served "health risk" for it
// under ranking_signal. ask-dev draws series.label as served, so the word is
// acr's to write.

// driverSeriesAnswer is chrisTeamsAnswer with one driver for each signal, and the
// shapes the real selector draws for it.
func driverSeriesAnswer(t *testing.T, signals []string) (InvestigationResult, *contractsv1.ContextFabricRenderShape) {
	t.Helper()
	result := chrisTeamsAnswer()
	drivers := make([]contractsv1.ContextFabricCohortMemberDriver, 0, len(signals))
	for _, signal := range signals {
		drivers = append(drivers, contractsv1.ContextFabricCohortMemberDriver{Signal: signal, Value: 0.5, Weight: 20, WeightContributed: 10})
	}
	result.Cohort.Members[0].Drivers = drivers
	result.Cohort.Members[0].Score = renderFloat(10 * float64(len(signals)))
	shapes, _ := SelectRenderShapes(result, frameForRenderFixture(result))
	stack := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleCohortDriverContribution)
	if stack == nil {
		t.Fatal("the selector drew no driver contribution shape")
	}
	if len(stack.Series) != len(signals) {
		t.Fatalf("%d series, want one for each of %d signals", len(stack.Series), len(signals))
	}
	result.RenderShapes = shapes
	return result, stack
}

func rankingSignals() []string {
	signals := make([]string, 0, len(cohortSignalDriverCategory))
	for signal := range cohortSignalDriverCategory {
		signals = append(signals, signal)
	}
	sort.Strings(signals)
	return signals
}

func TestADriverSeriesReadsTheServedRankingSignalWord(t *testing.T) {
	t.Parallel()
	result, stack := driverSeriesAnswer(t, rankingSignals())
	served, _ := ServeVocabularyLabels(result, true, 1<<20)
	for _, series := range stack.Series {
		word := served.VocabularyLabels[contractsv1.ContextFabricVocabularyLabelKey(contractsv1.ContextFabricVocabularyRankingSignal, series.Key)]
		if word == "" {
			t.Fatalf("%s: no ranking_signal word is served for it", series.Key)
		}
		if !strings.EqualFold(series.Label, word) {
			t.Errorf("%s: legend %q, want the word the same result serves, %q", series.Key, series.Label, word)
		}
		if strings.Contains(series.Label, ".") {
			t.Errorf("%s: legend %q carries the token", series.Key, series.Label)
		}
	}
}

func TestADriverSeriesWithNoWordKeepsAGenericLabel(t *testing.T) {
	t.Parallel()
	_, stack := driverSeriesAnswer(t, []string{"custom.unseen_signal"})
	if got := stack.Series[0].Label; got != "Custom unseen signal" {
		t.Fatalf("legend %q: a signal with no word gets a generic label and no dotted token", got)
	}
}

// A result is saved with its render shapes, so a row written before this change
// holds the raw label. Every serve door stamps, with the labels flag on or off:
// series.label is on every pin, so the word needs no gate.
func TestAStoredDriverSeriesIsRestatedAtServe(t *testing.T) {
	t.Parallel()
	const raw = "Health.compounding risk"
	result, stack := driverSeriesAnswer(t, []string{"health.compounding_risk"})
	stack.Series[0].Label = raw
	other := contractsv1.ContextFabricRenderShape{SelectedBy: contractsv1.ContextFabricRenderRuleCohortAttentionScore,
		Series: []contractsv1.ContextFabricRenderSeries{{Key: "attention_score", Label: "Attention. score"}}}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{*stack, other}
	for _, enabled := range []bool{true, false} {
		served, _ := ServeVocabularyLabels(result, enabled, 1<<20)
		restated := shapeByRule(served.RenderShapes, contractsv1.ContextFabricRenderRuleCohortDriverContribution)
		if got := restated.Series[0].Label; got != "Health risk" {
			t.Errorf("flag %v: legend %q, want the word", enabled, got)
		}
		if got := shapeByRule(served.RenderShapes, contractsv1.ContextFabricRenderRuleCohortAttentionScore).Series[0].Label; got != "Attention. score" {
			t.Errorf("flag %v: another shape's label became %q", enabled, got)
		}
	}
	// Past the bound the door falls back to a document with no stamps. The legend
	// stays the word there too, and that document is what a refusal measures.
	tooSmall, _ := ServeVocabularyLabels(result, true, 1)
	if got := shapeByRule(tooSmall.RenderShapes, contractsv1.ContextFabricRenderRuleCohortDriverContribution).Series[0].Label; got != "Health risk" {
		t.Errorf("past the bound: legend %q, want the word", got)
	}
	if !slices.ContainsFunc(result.RenderShapes[0].Series, func(s contractsv1.ContextFabricRenderSeries) bool { return s.Label == raw }) {
		t.Fatal("serving wrote into the stored result")
	}
}
