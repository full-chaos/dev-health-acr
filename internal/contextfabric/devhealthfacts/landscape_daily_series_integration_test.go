package devhealthfacts_test

// CHAOS-12534: the daily landscape series of a team, on a real ClickHouse. The
// series keeps a team's newest landscapeSeriesDays (64) days. A longer history
// is said on the fact; it is a cut in the read only when the question stated
// the span.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const landscapeSeriesHistoryDays = 70

func landscapeSeriesProvider(t *testing.T, ctx context.Context, orgID string, teams []string, retractedDaysAgo int) contextfabric.FactProvider {
	t.Helper()
	rawQuery, direct := newDedicatedClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "team_project_ownership", "ic_landscape_rolling_30d") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	yesterday := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	for _, team := range teams {
		for _, person := range []string{"alice", "bob"} {
			// Days 1 to 70 before today, one row per map.
			if err := direct.Exec(ctx, `INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, x_raw, y_raw, x_norm, y_norm, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at)
SELECT ?, toUUID(?), toDate(?) - number, ?, ?, arrayJoin(['churn_throughput', 'cycle_throughput', 'wip_throughput']), 1.5, 2.5, 0.5, 0.5, toUInt64(100), toUInt32(number % 7 + 1), 5.0, toUInt32(3), toDateTime(?)
FROM numbers(?)`, orgID, repoUUID("repo-series"), yesterday.Format(time.DateOnly), person, team, yesterday, uint64(landscapeSeriesHistoryDays)); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		if retractedDaysAgo > 0 {
			// Ops retracts a key with a later row of zeros: that day holds no live row.
			day := yesterday.AddDate(0, 0, 1-retractedDaysAgo)
			for _, person := range []string{"alice", "bob"} {
				for _, mapName := range []string{"churn_throughput", "cycle_throughput", "wip_throughput"} {
					if err := direct.Exec(ctx, `INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, computed_at) VALUES (?,?,?,?,?,?,?)`,
						orgID, repoUUID("repo-series"), day, person, team, mapName, yesterday.Add(time.Hour)); err != nil {
						t.Fatalf("retract: %v", err)
					}
				}
			}
		}
	}
	return findProvider(t, devhealthfacts.NewProviders(statementthreads.API(rawQuery)), contextfabric.FactLandscape)
}

func readLandscapeSeries(t *testing.T, ctx context.Context, provider contextfabric.FactProvider, orgID string, at contextfabric.TimeContext, teams ...string) contextfabric.FactProviderResult {
	t.Helper()
	subjects := make([]contextfabric.SubjectRef, 0, len(teams))
	for _, team := range teams {
		subjects = append(subjects, teamSubject(team))
	}
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{Time: at, Kind: contextfabric.FactLandscape, Subjects: subjects})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result
}

func seriesOf(t *testing.T, fact contextfabric.CanonicalFact) []contextfabric.FactValueRow {
	t.Helper()
	series := fact.Fields["daily_landscape"]
	if series.Table == nil || series.Table.Shape != contextfabric.FactTableTimeSeries {
		t.Fatalf("%s: no declared daily series: %+v", fact.Subject.CanonicalID, fact.Fields)
	}
	return series.Rows
}

