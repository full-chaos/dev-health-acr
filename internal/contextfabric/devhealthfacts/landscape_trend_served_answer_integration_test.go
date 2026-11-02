package devhealthfacts_test

// CHAOS-12534, the run first: a team's landscape measures over time, through the
// real Engine.Investigate over a real ClickHouse, into dated_fact_trend.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const landscapeTrendTeam = "gh:tm-trend"

// landscapeTrendStore seeds two identities on each of the three maps for days 1
// to 10 before today, except day 5, which holds no row.
func landscapeTrendStore(t *testing.T, ctx context.Context, orgID string) contextpacket.ClickHouseQueryClient {
	t.Helper()
	rawQuery, direct := newDedicatedClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "team_project_ownership", "ic_landscape_rolling_30d") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for daysAgo := 1; daysAgo <= 10; daysAgo++ {
		if daysAgo == 5 {
			continue
		}
		day := today.AddDate(0, 0, -daysAgo)
		for _, mapName := range []string{"churn_throughput", "cycle_throughput", "wip_throughput"} {
			for _, person := range []string{"alice", "bob"} {
				if err := direct.Exec(ctx, `INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, x_raw, y_raw, x_norm, y_norm, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					orgID, repoUUID("repo-trend"), day, person, landscapeTrendTeam, mapName, 1.5, 2.5, 0.5, 0.5, uint64(100*daysAgo), uint32(daysAgo), 5.0, uint32(3), day); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
		}
	}
	return statementthreads.API(rawQuery)
}

func TestALandscapeClaimDrawsTheTeamsMeasureOverTime(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-landscape-trend"
	query := landscapeTrendStore(t, ctx, orgID)
	result, model := serveLandscapeQuestion(t, ctx, query, orgID, teamSubject(landscapeTrendTeam), "delivery_units_30d")
	claim := servedLandscapeClaim(t, result, model)
	t.Logf("plan family=%s render_kinds=%v", result.AnswerPlan.Family, result.AnswerPlan.RenderKinds)
	t.Logf("claim rows=%d table=%v series_rows=%d series_table=%+v", len(claim.Rows), claim.Table != nil, len(claim.TimeSeriesRows), claim.TimeSeriesTable)
	for _, shape := range result.RenderShapes {
		days := []string{}
		for _, point := range shape.Series[0].Points {
			days = append(days, point.Label)
		}
		t.Logf("shape kind=%s rule=%s title=%q value_label=%q points=%d days=%v", shape.Kind, shape.SelectedBy, shape.Title, shape.ValueLabel, len(shape.Series[0].Points), days)
	}
	if len(claim.TimeSeriesRows) != 9 {
		t.Fatalf("series rows = %d, want 9: day 5 holds no row and stays missing", len(claim.TimeSeriesRows))
	}
	var trend *contractsv1.ContextFabricRenderShape
	for i := range result.RenderShapes {
		if result.RenderShapes[i].SelectedBy == contractsv1.ContextFabricRenderRuleDatedFactTrend {
			trend = &result.RenderShapes[i]
		}
	}
	if trend == nil {
		t.Fatalf("no dated_fact_trend shape: %+v", result.RenderShapes)
	}
	if got := len(trend.Series[0].Points); got != 9 {
		t.Fatalf("trend points = %d, want 9", got)
	}
}

// A claim on a value the series does not hold draws no trend (check 10).
func TestALandscapeClaimOffTheSeriesDrawsNoTrend(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-landscape-trend-off"
	query := landscapeTrendStore(t, ctx, orgID)
	result, model := serveLandscapeQuestion(t, ctx, query, orgID, teamSubject(landscapeTrendTeam), "cycle_p50_30d_hours_avg")
	servedLandscapeClaim(t, result, model)
	for _, shape := range result.RenderShapes {
		if shape.SelectedBy == contractsv1.ContextFabricRenderRuleDatedFactTrend {
			t.Fatalf("a claim off the series drew %+v", shape)
		}
	}
}

var _ = contextfabric.FactLandscape
