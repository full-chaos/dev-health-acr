package devhealthfacts_test

// A team is credited a measured deficiency zero only when the producer can
// show its rules were evaluated inside the freshness window. The producer
// writes one recommendations_daily row per rule per evaluation, fired or not,
// so these tests seed those rows and read them back through the real
// provider, the real registry and the real ranking over a real ClickHouse.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var evaluationRules = []string{"saturation", "sustainability-risk", "compounding-risk", "review-latency", "flow-debt"}

func seedEvaluation(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}, orgID, teamID string, windowEnd time.Time, firedRules ...string) {
	t.Helper()
	fired := map[string]bool{}
	for _, rule := range firedRules {
		fired[rule] = true
	}
	for _, rule := range evaluationRules {
		severity := "critical"
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			teamID, orgID, rule, windowEnd.AddDate(0, 0, -14), windowEnd, fired[rule], severity, rule, "", "", windowEnd.Add(2*time.Hour)); err != nil {
			t.Fatalf("seed evaluation %s/%s: %v", teamID, rule, err)
		}
	}
}

func readDeficiencyAsOf(t *testing.T, ctx context.Context, orgID string, asOf time.Time, teams ...string) contextfabric.FactProviderResult {
	t.Helper()
	query, _ := sharedClickHouseFixture(t)
	var provider contextfabric.FactProvider
	for _, candidate := range devhealthfacts.NewProviders(query) {
		if candidate.Capability().Kind == contextfabric.FactOperationalDeficiencies {
			provider = candidate
		}
	}
	subjects := make([]contextfabric.SubjectRef, 0, len(teams))
	for _, team := range teams {
		subjects = append(subjects, teamSubject(team))
	}
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Kind: contextfabric.FactOperationalDeficiencies, Subjects: subjects,
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result
}

func evaluatedSet(result contextfabric.FactProviderResult) map[string]bool {
	set := map[string]bool{}
	for _, subject := range result.EvaluatedSubjects {
		set[subject.Label] = true
	}
	return set
}

