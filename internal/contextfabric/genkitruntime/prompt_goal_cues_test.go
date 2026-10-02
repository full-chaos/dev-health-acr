package genkitruntime

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestInterpretationPromptGoalCuesStatedOnce(t *testing.T) {
	t.Parallel()
	for _, rule := range []string{
		`naming no measure to follow over a span, is assess_state. When the question names one measure and a span, the movement cue below wins.`,
		`explain_change needs an explicit "why" about a movement`,
		`How one named measure (a duration, a rate or a count) moved over a stated span or over time is describe_trend.`,
		`A "why" about a present condition (stuck, blocked, behind, slow, struggling) is explain_drivers.`,
		`The state of each member of a set, with no order word (an "each" question): assess_state, not rank_or_survey.`,
		`The trend of an effort split over a window: allocate_investment and describe_trend.`,
		`A "why" about an effort split (what causes the split): explain_drivers alone, without allocate_investment`,
		`A follow-up that adds a comparison after an effort-split question: compare alone, without allocate_investment.`,
		`the trend of one measure for two or more named subjects shown together ("side by side", "next to") takes describe_trend and compare`,
		`Clarification: a bare "why?" with no referent`,
		`requested_judgment_kind cues.`,
		`- attention: needs attention or support`,
		`- performance: performed or performing best or worst`,
		`A window-only follow-up also keeps the prior turn's emphasis.`,
		`Both values when both ends are asked`,
		`No emphasis for a plain filter`,
	} {
		if got := strings.Count(interpretationSystemPrompt, rule); got != 1 {
			t.Errorf("rule %q appears %d times, want exactly 1", rule, got)
		}
	}
}

func TestInterpretationPromptDropsContradictedSentences(t *testing.T) {
	t.Parallel()
	for _, gone := range []string{
		"Emit it only when the question explicitly asks about the ends of a ranking",
		"Emit a dimension only when the question is explicitly ABOUT it",
		"Emit EVERY goal the question asks for",
		"Use clarification only when materially different authorized subjects or timeframes remain plausible and proceeding would make the answer unreliable.\n",
		"performance when it asks who performed best/worst",
	} {
		if strings.Contains(interpretationSystemPrompt, gone) {
			t.Errorf("prompt still carries the contradicted sentence %q", gone)
		}
	}
}

// The word lists are data: a synonym added to a list must be a deliberate edit
// of this table, not a stray edit of prose.
func TestInterpretationDimensionWordListsAreTheClosedTable(t *testing.T) {
	t.Parallel()
	want := map[string][]string{
		"delivery_flow":               {"flow", "pace", "speed", "fast", "faster", "fastest", "slow", "slower", "slowest", "slowing", "cycle time", "throughput"},
		"cognitive_workload_pressure": {"workload", "on-call", "burden", "overload", "load on a team or the organization"},
		"dependencies_and_blockers":   {`"stuck"`, `"holding up"`, `"blocked by"`, `"blocking"`, `"holding X back"`},
	}
	vocab := map[string]bool{}
	for _, d := range contextfabric.HealthDimensionVocabulary() {
		vocab[string(d)] = true
	}
	seen := map[string]bool{}
	byName := map[string]string{}
	for _, d := range interpretationDimensionWords {
		if !vocab[d.Dimension] {
			t.Errorf("dimension %q is not in the closed vocabulary", d.Dimension)
		}
		if seen[d.Dimension] {
			t.Errorf("dimension %q listed twice", d.Dimension)
		}
		seen[d.Dimension] = true
		byName[d.Dimension] = d.Words
		if got := strings.Count(interpretationSystemPrompt, "- "+d.Dimension+": "+d.Words+"."); got != 1 {
			t.Errorf("dimension line for %q appears %d times, want 1", d.Dimension, got)
		}
	}
	for name := range vocab {
		if !seen[name] {
			t.Errorf("vocabulary dimension %q has no word list", name)
		}
	}
	for name, words := range want {
		for _, w := range words {
			if !strings.Contains(byName[name], w) {
				t.Errorf("%s word list lost %q", name, w)
			}
		}
	}
	for _, synonym := range []string{"overwhelmed", "capacity", "turnaround", "on track to finish", "bottleneck", "moving smoothly"} {
		for name, words := range byName {
			if strings.Contains(strings.ToLower(words), synonym) {
				t.Errorf("synonym %q must not be in the %s word list", synonym, name)
			}
		}
	}
	if len(interpretationDimensionWords) != 9 {
		t.Errorf("want 9 dimension word lists, got %d", len(interpretationDimensionWords))
	}
}

func TestInterpretationEmphasisWordListsCoverTheVocabulary(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, e := range interpretationEmphasisWords {
		seen[e.Emphasis] = true
		if got := strings.Count(interpretationSystemPrompt, "- "+e.Emphasis+" for a word like: "+e.Words+"."); got != 1 {
			t.Errorf("emphasis line for %q appears %d times, want 1", e.Emphasis, got)
		}
	}
	for _, v := range contextfabric.AnswerEmphasisVocabulary() {
		if !seen[string(v)] {
			t.Errorf("emphasis %q has no word list", v)
		}
	}
}
