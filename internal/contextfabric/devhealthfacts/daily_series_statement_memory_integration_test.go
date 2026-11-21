package devhealthfacts_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// Every team and project daily-series statement stays under the package's
// per-statement memory ceiling when it reads a year of days for 20 teams
// and 20 projects, each day stored twice, next to 80 teams that are not
// asked for. The client runs with the hosted runtime's result-row and
// read-byte limits.
func TestDailySeriesStatementMemoryStaysUnderTheCeilingOnRealClickHouse(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("memory bound rows run in the unit legs; the race detector does not change a server peak")
	}
	ctx := context.Background()
	rawQuery, direct := newDedicatedClickHouseIntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
		options.MaxResultRows = productionMaxResultRows()
	})
	query := statementthreads.API(rawQuery)
	createDailySeriesTables(t, ctx, direct)
	const orgID = "org-daily-series-memory"
	const requested, filler, days = 20, 80, 365
	var teams, projects []contextfabric.SubjectRef
	for i := 0; i < requested+filler; i++ {
		teamID := fmt.Sprintf("team-%03d", i)
		workScopeID := fmt.Sprintf("filler-scope-%03d", i)
		if i < requested {
			workScopeID = fmt.Sprintf("proj-%03d", i)
			projectKey := fmt.Sprintf("P%03d", i)
			dailySeriesExec(t, ctx, direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('`+workScopeID+`', '`+orgID+`', 'linear', '`+projectKey+`', 'Project', 1, 'active', '', toDateTime('2026-01-01 00:00:00'))`)
			dailySeriesExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES ('`+teamID+`', '`+teamID+`', NULL, toDateTime('2026-01-01 00:00:00'), '`+orgID+`', 'linear', [], 1)`)
			dailySeriesExec(t, ctx, direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES ('`+orgID+`', 'linear', '`+teamID+`', 'irrelevant', '`+projectKey+`', 'native', toDateTime('2026-01-01 00:00:00'), NULL, toDateTime('2026-01-01 00:00:00'))`)
			teams = append(teams, teamSubject(teamID))
			projects = append(projects, projectSubject("linear", workScopeID))
		}
		for _, kind := range dailySeriesKinds {
			kind.seed(t, ctx, direct, orgID, teamID, workScopeID, days, true)
		}
	}
	providers := devhealthfacts.NewProviders(query)
	for _, tc := range []struct {
		kind     contextfabric.FactKind
		subjects []contextfabric.SubjectRef
		filter   statementFilter
	}{
		{contextfabric.FactFlow, teams, statementFilter{Table: "work_item_metrics_daily", QueryContains: "GROUP BY team_id, day"}},
		{contextfabric.FactFlow, projects, statementFilter{Table: "work_item_metrics_daily", QueryContains: "GROUP BY p.provider, p.id, wm.day"}},
		{contextfabric.FactReadiness, teams, statementFilter{Table: "estimate_coverage_metrics_daily", QueryContains: "GROUP BY team_id, day"}},
		{contextfabric.FactReadiness, projects, statementFilter{Table: "estimate_coverage_metrics_daily", QueryContains: "GROUP BY p.provider, p.id, ec.day"}},
		{contextfabric.FactWorkload, teams, statementFilter{Table: "capacity_forecasts", QueryContains: "GROUP BY team_id, toDate(computed_at)"}},
		{contextfabric.FactWorkload, projects, statementFilter{Table: "capacity_forecasts", QueryContains: "GROUP BY p.provider, p.id, toDate(cf.computed_at)"}},
		{contextfabric.FactHealth, teams, statementFilter{Table: "compounding_risk_daily", QueryContains: "PARTITION BY scope_id, day ORDER BY computed_at DESC"}},
		{contextfabric.FactHealth, projects, statementFilter{Table: "compounding_risk_daily", QueryContains: "GROUP BY project_key, day"}},
	} {
		label := string(tc.kind) + " " + string(tc.subjects[0].Kind) + " daily series"
		read := func() statementMemory {
			mark := serverNowMicros(t, ctx, direct)
			if _, err := findProvider(t, providers, tc.kind).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: tc.kind, Subjects: tc.subjects,
			}); err != nil {
				t.Fatalf("%s: ReadFacts: %v: %s", label, err, readFailureCause(err))
			}
			return statementMemorySince(t, ctx, direct, tc.filter, mark)
		}
		read()
		peaks := read()
		if len(peaks) != 1 {
			t.Fatalf("%s: %d statements matched, want 1: the measurement did not happen; recent server statements:\n%s", label, len(peaks), recentServerStatements(ctx, direct))
		}
		for _, peak := range peaks {
			t.Logf("MEMORY %s: peak %s bytes (%.2f MiB), ceiling %d bytes", label, strconv.FormatUint(peak, 10), float64(peak)/(1<<20), statementMemoryCeiling)
			if peak >= statementMemoryCeiling {
				t.Errorf("%s peaked at %d bytes, want under %d", label, peak, statementMemoryCeiling)
			}
		}
	}
}
