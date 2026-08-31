package contextfabric

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// TestChaos4645RigCheck_DualTableFlowFactStillServesLegacyRowsOnRealData is
// the executed rig check main's ruling requires before merge: a REAL
// dual-table CanonicalFact -- carrying BOTH scope_breakdown (the
// pre-CHAOS-4645 legacy breakdown FactFlow already emitted) and daily_flow
// (the new time_series this ticket adds) -- built from rows read live off
// kiac/dh_0830 (real data, org 70d529e0), proves the CHAOS-4645 ambiguity
// fix in attachCanonicalRows/canonicalFieldRows serves the legacy field's
// rows rather than dropping them.
//
// This is NOT devhealthfacts.FlowProvider run end-to-end -- that package
// cannot be imported from here (it imports THIS package, so doing so would
// be a build cycle; chaos4099_fact_scope_test.go's own doc comment records
// the same constraint). FlowProvider producing this exact dual-table shape
// from these exact tables is separately, already proven: the SQL below is
// copied verbatim from flow.go's queryTeamScopeRows/queryTeamFlowDailySeries
// (also proven directly against this same cluster, same team, during this
// session), and chaos4645_flow_daily_series_integration_test.go proves
// FlowProvider.ReadFacts assembles the two into one CanonicalFact. What
// THIS test proves, and only this test needs to prove, is the reader fix:
// given that real dual-table fact, does attachCanonicalRows resolve the
// ambiguity by declaration rather than dropping the row table -- so it
// builds the fact directly from real rows rather than reaching through
// FlowProvider, and stops at exactly the boundary the fix touches (see
// main's ruling for why (b) was chosen over standing up the full rig).
//
// Connects directly to the already-running kiac ClickHouse over the
// network (native protocol, port 30502) -- no testcontainers, no compute
// slot: this is a read against infrastructure that is already up, not a
// new container being started.
//
// Skips (does not fail) when kiac is unreachable, so this stays runnable
// in CI/any environment without kiac reachable -- the same opt-in
// convention TestCHAOS3783PruningMeasurement already uses in this repo for
// a real-cluster-only check.
func TestChaos4645RigCheck_DualTableFlowFactStillServesLegacyRowsOnRealData(t *testing.T) {
	dsn := os.Getenv("ACR_CHAOS4645_KIAC_DSN")
	if dsn == "" {
		dsn = "clickhouse://ch:acr-trial-dev@192.168.65.4:30502/dh_0830"
	}
	orgID := os.Getenv("ACR_CHAOS4645_KIAC_ORG_ID")
	if orgID == "" {
		orgID = "70d529e0-3c06-4597-8480-794fd02328b6"
	}
	teamID := os.Getenv("ACR_CHAOS4645_KIAC_TEAM_ID")
	if teamID == "" {
		teamID = "CHAOS"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: dsn, DialTimeout: 5 * time.Second, QueryTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Skipf("kiac/dh_0830 not reachable (%v) -- set ACR_CHAOS4645_KIAC_DSN to run this rig check", err)
	}

	legacyRows, err := queryLegacyScopeBreakdown(ctx, client, orgID, teamID)
	if err != nil {
		t.Skipf("kiac/dh_0830 read failed (%v) -- environment or data may have changed; not a code defect this test asserts", err)
	}
	seriesRows, err := queryDailyFlowSeries(ctx, client, orgID, teamID)
	if err != nil {
		t.Skipf("kiac/dh_0830 read failed (%v)", err)
	}
	if len(legacyRows) == 0 {
		t.Fatalf("kiac/dh_0830 (real data): scope_breakdown has no rows for team %q -- pick a team with real work_item_metrics_daily activity", teamID)
	}
	if len(seriesRows) == 0 {
		t.Fatalf("kiac/dh_0830 (real data): daily_flow has no rows for team %q", teamID)
	}
	// capFactValueRows' own bound (production caps every table to
	// MaxFactValueRows before constructing the CanonicalFact -- see
	// devhealthfacts/shared.go's capFactValueRows) -- kiac's real team
	// "CHAOS" has more than 64 days of history, so this test must apply the
	// same cap production does, or FactValue.Validate() rejects the whole
	// table outright rather than reflecting what a real read actually ships.
	if len(legacyRows) > MaxFactValueRows {
		legacyRows = legacyRows[:MaxFactValueRows]
	}
	if len(seriesRows) > MaxFactValueRows {
		seriesRows = seriesRows[:MaxFactValueRows]
	}
	t.Logf("kiac/dh_0830 (real data): team %q -- scope_breakdown %d rows, daily_flow %d rows (dual-table case)", teamID, len(legacyRows), len(seriesRows))

	// The exact dual-table shape flow.go's readTeamFlow builds: a real
	// CanonicalFact carrying both the legacy breakdown and the new
	// time_series, both TableFactValue-declared, off REAL rows.
	teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:" + teamID, Label: teamID}
	fact := CanonicalFact{
		Kind: FactFlow, Subject: teamSubject,
		Fields: map[string]FactValue{
			"scope_breakdown": TableFactValue(FactTable{
				Shape: FactTableBreakdown, Key: []string{"provider", "work_scope_id"},
				Measures: []string{"day", "items_started", "items_completed"}, Rows: legacyRows,
			}),
			"daily_flow": TableFactValue(FactTable{
				Shape: FactTableTimeSeries, Key: []string{"day"},
				Measures: []string{"items_started", "items_completed"}, Rows: seriesRows,
			}),
		},
	}
	if err := fact.Fields["scope_breakdown"].Validate(); err != nil {
		t.Fatalf("kiac/dh_0830 (real data): scope_breakdown fails FactValue.Validate(): %v", err)
	}
	if err := fact.Fields["daily_flow"].Validate(); err != nil {
		t.Fatalf("kiac/dh_0830 (real data): daily_flow fails FactValue.Validate(): %v", err)
	}

	// This is the actual production call chain: a real model-authored claim
	// citing FactFlow for this team, run through attachCanonicalRows exactly
	// as RuntimeAnswerSynthesizer does.
	claims := []ClaimedFact{{Kind: FactFlow, Subject: teamSubject}}
	got, rowsCount, byKind, truncated := attachCanonicalRows(claims, []CanonicalFact{fact})
	if truncated {
		t.Fatalf("kiac/dh_0830 (real data): attachCanonicalRows reported truncated=true, want false -- the dual-table ambiguity must resolve to the legacy field, not fail closed")
	}
	if rowsCount == 0 || byKind[FactFlow] == 0 {
		t.Fatalf("kiac/dh_0830 (real data): rowsCount=%d byKind[flow]=%d, want > 0 -- Rows must arrive on the claim, not stay nil", rowsCount, byKind[FactFlow])
	}
	if len(got) != 1 || len(got[0].Rows) == 0 {
		t.Fatalf("kiac/dh_0830 (real data): claim.Rows = %#v, want the legacy scope_breakdown rows attached", got)
	}
	if len(got[0].Rows) != len(legacyRows) {
		t.Fatalf("kiac/dh_0830 (real data): claim carries %d rows, want exactly the legacy field's %d rows (the new time_series field must NOT be what gets served)", len(got[0].Rows), len(legacyRows))
	}
	t.Logf("kiac/dh_0830 (real data): team %q -- claim.Rows = %d rows (from scope_breakdown, the legacy field; daily_flow independently carried %d rows on the same fact) -- Rows=nil regression is closed", teamID, len(got[0].Rows), len(seriesRows))
}

