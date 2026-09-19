package devhealthfacts_test

// The ranking formula's operational_deficiencies zero ("no rule fired") is
// credited from what the registry's own read of THIS member's subject did.
// These tests drive the real OperationalDeficienciesProvider over a real
// ClickHouse through the real FactCapabilityRegistry and the real ranking,
// so the read attribution the ranking consumes is the one the producer
// wrote, never a hand-built set.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func deficiencyRankingSignalMissing(member contextfabric.CohortMember) bool {
	for _, name := range member.MissingSignals {
		if name == contextfabric.RankingSignalDeficiencySeverity {
			return true
		}
	}
	return false
}

func readDeficiencyBundle(t *testing.T, ctx context.Context, orgID string, request contextfabric.CanonicalFactRequest) contextfabric.CanonicalFactBundle {
	t.Helper()
	query, _ := sharedClickHouseFixture(t)
	var deficiencies []contextfabric.FactProvider
	for _, provider := range devhealthfacts.NewProviders(query) {
		if provider.Capability().Kind == contextfabric.FactOperationalDeficiencies {
			deficiencies = append(deficiencies, provider)
		}
	}
	registry, err := contextfabric.NewFactCapabilityRegistry(deficiencies, contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	bundle, err := registry.ReadFacts(ctx, storage.Principal{OrgID: orgID}, request)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return bundle
}

func deficiencyRequest(cohort *contextfabric.Cohort, subjects ...contextfabric.SubjectRef) contextfabric.CanonicalFactRequest {
	requirements := []contextfabric.FactRequirement{{Kind: contextfabric.FactOperationalDeficiencies, Parameters: map[string]string{}}}
	return contextfabric.CanonicalFactRequest{
		Question: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: requirements,
		},
		Subjects: subjects, Cohort: cohort, Requirements: requirements,
	}
}

func TestCHAOS5934DeficiencyZeroIsPerMemberAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := sharedClickHouseFixture(t)

	t.Run("project_members_are_not_credited_the_anchor_teams_read", func(t *testing.T) {
		const orgID = "org-5934-projects"
		// The anchor team was read and its read is available for the whole
		// investigation -- evidence about the team only.
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			"ANCHOR", orgID, "saturation", date(2026, 7, 29), date(2026, 8, 12), true, "warning", "Saturation", "elevated", "below threshold", ts(2026, 8, 12, 2, 0, 0)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		cohort := &contextfabric.Cohort{Kind: contextfabric.SubjectProject, Members: []contextfabric.CohortMember{
			{Subject: projectSubject("linear", "proj-a"), Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: projectSubject("linear", "proj-b"), Rank: 2, InclusionReasons: []string{"matched"}},
		}}
		bundle := readDeficiencyBundle(t, ctx, orgID, deficiencyRequest(cohort, teamSubject("ANCHOR")))
		if len(bundle.Facts) != 1 {
			t.Fatalf("facts = %d, want the anchor team's one fired rule", len(bundle.Facts))
		}
		if source := bundle.Coverage.Sources; len(source) != 1 || source[0].State != contextfabric.SourceAvailable {
			t.Fatalf("coverage = %#v, want the kind available (the premise of the leak)", source)
		}
		ranked, event, _ := contextfabric.RankCohortWithReads(cohort, bundle.Facts, bundle.Coverage, bundle.ReadSubjects)
		for _, member := range ranked.Members {
			if !deficiencyRankingSignalMissing(member) {
				t.Fatalf("%s was credited the deficiency zero the anchor team's read produced: missing=%v", member.Subject.CanonicalID, member.MissingSignals)
			}
		}
		if event.DeficiencyZeroWithheld != 2 || !event.ReadAttributionCarried {
			t.Fatalf("event withheld=%d carried=%v, want 2/true", event.DeficiencyZeroWithheld, event.ReadAttributionCarried)
		}
	})

	t.Run("team_members_keep_their_own_zero_and_their_own_fired_rule", func(t *testing.T) {
		const orgID = "org-5934-teams"
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			"HOT", orgID, "saturation", date(2026, 7, 29), date(2026, 8, 12), true, "critical", "Saturation", "high", "below threshold", ts(2026, 8, 12, 2, 0, 0)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		cohort := &contextfabric.Cohort{Kind: contextfabric.SubjectTeam, Members: []contextfabric.CohortMember{
			{Subject: teamSubject("QUIET"), Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: teamSubject("HOT"), Rank: 2, InclusionReasons: []string{"matched"}},
		}}
		bundle := readDeficiencyBundle(t, ctx, orgID, deficiencyRequest(cohort))
		ranked, event, _ := contextfabric.RankCohortWithReads(cohort, bundle.Facts, bundle.Coverage, bundle.ReadSubjects)
		for _, member := range ranked.Members {
			if deficiencyRankingSignalMissing(member) {
				t.Fatalf("%s lost the deficiency signal: missing=%v", member.Subject.CanonicalID, member.MissingSignals)
			}
		}
		if event.DeficiencyZeroWithheld != 0 || event.SignalsAvailable[contextfabric.RankingSignalDeficiencySeverity] != 2 {
			t.Fatalf("event = %#v, want both members scored and none withheld", event)
		}
	})
}
