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
	return readDeficiencyAt(t, ctx, orgID, contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}, teams...)
}

func readDeficiencyRange(t *testing.T, ctx context.Context, orgID string, start, end time.Time, teams ...string) contextfabric.FactProviderResult {
	t.Helper()
	return readDeficiencyAt(t, ctx, orgID, contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, teams...)
}

func readDeficiencyAt(t *testing.T, ctx context.Context, orgID string, timeContext contextfabric.TimeContext, teams ...string) contextfabric.FactProviderResult {
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
		Time: timeContext,
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
		if evaluation == nil || evaluation.Covered != 4 || evaluation.Fired != 1 || evaluation.Stale != 1 || evaluation.NeverEvaluated != 2 || evaluation.RulesEvaluated != len(evaluationRules) || evaluation.LatestWindowEnd != "2026-08-20" || evaluation.FreshnessWindowDays != 14 {
			t.Errorf("evaluation = %#v, want covered 4 fired 1 stale 1 never 2 rules %d latest 2026-08-20 window 14", evaluation, len(evaluationRules))
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
		if stale.Reason == never.Reason || stale.Reason == "" || never.Reason == "" || !strings.Contains(stale.Reason, "1 evaluated outside the freshness window") {
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
		if !strings.Contains(result.Reason, "1 evaluated outside the freshness window") || !strings.Contains(result.Reason, "1 never evaluated") {
			t.Fatalf("reason = %q, want the stale and the never-evaluated counts together", result.Reason)
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

// The reads this feature makes share ONE temporal predicate: the requested
// range intersected with the freshness window. These cells run the real
// producer for every combination of evaluation age and outcome.
func TestCHAOS6031DeficiencyWindowIsOnePredicateAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := sharedClickHouseFixture(t)
	asOf := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	day := func(offset int) time.Time { return asOf.AddDate(0, 0, offset) }
	rangeStart := day(-10)

	t.Run("a_stale_fired_rule_is_not_current_evidence", func(t *testing.T) {
		const orgID = "org-6031-stale-fired"
		seedEvaluation(t, ctx, direct, orgID, "OLDFIRE", day(-20), "saturation")
		result := readDeficiencyAsOf(t, ctx, orgID, asOf, "OLDFIRE")
		if len(result.Facts) != 0 || len(result.EvaluatedSubjects) != 0 {
			t.Fatalf("stale fired rule served: facts=%d evaluated=%d", len(result.Facts), len(result.EvaluatedSubjects))
		}
		if result.Evaluation == nil || result.Evaluation.Stale != 1 {
			t.Fatalf("evaluation = %#v, want the team named stale", result.Evaluation)
		}
	})

	t.Run("a_clean_evaluation_before_the_range_is_not_a_measured_zero", func(t *testing.T) {
		const orgID = "org-6031-before-range"
		seedEvaluation(t, ctx, direct, orgID, "EARLY", day(-12))
		result := readDeficiencyRange(t, ctx, orgID, rangeStart, asOf, "EARLY")
		if len(result.EvaluatedSubjects) != 0 || result.Evaluation == nil || result.Evaluation.BeforeRange != 1 {
			t.Fatalf("evaluated=%d evaluation=%#v, want the team named before_range and not measured", len(result.EvaluatedSubjects), result.Evaluation)
		}
	})

	t.Run("a_fired_rule_before_the_range_is_not_served", func(t *testing.T) {
		const orgID = "org-6031-before-range-fired"
		seedEvaluation(t, ctx, direct, orgID, "EARLYFIRE", day(-12), "saturation")
		result := readDeficiencyRange(t, ctx, orgID, rangeStart, asOf, "EARLYFIRE")
		if len(result.Facts) != 0 {
			t.Fatalf("fired rule dated before the range served: %d facts", len(result.Facts))
		}
	})

	t.Run("a_mixed_cohort_keeps_the_reason_for_its_unmeasured_members", func(t *testing.T) {
		const orgID = "org-6031-mixed-reason"
		seedEvaluation(t, ctx, direct, orgID, "CLEAR", day(-1))
		seedEvaluation(t, ctx, direct, orgID, "STALE", day(-30))
		seedEvaluation(t, ctx, direct, orgID, "EARLY", day(-12))
		result := readDeficiencyRange(t, ctx, orgID, rangeStart, asOf, "CLEAR", "STALE", "EARLY", "GHOST")
		if result.State != contextfabric.SourceAvailable {
			t.Fatalf("state = %q, want available", result.State)
		}
		for _, want := range []string{"3 of 4", "1 evaluated outside the freshness window", "1 evaluated before the requested range", "1 never evaluated"} {
			if !strings.Contains(result.Reason, want) {
				t.Errorf("reason %q lacks %q", result.Reason, want)
			}
		}
		states := map[string]string{}
		for _, member := range result.Evaluation.Members {
			states[member.Subject.Label] = member.State
		}
		want := map[string]string{"CLEAR": "measured_zero", "STALE": "stale", "EARLY": "before_range", "GHOST": "never_evaluated"}
		for team, state := range want {
			if states[team] != state {
				t.Errorf("%s state = %q, want %q", team, states[team], state)
			}
		}
	})

	t.Run("never_evaluated_members_are_named_beside_answered_ones", func(t *testing.T) {
		const orgID = "org-6031-answered"
		seedEvaluation(t, ctx, direct, orgID, "CLEAR", day(-1))
		seedEvaluation(t, ctx, direct, orgID, "HOT", day(-1), "saturation")
		measured := readDeficiencyAsOf(t, ctx, orgID, asOf, "CLEAR", "GHOST")
		if !strings.Contains(measured.Reason, "1 of 2") || !strings.Contains(measured.Reason, "1 never evaluated") {
			t.Fatalf("measured plus never reason = %q, want the never-evaluated team named", measured.Reason)
		}
		fired := readDeficiencyAsOf(t, ctx, orgID, asOf, "HOT", "GHOST")
		if !strings.Contains(fired.Reason, "1 of 2") || !strings.Contains(fired.Reason, "1 never evaluated") {
			t.Fatalf("fired plus never reason = %q, want the never-evaluated team named", fired.Reason)
		}
	})

	// {fresh, stale, before-range, never} x {fired, clean, cleared, refired}
	t.Run("enumeration", func(t *testing.T) {
		type cell struct {
			name  string
			seed  func(org, team string)
			rng   bool
			want  string
			facts int
		}
		fired := []string{"saturation"}
		cells := []cell{
			{"fresh_fired", func(o, tm string) { seedEvaluation(t, ctx, direct, o, tm, day(-1), fired...) }, false, "fired", 1},
			{"fresh_clean", func(o, tm string) { seedEvaluation(t, ctx, direct, o, tm, day(-1)) }, false, "measured_zero", 0},
			{"fresh_cleared", func(o, tm string) {
				seedEvaluation(t, ctx, direct, o, tm, day(-5), fired...)
				seedEvaluation(t, ctx, direct, o, tm, day(-1))
			}, false, "measured_zero", 0},
			{"fresh_refired", func(o, tm string) {
				seedEvaluation(t, ctx, direct, o, tm, day(-5))
				seedEvaluation(t, ctx, direct, o, tm, day(-1), fired...)
			}, false, "fired", 1},
			{"stale_fired", func(o, tm string) { seedEvaluation(t, ctx, direct, o, tm, day(-20), fired...) }, false, "stale", 0},
			{"stale_clean", func(o, tm string) { seedEvaluation(t, ctx, direct, o, tm, day(-20)) }, false, "stale", 0},
			{"before_range_fired", func(o, tm string) { seedEvaluation(t, ctx, direct, o, tm, day(-12), fired...) }, true, "before_range", 0},
			{"before_range_clean", func(o, tm string) { seedEvaluation(t, ctx, direct, o, tm, day(-12)) }, true, "before_range", 0},
			{"in_range_clean", func(o, tm string) { seedEvaluation(t, ctx, direct, o, tm, day(-8)) }, true, "measured_zero", 0},
			{"in_range_fired", func(o, tm string) { seedEvaluation(t, ctx, direct, o, tm, day(-8), fired...) }, true, "fired", 1},
			{"never", func(o, tm string) {}, false, "never_evaluated", 0},
		}
		for _, c := range cells {
			c := c
			t.Run(c.name, func(t *testing.T) {
				orgID := "org-6031-enum-" + c.name
				c.seed(orgID, "TEAM")
				var result contextfabric.FactProviderResult
				if c.rng {
					result = readDeficiencyRange(t, ctx, orgID, rangeStart, asOf, "TEAM")
				} else {
					result = readDeficiencyAsOf(t, ctx, orgID, asOf, "TEAM")
				}
				if result.Evaluation == nil || len(result.Evaluation.Members) != 1 || result.Evaluation.Members[0].State != c.want {
					t.Fatalf("evaluation = %#v, want state %s", result.Evaluation, c.want)
				}
				if len(result.Facts) != c.facts {
					t.Fatalf("facts = %d, want %d", len(result.Facts), c.facts)
				}
				if measured := len(result.EvaluatedSubjects) == 1; measured != (c.want == "measured_zero") {
					t.Fatalf("measured = %v, want %v", measured, c.want == "measured_zero")
				}
			})
		}
	})
}

// Sibling sweep: the health reader resolves the latest known band at or
// before the range end with the same freshness window; a band dated before
// the requested range's start must not be served for that range.
func TestCHAOS6031HealthBandBeforeTheRangeStartIsNotServed(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4363Tables(t, ctx, direct)
	const orgID = "org-6031-health-range"
	repoID := "10000000-0000-0000-0000-000000006031"
	day := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
		orgID, day, "repo", repoID, 0.4, "elevated", day.Add(6*time.Hour)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	inRangeRepo := "10000000-0000-0000-0000-000000006032"
	inRange := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
		orgID, inRange, "repo", inRangeRepo, 0.4, "elevated", inRange.Add(6*time.Hour)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	start, end := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	var provider contextfabric.FactProvider
	for _, candidate := range devhealthfacts.NewProviders(query) {
		if candidate.Capability().Kind == contextfabric.FactHealth {
			provider = candidate
		}
	}
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	for _, fact := range result.Facts {
		if severity := fact.Fields["severity"].String; severity != nil && *severity != "unknown" {
			t.Fatalf("a band dated before the range start was served: %q", *severity)
		}
	}
	control, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject(inRangeRepo)},
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
	})
	if err != nil || len(control.Facts) != 1 || control.Facts[0].Fields["severity"].String == nil || *control.Facts[0].Fields["severity"].String != "elevated" {
		t.Fatalf("control: an in-range band was not served: err=%v facts=%d", err, len(control.Facts))
	}
}