// queryLegacyScopeBreakdown mirrors flow.go's queryTeamScopeRows exactly
// (the pre-CHAOS-4645 latest-per-scope read) against real kiac data.
func queryLegacyScopeBreakdown(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID, teamID string) ([]FactValueRow, error) {
	statement := `SELECT toString(provider), toString(work_scope_id), toString(day), toInt64(items_started), toInt64(items_completed)
FROM (
	SELECT team_id, provider, work_scope_id, day, items_started, items_completed,
		row_number() OVER (PARTITION BY team_id, provider, work_scope_id ORDER BY day DESC, computed_at DESC) AS rn
	FROM work_item_metrics_daily
	WHERE org_id = {org_id:String} AND toString(team_id) IN {ids:Array(String)}
)
WHERE rn = 1
ORDER BY team_id, work_scope_id, provider
LIMIT 200`
	rowsCursor, err := client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID}, {Name: "ids", Value: []string{teamID}},
	})
	if err != nil {
		return nil, err
	}
	defer rowsCursor.Close()
	var out []FactValueRow
	for rowsCursor.Next() {
		var provider, workScopeID, day string
		var itemsStarted, itemsCompleted int64
		if err := rowsCursor.Scan(&provider, &workScopeID, &day, &itemsStarted, &itemsCompleted); err != nil {
			return nil, err
		}
		out = append(out, FactValueRow{Fields: map[string]FactValue{
			"provider": StringFactValue(provider), "work_scope_id": StringFactValue(workScopeID),
			"day": StringFactValue(day), "items_started": IntegerFactValue(itemsStarted), "items_completed": IntegerFactValue(itemsCompleted),
		}})
	}
	return out, rowsCursor.Err()
}

// queryDailyFlowSeries mirrors flow.go's queryTeamFlowDailySeries exactly
// (the CHAOS-4645 per-day read) against real kiac data.
func queryDailyFlowSeries(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID, teamID string) ([]FactValueRow, error) {
	statement := `SELECT toString(team_id), toString(day), toInt64(sum(items_started)), toInt64(sum(items_completed))
FROM (
	SELECT team_id, provider, work_scope_id, day, items_started, items_completed,
		row_number() OVER (PARTITION BY team_id, provider, work_scope_id, day ORDER BY computed_at DESC) AS rn
	FROM work_item_metrics_daily
	WHERE org_id = {org_id:String} AND toString(team_id) IN {ids:Array(String)}
)
WHERE rn = 1
GROUP BY team_id, day
ORDER BY team_id, day DESC
LIMIT 200`
	rowsCursor, err := client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID}, {Name: "ids", Value: []string{teamID}},
	})
	if err != nil {
		return nil, err
	}
	defer rowsCursor.Close()
	var out []FactValueRow
	for rowsCursor.Next() {
		var team, day string
		var itemsStarted, itemsCompleted int64
		if err := rowsCursor.Scan(&team, &day, &itemsStarted, &itemsCompleted); err != nil {
			return nil, err
		}
		out = append(out, FactValueRow{Fields: map[string]FactValue{
			"day": StringFactValue(day), "items_started": IntegerFactValue(itemsStarted), "items_completed": IntegerFactValue(itemsCompleted),
		}})
	}
	return out, rowsCursor.Err()
}