func TestALandscapeSeriesKeepsTheNewestDaysAndSaysTheRest(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-landscape-series-cap"
	provider := landscapeSeriesProvider(t, ctx, orgID, []string{"gh:tm-cap"}, 0)
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	result := readLandscapeSeries(t, ctx, provider, orgID, current, "gh:tm-cap")
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	rows := seriesOf(t, result.Facts[0])
	if len(rows) != 64 {
		t.Fatalf("series rows = %d, want the newest 64", len(rows))
	}
	newest := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1).Format(time.DateOnly)
	if got := *rows[0].Fields["day"].String; got != newest {
		t.Fatalf("first row is %s, want the newest day %s", got, newest)
	}
	// One copy of each identity's stats per day, summed: two identities of 100 lines.
	if got := *rows[0].Fields["churn_loc_30d"].Integer; got != 200 {
		t.Fatalf("churn_loc_30d = %d, want 200: one copy per identity, not one per map", got)
	}
	if got := *rows[0].Fields["identity_count"].Integer; got != 2 {
		t.Fatalf("identity_count = %d, want 2", got)
	}
	if got := result.Facts[0].Fields["daily_landscape_omitted_count"].Integer; got == nil || *got != landscapeSeriesHistoryDays-64 {
		t.Fatalf("daily_landscape_omitted_count = %v, want %d", got, landscapeSeriesHistoryDays-64)
	}
	if result.OmittedCount != 0 || result.Truncated {
		t.Fatalf("with no stated window the series is the newest days by design, yet OmittedCount = %d, Truncated = %v", result.OmittedCount, result.Truncated)
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	start := today.AddDate(0, 0, -90)
	stated := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &today, RelativeID: contextfabric.RelativeWindowTrailing90D}}
	windowed := readLandscapeSeries(t, ctx, provider, orgID, stated, "gh:tm-cap")
	if len(windowed.Facts) != 1 || len(seriesOf(t, windowed.Facts[0])) != 64 {
		t.Fatalf("a stated 90-day window: facts %d", len(windowed.Facts))
	}
	if windowed.OmittedCount != landscapeSeriesHistoryDays-64 {
		t.Fatalf("a stated window holds %d days and the series 64, yet OmittedCount = %d", landscapeSeriesHistoryDays, windowed.OmittedCount)
	}

	// A shorter stated window bounds the series: days 1 to 30 before today.
	start30 := today.AddDate(0, 0, -30)
	short := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start30, End: &today, RelativeID: contextfabric.RelativeWindowTrailing30D}}
	bounded := readLandscapeSeries(t, ctx, provider, orgID, short, "gh:tm-cap")
	if len(bounded.Facts) != 1 {
		t.Fatalf("a stated 30-day window: facts %d", len(bounded.Facts))
	}
	if got := len(seriesOf(t, bounded.Facts[0])); got != 30 {
		t.Fatalf("a stated 30-day window holds %d days of series, want 30", got)
	}
	if _, cut := bounded.Facts[0].Fields["daily_landscape_omitted_count"]; cut || bounded.OmittedCount != 0 {
		t.Fatalf("a 30-day series is whole, yet a cut is said: %+v, OmittedCount %d", bounded.Facts[0].Fields["daily_landscape_omitted_count"], bounded.OmittedCount)
	}
}

// Five teams' series are 320 rows. The per-team limit keeps each series whole,
// whatever the shared row budget of a query.
func TestEveryTeamKeepsItsWholeSeriesInAManyTeamRead(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-landscape-series-many"
	teams := []string{}
	for i := 0; i < 5; i++ {
		teams = append(teams, fmt.Sprintf("gh:tm-many-%d", i))
	}
	provider := landscapeSeriesProvider(t, ctx, orgID, teams, 0)
	result := readLandscapeSeries(t, ctx, provider, orgID, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, teams...)
	if len(result.Facts) != len(teams) {
		t.Fatalf("facts = %d, want %d", len(result.Facts), len(teams))
	}
	for _, fact := range result.Facts {
		if got := len(seriesOf(t, fact)); got != 64 {
			t.Fatalf("%s: series rows = %d, want 64", fact.Subject.CanonicalID, got)
		}
	}
	if result.Truncated {
		t.Fatal("a many-team read was marked truncated")
	}
}

// A day Ops retracted holds no live row: it is missing, never a day of zeros.
func TestARetractedDayIsMissingFromTheSeries(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-landscape-series-retracted"
	provider := landscapeSeriesProvider(t, ctx, orgID, []string{"gh:tm-gap"}, 3)
	result := readLandscapeSeries(t, ctx, provider, orgID, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, "gh:tm-gap")
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	retracted := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -3).Format(time.DateOnly)
	for _, row := range seriesOf(t, result.Facts[0]) {
		if *row.Fields["day"].String == retracted {
			t.Fatalf("the retracted day %s is in the series: %+v", retracted, row.Fields)
		}
		if *row.Fields["identity_count"].Integer == 0 {
			t.Fatalf("a day of zeros is in the series: %+v", row.Fields)
		}
	}
}
