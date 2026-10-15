package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The cohort lines' legacy key sequences, written down before they were
// declared so a declaration edit cannot move them.
var (
	cohortRankedLegacyKeys = []string{
		"time", "level", "msg",
		"org_id", "cohort_kind", "member_count", "formula_version", "score_meaning", "judgment_mismatch",
		"requested_judgment_kind", "degraded_member_count", "signals_available", "outcome_counts",
		"investment_mix_source_counts", "read_attribution_carried", "deficiency_zero_withheld", "request_id",
	}
	cohortNarrationLegacyKeys = []string{
		"time", "level", "msg",
		"org_id", "outcome", "judgments_emitted", "facts_minted", "members_narrated", "members_skipped_no_evidence",
		"drivers_skipped", "answer_narrative_recomposed", "narration_allocator", "narration_allocated_items", "request_id",
	}
)

func cohortTelemetry(buf *bytes.Buffer) (contextfabric.SlogEngineTelemetry, context.Context) {
	ctx := observability.WithRequestID(context.Background(), factScopeCertifyRequestID)
	return contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))), ctx
}

func certifyRequestLine(t *testing.T, raw []byte, event eventspec.Event, want map[string]any) certify.Line {
	t.Helper()
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if want == nil {
		want = map[string]any{}
	}
	want["request_id"] = factScopeCertifyRequestID
	result, err := certify.Certify(log, certify.Assertion{Event: event, Want: want})
	if err != nil {
		t.Fatalf("%s failed certification: %v\n%s", event.ID, err, raw)
	}
	return result.Line
}

func cohortSubject(id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + id, Label: id}
}

func cohortFact(kind contextfabric.FactKind, id string, fields map[string]contextfabric.FactValue) contextfabric.CanonicalFact {
	return contextfabric.CanonicalFact{Kind: kind, Subject: cohortSubject(id), Fields: fields}
}

func cohortMixFact(id, source string) contextfabric.CanonicalFact {
	fields := map[string]contextfabric.FactValue{
		contextfabric.FactFieldTheme(contextfabric.ThemeFeatureDelivery): contextfabric.NumberFactValue(0.6),
		contextfabric.FactFieldTheme(contextfabric.ThemeOperational):     contextfabric.NumberFactValue(0.4),
		contextfabric.FactFieldThemeQualityBugfix:                        contextfabric.NumberFactValue(0),
	}
	if source != "" {
		fields[contextfabric.FactFieldInvestmentMixSource] = contextfabric.StringFactValue(source)
	}
	return cohortFact(contextfabric.FactInvestment, id, fields)
}

func cohortCoverage(pruneDeficiencies bool) contextfabric.Coverage {
	kinds := []contextfabric.FactKind{contextfabric.FactInvestment, contextfabric.FactHealth, contextfabric.FactOperationalDeficiencies, contextfabric.FactReadiness, contextfabric.FactWorkload}
	sources := make([]contextfabric.SourceObservation, 0, len(kinds))
	for _, kind := range kinds {
		state := contextfabric.SourceAvailable
		if pruneDeficiencies {
			state = contextfabric.SourcePruned
		}
		sources = append(sources, contextfabric.SourceObservation{Source: "canonical_fact:" + string(kind), State: state})
	}
	return contextfabric.Coverage{Sources: sources}
}

