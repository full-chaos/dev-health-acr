package devhealthfacts_test

// The phases of the project mix that read a wide column for every unit -- the
// roll-up evidence arm and the native placement (structural_evidence_json), the
// roll-up repo bugfix and the native bugfix values (subcategory_distribution_json)
// -- must keep headroom under half of max_bytes_to_read as the table grows, and
// whatever share of the table the requested window holds. The cells run the
// production read on the prod-shaped fixture of
// chaos7257_project_theme_mix_read_bytes_integration_test.go and read each
// statement's read_bytes from the server's query_log.
//
// The membership scope load (ReadMembershipScopeUnits) is not a phase of the
// mix: it reads work_unit_membership, not work_unit_investments, and is not
// measured here.

import (
	"context"
	"strings"
	"testing"
	"time"

	clickhousedriverv2 "github.com/ClickHouse/clickhouse-go/v2"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// projectMixLine is the per-statement line: half of the prod cap.
const projectMixLine = chaos7257ProdMaxBytesToRead / 2

// projectMixHeadroomLimit is what a wide-column phase statement may read where
// headroom is required: 60% of the line, so 40% of it stays free.
const projectMixHeadroomLimit = projectMixLine * 6 / 10

// projectMixWidePhases are the phases that read a wide column for every unit.
var projectMixWidePhases = []string{"rollup_evidence_arm", "native_placement", "rollup_repo_bugfix", "native_bugfix_values"}

// projectMixPhaseOf names the phase of a project mix statement by its text.
func projectMixPhaseOf(query string) string {
	for _, c := range []struct{ marker, phase string }{
		{"AS pinned_present", "pinned_presence"},
		{"AS excluded_no_repo_link", "rollup_evidence_arm"},
		{"groupArray(multi_placed)", "native_placement"},
		{"AS bugfix_weighted", "rollup_repo_bugfix"},
		{"groupArray(bugfix_share)", "native_bugfix_values"},
		{"groupArray(theme_feature_delivery)", "native_theme_values"},
		{"AS feature_delivery", "rollup_repo_themes"},
		{"groupArrayIf(work_unit_id, in_window)", "scope"},
		{"AS chunk_bounds", "chunk_bounds"},
	} {
		if strings.Contains(query, c.marker) {
			return c.phase
		}
	}
	return "unclassified"
}

type projectMixStatement struct {
	Phase, Type string
	Code        int32
	ReadBytes   uint64
	// InSetIndex is the statement's use_index_for_in_with_subqueries setting as
	// query_log recorded it ("" when the statement ran with the default).
	InSetIndex string
}

// projectMixStatementsSince returns the project mix statements over
// work_unit_investments logged after markMicros, classified by phase.
func projectMixStatementsSince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, markMicros int64) []projectMixStatement {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT type, exception_code, read_bytes, query, Settings['use_index_for_in_with_subqueries'] FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_unit_investments'))
ORDER BY event_time_microseconds`, markMicros)
	if err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	defer rows.Close()
	var out []projectMixStatement
	for rows.Next() {
		var s projectMixStatement
		var query string
		if err := rows.Scan(&s.Type, &s.Code, &s.ReadBytes, &query, &s.InSetIndex); err != nil {
			t.Fatalf("scan query_log: %v", err)
		}
		s.Phase = projectMixPhaseOf(query)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read query_log rows (measurement did not happen): %v", err)
	}
	return out
}

type projectMixHeadroomCase struct {
	units int
	// quarterInWindow moves three units in four (by a hash of the unit id, so the
	// in-window units are spread over the whole key range) 90 days back, and
	// reads a 30-day window that holds the remaining quarter.
	quarterInWindow bool
	// dsnSettings are ClickHouse settings the production query client sends with
	// every statement (DSN query string).
	dsnSettings string
}

// readProjectMixHeadroom runs the production project investment read for the
// fixture's five projects under the prod cap and returns its statements.
func readProjectMixHeadroom(t *testing.T, c projectMixHeadroomCase) ([]projectMixStatement, error) {
	t.Helper()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, func(o *runtimeclickhouse.Options) {
		capBytes := chaos7257ProdMaxBytesToRead
		o.MaxBytesToRead = &capBytes
		rows := uint(1_000_000)
		o.MaxResultRows = &rows
		if c.dsnSettings != "" {
			o.DSN += "?" + c.dsnSettings
		}
	})
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-project-mix-headroom"
	shape := seedCHAOS7257ProdShape(t, ctx, direct, orgID, c.units)
	window := investmentAllHistory()
	if c.quarterInWindow {
		syncCtx := clickhousedriverv2.Context(ctx, clickhousedriverv2.WithSettings(clickhousedriverv2.Settings{"mutations_sync": 2}))
		if err := direct.Exec(syncCtx, `ALTER TABLE work_unit_investments
UPDATE from_ts = from_ts - INTERVAL 90 DAY, to_ts = to_ts - INTERVAL 90 DAY
WHERE org_id = ? AND cityHash64(work_unit_id) % 4 != 0`, orgID); err != nil {
			t.Fatalf("move three units in four out of the window: %v", err)
		}
		var in, all uint64
		if err := direct.QueryRow(ctx, `SELECT countIf(to_ts >= ?), count() FROM work_unit_investments WHERE org_id = ?`, shape.at.Add(-30*24*time.Hour), orgID).Scan(&in, &all); err != nil {
			t.Fatalf("count in-window rows: %v", err)
		}
		if share := float64(in) / float64(all); share < 0.20 || share > 0.30 {
			t.Fatalf("fixture: %d of %d rows in the window (%.1f%%), want about 25%%", in, all, 100*share)
		}
		start, end := shape.at.Add(-30*24*time.Hour), shape.at.Add(24*time.Hour)
		window = contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}
	}
	provider := findProvider(t, devhealthfacts.NewInstrumentedProviders(query, nil), contextfabric.FactInvestment)
	subjects := make([]contextfabric.SubjectRef, 0, len(shape.projects))
	for _, project := range shape.projects {
		subjects = append(subjects, projectSubject("linear", project))
	}
	mark := serverNowMicros(t, ctx, direct)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{Time: window, Kind: contextfabric.FactInvestment, Subjects: subjects})
	statements := projectMixStatementsSince(t, ctx, direct, mark)
	for i, s := range statements {
		t.Logf("statement %d: %s %s code=%d read_bytes=%d (%.1f%% of the line) use_index_for_in_with_subqueries=%q",
			i, s.Phase, s.Type, s.Code, s.ReadBytes, 100*float64(s.ReadBytes)/float64(projectMixLine), s.InSetIndex)
	}
	if err == nil {
		served := 0
		for _, fact := range result.Facts {
			if _, ok := fact.Fields["theme_feature_delivery"]; ok {
				served++
			}
		}
		if served != len(shape.projects) {
			t.Errorf("served %d project investment facts with a mix, want %d (reason %q)", served, len(shape.projects), result.Reason)
		}
	}
	return statements, err
}

// checkWidePhasesUnder fails every wide-column phase statement at or over
// limit bytes, and a read in which a wide-column phase did not run at all.
func checkWidePhasesUnder(t *testing.T, statements []projectMixStatement, limit uint64) {
	t.Helper()
	seen := map[string]int{}
	for i, s := range statements {
		if s.Phase == "unclassified" {
			t.Errorf("statement %d over work_unit_investments is no known phase of the project mix", i)
		}
		if s.Type != "QueryFinish" {
			t.Errorf("statement %d (%s) ended %s code %d", i, s.Phase, s.Type, s.Code)
		}
		for _, wide := range projectMixWidePhases {
			if s.Phase != wide {
				continue
			}
			seen[wide]++
			if s.ReadBytes > limit {
				t.Errorf("statement %d (%s) read %d bytes = %.1f%% of the line, over the %d-byte limit (%.1f%% of the line)",
					i, s.Phase, s.ReadBytes, 100*float64(s.ReadBytes)/float64(projectMixLine), limit, 100*float64(limit)/float64(projectMixLine))
			}
		}
	}
	for _, wide := range projectMixWidePhases {
		if seen[wide] == 0 {
			t.Errorf("no %s statement was logged: the cell measured nothing for it", wide)
		}
	}
}

func TestProjectMixWidePhasesKeepHeadroomAtTwiceTheTableAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	statements, err := readProjectMixHeadroom(t, projectMixHeadroomCase{units: chaos7271GrowthUnits})
	if err != nil {
		t.Fatalf("ReadFacts: %v (cause %s)", err, readFailureCause(err))
	}
	checkWidePhasesUnder(t, statements, projectMixHeadroomLimit)
}

// At four times the prod table every wide-column phase statement stays under
// the line and the read succeeds. The cell covers those phases only: the theme
// phases, the scope and the pinned presence are logged, not bounded, and the
// membership scope load is not a statement over work_unit_investments.
func TestProjectMixWidePhasesStayUnderTheLineAtFourTimesTheTableAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	statements, err := readProjectMixHeadroom(t, projectMixHeadroomCase{units: 4 * chaos7257Units})
	if err != nil {
		t.Fatalf("ReadFacts at four times the table: %v (cause %s)", err, readFailureCause(err))
	}
	checkWidePhasesUnder(t, statements, projectMixLine-1)
}

// The headroom comes from the key range of the statement, not from the
// server's index analysis of an IN set: with that analysis off, the bound holds.
// Every statement must show the setting in query_log, or the cell proves
// nothing.
func TestProjectMixWidePhasesKeepHeadroomWithoutInSetIndexAnalysisAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	statements, err := readProjectMixHeadroom(t, projectMixHeadroomCase{units: chaos7271GrowthUnits, dsnSettings: "use_index_for_in_with_subqueries=0"})
	if err != nil {
		t.Fatalf("ReadFacts: %v (cause %s)", err, readFailureCause(err))
	}
	for i, s := range statements {
		if s.InSetIndex != "0" {
			t.Errorf("statement %d (%s) ran with use_index_for_in_with_subqueries=%q, want \"0\"", i, s.Phase, s.InSetIndex)
		}
	}
	checkWidePhasesUnder(t, statements, projectMixHeadroomLimit)
}

// A window that holds a quarter of the units, spread over the whole key range,
// must not widen what one statement reads: at four times the table every
// wide-column phase statement keeps the same headroom.
func TestProjectMixWidePhasesKeepHeadroomWhenAQuarterOfTheUnitsIsInTheWindowAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	statements, err := readProjectMixHeadroom(t, projectMixHeadroomCase{units: 4 * chaos7257Units, quarterInWindow: true})
	if err != nil {
		t.Fatalf("ReadFacts: %v (cause %s)", err, readFailureCause(err))
	}
	checkWidePhasesUnder(t, statements, projectMixHeadroomLimit)
}
