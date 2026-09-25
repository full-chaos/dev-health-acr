package devhealthfacts_test

// CHAOS-6559 (prod shape): a team that has legacy investment_metrics_daily
// day rows AND owns repositories with persisted work. On prod the mix was
// merged onto the first day row, the model answered from the day rows and
// reported "no normalized shares". EXECUTED against a real ClickHouse.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTeamMixIsStandaloneAndLegacyDayRowsAreNeverServedAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-6559-legacy-rows"

	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
		repoUUID("repo-a"), orgID, "acme/repo-a", "github", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	// The prod ownership anomaly: the same (team, repo) held by many rows
	// that differ only by valid_from; it must still count the repo once.
	for i := 0; i < 5; i++ {
		validFrom := ts(2026, 9, 1+i, 0, 0, 0)
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", "CHAOS", repoUUID("repo-a"), "acme/repo-a", "exact", "native", uint8(1), uint16(100), int32(0), validFrom, nil, validFrom); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
	}
	if err := direct.Exec(ctx,
		`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
		"wu1", at, at, 10.0, map[string]float64{"feature_delivery": 0.6, "risk": 0.4}, map[string]float64{},
		fmt.Sprintf(`{"issues":[],"prs":["%s#pr1"]}`, repoUUID("repo-a")), at, orgID); err != nil {
		t.Fatalf("seed wu: %v", err)
	}
	for i, area := range []string{"quality", "security", "infra", "product"} {
		day := date(2026, 9, 10+i)
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			day, "CHAOS", area, "", uint32(5), uint32(1), uint32(1), uint64(100), 10.0, at, orgID); err != nil {
			t.Fatalf("seed legacy row: %v", err)
		}
	}

	read := func(teams ...string) contextfabric.FactProviderResult {
		t.Helper()
		subjects := make([]contextfabric.SubjectRef, 0, len(teams))
		for _, id := range teams {
			subjects = append(subjects, teamSubject(id))
		}
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result
	}

	t.Run("mix_is_one_standalone_fact_and_no_day_row_is_served", func(t *testing.T) {
		result := read("CHAOS")
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %d, want exactly 1 (the mix): %#v", len(result.Facts), result.Facts)
		}
		fields := result.Facts[0].Fields
		for _, legacy := range []string{"investment_area", "day", "delivery_units"} {
			if _, has := fields[legacy]; has {
				t.Fatalf("the mix fact carries legacy field %q: a window mix must not ride on a day row", legacy)
			}
		}
		for theme, want := range map[string]float64{"theme_feature_delivery": 0.6, "theme_risk": 0.4} {
			got, ok := fields[theme]
			if !ok || got.Number == nil || *got.Number < want-1e-9 || *got.Number > want+1e-9 {
				t.Fatalf("%s = %#v, want %v", theme, got, want)
			}
		}
		if got := *fields["owned_repository_count"].Integer; got != 1 {
			t.Fatalf("owned_repository_count = %d, want 1 (repo counted once across 5 ownership rows)", got)
		}
	})

	t.Run("team_without_a_mix_gets_no_fact_and_a_loud_unavailable_reason_with_watermark", func(t *testing.T) {
		result := read("NOOWNER")
		if len(result.Facts) != 0 {
			t.Fatalf("facts = %#v, want none: legacy day rows must not stand in for a missing mix", result.Facts)
		}
		if result.State != contextfabric.SourceNoData {
			t.Fatalf("state = %q, want %q", result.State, contextfabric.SourceNoData)
		}
		for _, want := range []string{"investment mix unavailable for 1 of 1 requested teams", "2026-09-18"} {
			if !strings.Contains(result.Reason, want) {
				t.Fatalf("reason = %q, want it to contain %q", result.Reason, want)
			}
		}
	})

	t.Run("one_team_with_a_mix_and_one_without_serves_one_and_discloses_the_other", func(t *testing.T) {
		result := read("CHAOS", "NOOWNER")
		if len(result.Facts) != 1 || result.State != contextfabric.SourceAvailable {
			t.Fatalf("facts = %d state = %q, want 1 fact available", len(result.Facts), result.State)
		}
		if !strings.Contains(result.Reason, "investment mix unavailable for 1 of 2 requested teams") {
			t.Fatalf("reason = %q, want the unavailable disclosure for 1 of 2", result.Reason)
		}
	})
}