// rankedCohortEvents ranks two cohorts so that every declared signal family,
// every member outcome and every investment mix source is reached by a real
// RankCohort call, and returns the events.
func rankedCohortEvents() []contextfabric.CohortRankedEvent {
	member := func(id string) contextfabric.CohortMember {
		return contextfabric.CohortMember{Subject: cohortSubject(id), Rank: 1, InclusionReasons: []string{"matched"}}
	}
	healthy := func(id string) []contextfabric.CanonicalFact {
		return []contextfabric.CanonicalFact{
			cohortFact(contextfabric.FactHealth, id, map[string]contextfabric.FactValue{"severity": contextfabric.StringFactValue("high")}),
			cohortFact(contextfabric.FactOperationalDeficiencies, id, map[string]contextfabric.FactValue{"severity": contextfabric.StringFactValue("high")}),
			cohortFact(contextfabric.FactReadiness, id, map[string]contextfabric.FactValue{"estimate_coverage_ratio": contextfabric.NumberFactValue(0.5)}),
		}
	}
	workload := func(id string) contextfabric.CanonicalFact {
		return cohortFact(contextfabric.FactWorkload, id, map[string]contextfabric.FactValue{"forecast_p50_days": contextfabric.IntegerFactValue(12)})
	}
	var facts []contextfabric.CanonicalFact
	facts = append(facts, cohortMixFact("full", contextfabric.InvestmentMixSourceProjectNative), workload("full"))
	facts = append(facts, healthy("full")...)
	facts = append(facts, cohortMixFact("partial", contextfabric.InvestmentMixSourceOwningTeamRollup))
	facts = append(facts, healthy("partial")...)
	facts = append(facts, cohortMixFact("unlabeled", ""))
	cohortA := &contextfabric.Cohort{Kind: contextfabric.SubjectTeam, Members: []contextfabric.CohortMember{member("full"), member("partial"), member("unlabeled"), member("thin")}}
	_, eventA, _ := contextfabric.RankCohort(cohortA, facts, cohortCoverage(false))
	cohortB := &contextfabric.Cohort{Kind: contextfabric.SubjectTeam, Members: []contextfabric.CohortMember{member("empty")}}
	_, eventB, _ := contextfabric.RankCohort(cohortB, nil, cohortCoverage(true))
	return []contextfabric.CohortRankedEvent{eventA, eventB}
}

func TestCohortRankedLineCertifiesRealRankingsAndReachesEveryDeclaredKey(t *testing.T) {
	t.Parallel()
	reached := map[string]map[string]bool{"signals_available": {}, "outcome_counts": {}, "investment_mix_source_counts": {}}
	for _, event := range rankedCohortEvents() {
		var buf bytes.Buffer
		telemetry, ctx := cohortTelemetry(&buf)
		telemetry.RecordCohortRanked(ctx, storage.Principal{OrgID: "org_1"}, event)
		if got := jsonKeys(t, buf.Bytes()); !reflect.DeepEqual(got, cohortRankedLegacyKeys) {
			t.Fatalf("cohort ranked key sequence = %v, want %v", got, cohortRankedLegacyKeys)
		}
		asAny := func(counts map[string]int) map[string]any {
			out := make(map[string]any, len(counts))
			for key, value := range counts {
				out[key] = value
			}
			return out
		}
		certifyRequestLine(t, buf.Bytes(), eventspec.CohortRanked, map[string]any{
			"org_id": "org_1", "cohort_kind": "team", "member_count": event.MemberCount,
			"formula_version": contextfabric.RankingFormulaVersion, "score_meaning": "attention",
			"degraded_member_count": event.DegradedMemberCount, "read_attribution_carried": event.ReadAttributionCarried,
			"deficiency_zero_withheld": event.DeficiencyZeroWithheld, "signals_available": asAny(event.SignalsAvailable),
			"outcome_counts": asAny(event.OutcomeCounts), "investment_mix_source_counts": asAny(event.InvestmentMixSourceCounts),
		})
		for name, counts := range map[string]map[string]int{"signals_available": event.SignalsAvailable, "outcome_counts": event.OutcomeCounts, "investment_mix_source_counts": event.InvestmentMixSourceCounts} {
			for key := range counts {
				reached[name][key] = true
			}
		}
	}
	for name, seen := range reached {
		var want []string
		for _, key := range contextfabric.CohortRankedLineVocabulary(name) {
			want = append(want, key)
		}
		var got []string
		for key := range seen {
			got = append(got, key)
		}
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: real rankings reached keys %v, the declared closed key set is %v -- a key the producer writes but the declaration lacks (or the reverse) is drift", name, got, want)
		}
	}
}

