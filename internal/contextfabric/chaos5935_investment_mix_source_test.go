package contextfabric

import (
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func labelledInvestmentFact(id, source string) CanonicalFact {
	fact := investmentFact(id, map[string]float64{ThemeFeatureDelivery: 0.6, ThemeOperational: 0.4}, 0)
	fact.Fields[FactFieldInvestmentMixSource] = StringFactValue(source)
	return fact
}

// The ranked event counts, per source label, the members whose investment_mix
// signal was actually drawn; a member without theme data counts nowhere and a
// member whose fact carries no label counts as unlabeled.
func TestRankCohortCountsTheInvestmentMixSourceEachMemberDrew(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{
		rankTestMember("native-a"), rankTestMember("native-b"), rankTestMember("rollup"), rankTestMember("plain"), rankTestMember("none"),
	}}
	facts := []CanonicalFact{
		labelledInvestmentFact("native-a", InvestmentMixSourceProjectNative),
		labelledInvestmentFact("native-b", InvestmentMixSourceProjectNative),
		labelledInvestmentFact("rollup", InvestmentMixSourceOwningTeamRollup),
		investmentFact("plain", map[string]float64{ThemeFeatureDelivery: 1}, 0),
	}
	_, event, _ := RankCohort(cohort, facts, availableCoverage())
	want := map[string]int{InvestmentMixSourceProjectNative: 2, InvestmentMixSourceOwningTeamRollup: 1, InvestmentMixSourceUnlabeled: 1}
	if len(event.InvestmentMixSourceCounts) != len(want) {
		t.Fatalf("InvestmentMixSourceCounts = %v, want %v", event.InvestmentMixSourceCounts, want)
	}
	for source, count := range want {
		if event.InvestmentMixSourceCounts[source] != count {
			t.Fatalf("InvestmentMixSourceCounts = %v, want %v", event.InvestmentMixSourceCounts, want)
		}
	}
	if event.SignalsAvailable[RankingSignalInvestmentMix] != 4 {
		t.Fatalf("SignalsAvailable = %v, want 4 members drawing investment_mix", event.SignalsAvailable)
	}
}

// The fact investmentMixSignal reads is the theme-carrying one, so the label
// is read from that same fact, never from another investment fact on the
// member.
func TestInvestmentMixSourceReadsTheThemeCarryingFact(t *testing.T) {
	t.Parallel()
	legacy := CanonicalFact{Kind: FactInvestment, Subject: rankTestSubject("m"), Fields: map[string]FactValue{
		FactFieldInvestmentMixSource: StringFactValue(InvestmentMixSourceOwningTeamRollup),
	}}
	themed := labelledInvestmentFact("m", InvestmentMixSourceProjectNative)
	if got := investmentMixSource([]CanonicalFact{legacy, themed}); got != InvestmentMixSourceProjectNative {
		t.Fatalf("investmentMixSource = %q, want the themed fact's label", got)
	}
	if got := investmentMixSource([]CanonicalFact{legacy}); got != "" {
		t.Fatalf("investmentMixSource = %q, want empty when no fact carries theme shares", got)
	}
}

func TestCohortRankedLineCarriesTheInvestmentMixSourceCounts(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordCohortRanked(
			context.Background(), storage.Principal{OrgID: "org_sink_test"},
			CohortRankedEvent{
				CohortKind: SubjectProject, MemberCount: 3, FormulaVersion: RankingFormulaVersion,
				SignalsAvailable: map[string]int{}, OutcomeCounts: map[string]int{},
				InvestmentMixSourceCounts: map[string]int{InvestmentMixSourceProjectNative: 2, InvestmentMixSourceOwningTeamRollup: 1},
			})
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	counts, ok := records[0]["investment_mix_source_counts"].(map[string]any)
	if !ok || counts[InvestmentMixSourceProjectNative] != float64(2) || counts[InvestmentMixSourceOwningTeamRollup] != float64(1) {
		t.Fatalf("investment_mix_source_counts = %#v", records[0]["investment_mix_source_counts"])
	}
	if records[0]["level"] != "INFO" {
		t.Fatalf("level = %v, want INFO", records[0]["level"])
	}
}

// A fact whose label is present but empty is not a named source.
func TestInvestmentMixSourceTreatsAnEmptyLabelAsUnlabeled(t *testing.T) {
	t.Parallel()
	fact := labelledInvestmentFact("m", "")
	if got := investmentMixSource([]CanonicalFact{fact}); got != InvestmentMixSourceUnlabeled {
		t.Fatalf("investmentMixSource = %q, want %q", got, InvestmentMixSourceUnlabeled)
	}
}
