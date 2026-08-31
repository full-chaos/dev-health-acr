package contextfabric

import (
	"context"
	"os"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// TestChaos4637RigCheck_ADeclaredTimeSeriesChartsOnRealData is the executed
// real-data proof that the withdrawn capability is genuinely back: REAL rows
// read live off kiac/dh_0830 (org 70d529e0), declared by a producer, carried
// through the exact production hop (attachCanonicalRows), selected by the
// exact production rule (SelectRenderShapes), and validated by the exact
// production gate (ValidateRenderShapesForResult).
//
// WHY THIS AND NOT ONLY THE END-TO-END RIG. The three subject-kind paths whose
// capability declares `{time_series}` and NOTHING else -- readiness TEAM,
// workload TEAM, metrics REPOSITORY -- are the only ones whose time series
// reaches the wire at all (a fact carrying a legacy breakdown too serves the
// breakdown, per CHAOS-4645). Reaching one of them end-to-end depends on the
// model routing a question to that provider AND minting a claim whose Field is
// one of the table's declared measures, which is interpretation variance, not
// this slice's behaviour. This test stops at exactly the boundary this slice
// touches, on real rows, the same posture (and the same connection recipe)
// chaos4645_rig_check_dual_table_rows_test.go was ruled into.
//
// It uses the CHAOS-4645 daily-flow query verbatim because that read produces
// a genuine per-day series off real ClickHouse; the SHAPE under test is "a
// fact whose only rows-shaped field is a declared time_series", which is
// precisely what workload/readiness emit for a team.
//
// Skips (does not fail) when kiac is unreachable. No testcontainers, no
// compute slot: a read against infrastructure that is already up.
func TestChaos4637RigCheck_ADeclaredTimeSeriesChartsOnRealData(t *testing.T) {
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
	seriesRows, err := queryDailyFlowSeries(ctx, client, orgID, teamID)
	if err != nil {
		t.Skipf("kiac/dh_0830 read failed (%v)", err)
	}
	if len(seriesRows) < 2 {
		t.Fatalf("kiac/dh_0830 (real data): daily series for team %q has %d rows; a trend needs at least 2", teamID, len(seriesRows))
	}
	if len(seriesRows) > MaxFactValueRows {
		seriesRows = seriesRows[:MaxFactValueRows]
	}
	t.Logf("kiac/dh_0830 (real data): team %q -- %d real daily rows", teamID, len(seriesRows))

	// The sole-time_series shape: exactly one rows-shaped field, declared.
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:" + teamID, Label: teamID}
	fact := CanonicalFact{
		Kind: FactFlow, Subject: subject,
		Fields: map[string]FactValue{
			"items_completed": IntegerFactValue(0), // the scalar sibling the model sees
			"daily_flow": TableFactValue(FactTable{
				Shape: FactTableTimeSeries, Key: []string{"day"},
				Measures: []string{"items_started", "items_completed"},
				Rows:     seriesRows,
			}),
		},
	}
	if err := fact.Fields["daily_flow"].Validate(); err != nil {
		t.Fatalf("kiac/dh_0830 (real data): the real rows do not satisfy the declaration the producer makes for them: %v", err)
	}

	// The production hop.
	claims := []ClaimedFact{{ClaimID: "claim_rig_4637_flow", Kind: FactFlow, Subject: subject, Field: "items_completed"}}
	served, _, _, truncated := attachCanonicalRows(claims, []CanonicalFact{fact})
	if truncated {
		t.Fatal("kiac/dh_0830 (real data): the fact failed closed")
	}
	if served[0].Table == nil || served[0].Table.Shape != contractsv1.ContextFabricFactTableShapeTimeSeries {
		t.Fatalf("kiac/dh_0830 (real data): the declaration did not reach the wire: %+v", served[0].Table)
	}
	if len(served[0].Rows) != len(seriesRows) {
		t.Fatalf("kiac/dh_0830 (real data): %d rows served, want the %d real rows", len(served[0].Rows), len(seriesRows))
	}

	// The production rule and the production gate.
	result := InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
		ClaimedFacts:   []ClaimedFact{served[0]},
	}
	shapes, event := SelectRenderShapes(result)
	trend := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleDatedFactTrend)
	if trend == nil {
		t.Fatalf("kiac/dh_0830 (real data): REAL declared rows produced no trend; skipped=%+v", event.Skipped)
	}
	if err := event.Accounted(); err != nil {
		t.Fatalf("kiac/dh_0830 (real data): selector accounting: %v", err)
	}
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("kiac/dh_0830 (real data): the trend's points do not resolve against the real rows they cite: %v", err)
	}
	t.Logf("kiac/dh_0830 (real data): trend selected -- %d points on axis %q, measure %q, every point resolved by exact equality against the row it cites",
		len(trend.Series[0].Points), trend.AxisLabel, trend.Series[0].Key)
}
