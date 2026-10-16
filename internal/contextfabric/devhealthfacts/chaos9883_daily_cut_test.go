package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-9883: a team whose daily_health table lost rows to the display cap
// names the loss on its own fact and carries its own truncated state; the kind
// stays truncated for the engine, the truncation is flagged subject-local, and
// the scalar severity and risk come from a separate read, never from the cut
// rows.
func TestChaos9883DailyHealthCutIsDisclosedOnTheSubjectNotTheKind(t *testing.T) {
	t.Parallel()
	var series [][]any
	for i := 0; i < contextfabric.MaxFactValueRows+10; i++ {
		day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format("2006-01-02")
		series = append(series, healthTeamDailySeriesRow("CHECKOUT", day, "high", uint8(1), 0.61))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRow("CHECKOUT"), healthRow("IDENTITY")}},
		{match: healthDailySeriesMatch, rows: append(series, healthTeamDailySeriesRow("IDENTITY", "2026-09-27", "high", uint8(1), 0.61))},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{teamSubject("CHECKOUT"), teamSubject("IDENTITY")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if !result.Truncated || !result.TruncationSubjectLocal {
		t.Fatalf("Truncated=%v TruncationSubjectLocal=%v, want both true: only a display cap cut one subject's daily table", result.Truncated, result.TruncationSubjectLocal)
	}
	for _, fact := range result.Facts {
		omitted, cut := fact.Fields["daily_health_omitted_count"]
		wantCut := fact.Subject.CanonicalID == "team:CHECKOUT"
		if cut != wantCut {
			t.Errorf("%s daily_health_omitted_count present = %v, want %v", fact.Subject.CanonicalID, cut, wantCut)
		}
		wantState := contextfabric.SourceAvailable
		if wantCut {
			wantState = contextfabric.SourceTruncated
		}
		if fact.SourceState != wantState {
			t.Errorf("%s source state = %q, want %q", fact.Subject.CanonicalID, fact.SourceState, wantState)
		}
		if cut && (omitted.Integer == nil || *omitted.Integer != 10) {
			t.Errorf("%s daily_health_omitted_count = %v, want 10", fact.Subject.CanonicalID, omitted)
		}
		if got := fact.Fields["compounding_risk"].Number; got == nil || *got != 0.62 {
			t.Errorf("%s compounding_risk = %v, want the scalar read's 0.62", fact.Subject.CanonicalID, fmt.Sprint(got))
		}
	}
}

// The engine reads the registry bundle, not read_facts: the kind must stay
// truncated and partial there, with the per-fact states intact.
func TestChaos9883EngineBundleStaysTruncatedForADailyHealthCut(t *testing.T) {
	t.Parallel()
	var series [][]any
	for i := 0; i < contextfabric.MaxFactValueRows+10; i++ {
		day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format("2006-01-02")
		series = append(series, healthTeamDailySeriesRow("CHECKOUT", day, "high", uint8(1), 0.61))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRow("CHECKOUT"), healthRow("IDENTITY")}},
		{match: healthDailySeriesMatch, rows: series},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	requirements := []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth, Parameters: map[string]string{}}}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.CanonicalFactRequest{
		Question: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: requirements,
		},
		Subjects:     []contextfabric.SubjectRef{teamSubject("CHECKOUT"), teamSubject("IDENTITY")},
		Requirements: requirements,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if got := bundle.Outcomes[contextfabric.FactHealth]; got.State != contextfabric.SourceTruncated || !got.TruncatedOnlyBySubjectCuts {
		t.Fatalf("outcome state=%q TruncatedOnlyBySubjectCuts=%v, want truncated and true", got.State, got.TruncatedOnlyBySubjectCuts)
	}
	for _, source := range bundle.Coverage.Sources {
		if source.Source == "canonical_fact:health" && source.State != contextfabric.SourceTruncated {
			t.Errorf("coverage source state = %q, want truncated", source.State)
		}
	}
	for _, fact := range bundle.Facts {
		want := contextfabric.SourceAvailable
		if fact.Subject.CanonicalID == "team:CHECKOUT" {
			want = contextfabric.SourceTruncated
		}
		if fact.SourceState != want {
			t.Errorf("%s fact state = %q, want %q", fact.Subject.CanonicalID, fact.SourceState, want)
		}
	}
}