func TestCohortDriverNarrationLineCertifiesEveryOutcomeAllocatorAndMapShape(t *testing.T) {
	t.Parallel()
	for _, outcome := range contextfabric.CohortDriverNarrationLineVocabulary("outcome") {
		for _, allocator := range contextfabric.CohortDriverNarrationLineVocabulary("narration_allocator") {
			event := contextfabric.CohortDriverNarrationEvent{
				Outcome: contextfabric.CohortDriverNarrationOutcome(outcome), JudgmentsEmitted: 3, FactsMinted: 4, MembersNarrated: 5,
				MembersSkippedNoEvidence: 6, AnswerNarrativeRecomposed: true, AllocatedItems: 7,
				Allocator:      contextfabric.CohortDriverNarrationAllocator(allocator),
				DriversSkipped: map[string]int{"no_citation": 1, "unknown_signal": 2},
			}
			if allocator == "unclassified" {
				event.Allocator = contextfabric.CohortDriverNarrationAllocator("not_a_member")
			}
			var buf bytes.Buffer
			telemetry, ctx := cohortTelemetry(&buf)
			telemetry.RecordCohortDriverNarration(ctx, storage.Principal{OrgID: "org_1"}, event)
			if got := jsonKeys(t, buf.Bytes()); !reflect.DeepEqual(got, cohortNarrationLegacyKeys) {
				t.Fatalf("narration key sequence = %v, want %v", got, cohortNarrationLegacyKeys)
			}
			certifyRequestLine(t, buf.Bytes(), eventspec.CohortDriverNarration, map[string]any{
				"org_id": "org_1", "outcome": outcome, "judgments_emitted": 3, "facts_minted": 4, "members_narrated": 5,
				"members_skipped_no_evidence": 6, "answer_narrative_recomposed": true, "narration_allocator": allocator,
				"narration_allocated_items": 7, "drivers_skipped": map[string]any{"no_citation": 1, "unknown_signal": 2},
			})
		}
	}
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	telemetry.RecordCohortDriverNarration(ctx, storage.Principal{OrgID: "org_1"}, contextfabric.CohortDriverNarrationEvent{
		Outcome: contextfabric.CohortDriverNarrationNoDrivers, Allocator: contextfabric.CohortDriverNarrationAllocatorNotApplicable,
	})
	line := certifyRequestLine(t, buf.Bytes(), eventspec.CohortDriverNarration, nil)
	if value, present := line["drivers_skipped"]; !present || value != nil {
		t.Fatalf("the early outcomes leave the map nil, which must be written as an explicit null, got present=%t value=%v", present, value)
	}
}

// TestClosedCountMapRefusesAnUndeclaredKey drives the closed-key branch of the
// certifier through the real sink: a key the declaration does not list fails.
func TestClosedCountMapRefusesAnUndeclaredKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*contextfabric.CohortRankedEvent)
	}{
		{"signals_available", func(e *contextfabric.CohortRankedEvent) { e.SignalsAvailable = map[string]int{"invented_signal": 1} }},
		{"outcome_counts", func(e *contextfabric.CohortRankedEvent) { e.OutcomeCounts = map[string]int{"invented_outcome": 1} }},
		{"investment_mix_source_counts", func(e *contextfabric.CohortRankedEvent) {
			e.InvestmentMixSourceCounts = map[string]int{"invented_source": 1}
		}},
	} {
		event := contextfabric.CohortRankedEvent{
			SignalsAvailable: map[string]int{}, OutcomeCounts: map[string]int{}, InvestmentMixSourceCounts: map[string]int{},
		}
		tc.mutate(&event)
		var buf bytes.Buffer
		telemetry, ctx := cohortTelemetry(&buf)
		telemetry.RecordCohortRanked(ctx, storage.Principal{OrgID: "org_1"}, event)
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse: %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.CohortRanked, Want: map[string]any{"request_id": factScopeCertifyRequestID}}); err == nil {
			t.Errorf("%s: an undeclared key was certified, want it refused", tc.name)
		}
	}
	// The same key on a nullable closed map (drivers_skipped) is refused too.
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	telemetry.RecordCohortDriverNarration(ctx, storage.Principal{OrgID: "org_1"}, contextfabric.CohortDriverNarrationEvent{
		Outcome: contextfabric.CohortDriverNarrationEmitted, Allocator: contextfabric.CohortDriverNarrationAllocatorStaticCaps,
		DriversSkipped: map[string]int{"invented_reason": 1},
	})
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.CohortDriverNarration, Want: map[string]any{"request_id": factScopeCertifyRequestID}}); err == nil {
		t.Error("an undeclared drivers_skipped key was certified, want it refused")
	}
}