func TestCHAOS6031MeasuredZeroDomainAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := sharedClickHouseFixture(t)
	asOf := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	day := func(offset int) time.Time { return asOf.AddDate(0, 0, offset) }

	t.Run("domain_table", func(t *testing.T) {
		const orgID = "org-6031-domain"
		seedEvaluation(t, ctx, direct, orgID, "FIRED", day(-1), "saturation")
		seedEvaluation(t, ctx, direct, orgID, "CLEAR", day(-1))
		seedEvaluation(t, ctx, direct, orgID, "ASOFDAY", day(0))
		seedEvaluation(t, ctx, direct, orgID, "EDGE14", day(-14))
		seedEvaluation(t, ctx, direct, orgID, "EDGE15", day(-15))
		seedEvaluation(t, ctx, direct, orgID, "FUTURE", day(1))
		seedEvaluation(t, ctx, direct, orgID, "CLEARED", day(-30), "saturation")
		seedEvaluation(t, ctx, direct, orgID, "CLEARED", day(-1))
		result := readDeficiencyAsOf(t, ctx, orgID, asOf, "FIRED", "CLEAR", "ASOFDAY", "EDGE14", "EDGE15", "FUTURE", "CLEARED", "NEVER")
		got := evaluatedSet(result)
		want := map[string]bool{"CLEAR": true, "ASOFDAY": true, "EDGE14": true, "CLEARED": true}
		for team := range want {
			if !got[team] {
				t.Errorf("%s not credited a measured zero", team)
			}
		}
		for _, team := range []string{"FIRED", "EDGE15", "FUTURE", "NEVER"} {
			if got[team] {
				t.Errorf("%s credited a measured zero without a fresh clear evaluation", team)
			}
		}
		if len(result.Facts) != 1 || result.Facts[0].Subject.Label != "FIRED" {
			t.Errorf("facts = %d, want exactly the FIRED team's rule", len(result.Facts))
		}
		if result.State != contextfabric.SourceAvailable {
			t.Errorf("state = %q, want available", result.State)
		}
		evaluation := result.Evaluation
		if evaluation == nil || evaluation.Covered != 4 || evaluation.Stale != 1 || evaluation.NeverEvaluated != 2 || evaluation.RulesEvaluated != len(evaluationRules) || evaluation.LatestWindowEnd != "2026-08-20" || evaluation.FreshnessWindowDays != 14 {
			t.Errorf("evaluation = %#v, want covered 4 stale 1 never 2 rules %d latest 2026-08-20 window 14", evaluation, len(evaluationRules))
		}
	})

	t.Run("no_measured_zero_reports_why", func(t *testing.T) {
		const orgID = "org-6031-nodata"
		seedEvaluation(t, ctx, direct, orgID, "OLD", day(-40))
		stale := readDeficiencyAsOf(t, ctx, orgID, asOf, "OLD")
		if stale.State != contextfabric.SourceNoData || len(stale.EvaluatedSubjects) != 0 {
			t.Fatalf("stale-only read = state %q evaluated %d, want no_data and none", stale.State, len(stale.EvaluatedSubjects))
		}
		never := readDeficiencyAsOf(t, ctx, orgID, asOf, "GHOST")
		if never.State != contextfabric.SourceNoData || len(never.EvaluatedSubjects) != 0 {
			t.Fatalf("never-only read = state %q evaluated %d, want no_data and none", never.State, len(never.EvaluatedSubjects))
		}
		if stale.Reason == never.Reason || stale.Reason == "" || never.Reason == "" {
			t.Fatalf("stale reason %q and never reason %q must both be set and differ", stale.Reason, never.Reason)
		}
	})

	t.Run("a_clear_only_read_is_available_and_names_its_evidence", func(t *testing.T) {
		const orgID = "org-6031-clear-only"
		seedEvaluation(t, ctx, direct, orgID, "SOLO", day(-1))
		// An older evaluation of a rule the latest one does not carry
		// must not inflate the rule count of the latest evaluation.
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			"SOLO", orgID, "retired-rule", day(-30), day(-16), false, "warning", "t", "", "", day(-16).Add(time.Hour)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		result := readDeficiencyAsOf(t, ctx, orgID, asOf, "SOLO")
		if result.State != contextfabric.SourceAvailable || result.Reason != "" || len(result.Facts) != 0 {
			t.Fatalf("state %q reason %q facts %d, want available, no reason, no facts", result.State, result.Reason, len(result.Facts))
		}
		if !evaluatedSet(result)["SOLO"] || result.Evaluation == nil || result.Evaluation.RulesEvaluated != len(evaluationRules) {
			t.Fatalf("evaluated %v evaluation %#v, want SOLO with %d rules at the latest evaluation", evaluatedSet(result), result.Evaluation, len(evaluationRules))
		}
	})

	t.Run("stale_and_never_evaluated_teams_are_both_named", func(t *testing.T) {
		const orgID = "org-6031-mixed"
		seedEvaluation(t, ctx, direct, orgID, "OLD", day(-40))
		result := readDeficiencyAsOf(t, ctx, orgID, asOf, "OLD", "GHOST")
		if !strings.Contains(result.Reason, "outside the freshness window") || !strings.Contains(result.Reason, "; ") {
			t.Fatalf("reason = %q, want the stale disclosure and the absence disclosure together", result.Reason)
		}
	})

	t.Run("capped_read_withholds_every_zero", func(t *testing.T) {
		const orgID = "org-6031-capped"
		seedEvaluation(t, ctx, direct, orgID, "QUIET", day(-1))
		for i := 0; i < 201; i++ {
			if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				"BUSY", orgID, fmt.Sprintf("rule-%03d", i), day(-15), day(-1), true, "warning", "t", "r", "s", day(-1).Add(time.Hour)); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		result := readDeficiencyAsOf(t, ctx, orgID, asOf, "QUIET", "BUSY")
		if !result.Truncated {
			t.Fatal("the capped read did not report truncation")
		}
		if len(result.EvaluatedSubjects) != 0 {
			t.Fatalf("a capped read credited measured zeros: %v", evaluatedSet(result))
		}
	})

	t.Run("ranking_credits_only_evaluated_members", func(t *testing.T) {
		const orgID = "org-6031-ranking"
		seedEvaluation(t, ctx, direct, orgID, "HOT", day(-1), "saturation")
		seedEvaluation(t, ctx, direct, orgID, "CALM", day(-1))
		seedEvaluation(t, ctx, direct, orgID, "OLD", day(-40))
		cohort := &contextfabric.Cohort{Kind: contextfabric.SubjectTeam, Members: []contextfabric.CohortMember{
			{Subject: teamSubject("HOT"), Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: teamSubject("CALM"), Rank: 2, InclusionReasons: []string{"matched"}},
			{Subject: teamSubject("OLD"), Rank: 3, InclusionReasons: []string{"matched"}},
			{Subject: teamSubject("GHOST"), Rank: 4, InclusionReasons: []string{"matched"}},
		}}
		request := deficiencyRequest(cohort)
		request.Question.TimeContext = contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}
		bundle := readDeficiencyBundle(t, ctx, orgID, request)
		ranked, event, _ := contextfabric.RankCohortWithReads(cohort, bundle.Facts, bundle.Coverage, bundle.EvaluatedSubjects)
		missing := map[string]bool{}
		for _, member := range ranked.Members {
			missing[member.Subject.Label] = deficiencyRankingSignalMissing(member)
		}
		if missing["HOT"] || missing["CALM"] {
			t.Fatalf("evaluated members lost the signal: %v", missing)
		}
		if !missing["OLD"] || !missing["GHOST"] {
			t.Fatalf("stale/never-evaluated members were credited a zero: %v", missing)
		}
		if event.DeficiencyZeroWithheld != 2 {
			t.Fatalf("withheld = %d, want 2", event.DeficiencyZeroWithheld)
		}
	})
}
