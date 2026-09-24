package devhealthfacts_test

// CHAOS-6594: the repository and team theme-mix reads must scan
// work_unit_investments ONCE. The latest-row selection (argMax per work unit)
// is an inlined CTE in ClickHouse, so every extra reference re-scans the
// table; on trial that made the read 71 MiB against a 10.69 MiB table and the
// read-only user's 64 MiB max_bytes_to_read rejected it (error 307).
//
// The measurement is EXECUTED: read_bytes comes from the real server's
// query_log for every statement that touched work_unit_investments during the
// ReadFacts call. A measurement that did not happen (no log row, log
// disabled, table too small to mean anything) fails; it never skips.

import (
	"context"
	"fmt"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	chaos6594Repos       = 8
	chaos6594Units       = 12000
	chaos6594MinTableMiB = 10   // the trial table was 10.69 MiB; a smaller fixture proves nothing
	chaos6594MaxFactor   = 1.25 // read bytes may not exceed this multiple of the table's uncompressed size; one pass measures 0.97x, a second read of one wide column 1.79x
)

func seedCHAOS6594(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string) {
	t.Helper()
	at := ts(2026, 9, 10, 0, 0, 0)
	for i := 0; i < chaos6594Repos; i++ {
		label := fmt.Sprintf("mix-%d", i)
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", "team-bytes", repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
	}
	batch, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)`)
	if err != nil {
		t.Fatalf("prepare batch: %v", err)
	}
	themes := []string{"feature_delivery", "operational", "maintenance", "quality", "risk"}
	for i := 0; i < chaos6594Units; i++ {
		dist := map[string]float64{themes[i%5]: 0.6, themes[(i+2)%5]: 0.4}
		sub := map[string]float64{"bugfix": 0.25}
		// Realistic evidence: many PR refs across several repositories plus
		// issue-side refs, so the row width matches what trial carried.
		refs := ""
		for k := 0; k < 14; k++ {
			if k > 0 {
				refs += ","
			}
			refs += fmt.Sprintf(`"%s#pr%d"`, repoUUID(fmt.Sprintf("mix-%d", (i+k)%chaos6594Repos)), 1000+i*16+k)
		}
		evidence := fmt.Sprintf(`{"issues":["ghpr:acme/mix-%d#%d","linear:CH-%d","jira:OPS-%d"],"prs":[%s]}`, i%chaos6594Repos, 5000+i, i, i, refs)
		from := at.Add(-time.Duration(i%20) * 24 * time.Hour)
		// Append-only history: every third unit was recomputed, so argMax has
		// two versions to choose between.
		versions := 1
		if i%3 == 0 {
			versions = 2
		}
		for v := 0; v < versions; v++ {
			effort := float64(1 + i%9)
			if v == 0 && versions == 2 {
				effort = 1000 // stale version: must never be counted
			}
			if err := batch.Append(fmt.Sprintf("wu-%05d", i), from, at, nil, effort, dist, sub, evidence, at.Add(time.Duration(v)*time.Hour), orgID); err != nil {
				t.Fatalf("append: %v", err)
			}
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send batch: %v", err)
	}
}

// workUnitInvestmentsBytes is the table's uncompressed size on the server.
func workUnitInvestmentsBytes(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) uint64 {
	t.Helper()
	var size uint64
	row := direct.QueryRow(ctx, `SELECT sum(data_uncompressed_bytes) FROM system.parts WHERE database = currentDatabase() AND table = 'work_unit_investments' AND active`)
	if err := row.Scan(&size); err != nil {
		t.Fatalf("read table size: %v", err)
	}
	return size
}

type chaos6594Measurement struct {
	statements int
	readBytes  uint64
}

// queryLogTotals is the cumulative (statements, read_bytes) the server has
// logged for SELECTs that read work_unit_investments. Measuring as a
// before/after delta keeps the result independent of client/server clock and
// timezone handling.
func queryLogTotals(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) (statements, readBytes uint64) {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
	}
	if err := direct.QueryRow(ctx, `SELECT count(), sum(read_bytes) FROM system.query_log
WHERE type = 'QueryFinish' AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_unit_investments'))`).Scan(&statements, &readBytes); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	return statements, readBytes
}

// measureWorkUnitInvestmentsReads runs fn and returns what the server logged
// for statements reading work_unit_investments while fn ran.
func measureWorkUnitInvestmentsReads(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, fn func()) chaos6594Measurement {
	t.Helper()
	beforeStatements, beforeBytes := queryLogTotals(t, ctx, direct)
	fn()
	afterStatements, afterBytes := queryLogTotals(t, ctx, direct)
	return chaos6594Measurement{statements: int(afterStatements - beforeStatements), readBytes: afterBytes - beforeBytes}
}

func TestThemeMixReadsScanWorkUnitInvestmentsOnceAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	const orgID = "org-bytes"
	seedCHAOS6594(t, ctx, direct, orgID)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)

	table := workUnitInvestmentsBytes(t, ctx, direct)
	if table < chaos6594MinTableMiB<<20 {
		t.Fatalf("fixture table is %d bytes, want >= %d MiB (a small fixture cannot show a re-scan)", table, chaos6594MinTableMiB)
	}
	t.Logf("work_unit_investments uncompressed: %d bytes", table)

	subjects := func(kind string) []contextfabric.SubjectRef {
		if kind == "team" {
			return []contextfabric.SubjectRef{teamSubject("team-bytes")}
		}
		out := make([]contextfabric.SubjectRef, 0, chaos6594Repos)
		for i := 0; i < chaos6594Repos; i++ {
			label := fmt.Sprintf("mix-%d", i)
			out = append(out, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label})
		}
		return out
	}
	for _, kind := range []string{"repository", "team"} {
		t.Run(kind, func(t *testing.T) {
			var facts int
			m := measureWorkUnitInvestmentsReads(t, ctx, direct, func() {
				result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
					Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects(kind),
				})
				if err != nil {
					t.Fatalf("ReadFacts: %v\nserver: %s", err, lastServerException(ctx, direct))
				}
				facts = len(result.Facts)
			})
			if facts == 0 {
				t.Fatal("no investment facts served")
			}
			if m.statements == 0 || m.readBytes == 0 {
				t.Fatalf("measurement did not happen: %d statements, %d bytes in query_log", m.statements, m.readBytes)
			}
			t.Logf("%s: %d statement(s) read %d bytes = %.2fx table", kind, m.statements, m.readBytes, float64(m.readBytes)/float64(table))
			if float64(m.readBytes) > chaos6594MaxFactor*float64(table) {
				t.Fatalf("%s mix read %d bytes = %.2fx the %d-byte table, want <= %.2fx (latest-row selection must be one pass)",
					kind, m.readBytes, float64(m.readBytes)/float64(table), table, chaos6594MaxFactor)
			}
		})
	}
}

func lastServerException(ctx context.Context, direct clickhousedriver.Conn) string {
	_ = direct.Exec(ctx, `SYSTEM FLUSH LOGS`)
	var text string
	if err := direct.QueryRow(ctx, `SELECT exception FROM system.query_log WHERE type IN ('ExceptionBeforeStart','ExceptionWhileProcessing') ORDER BY event_time_microseconds DESC LIMIT 1`).Scan(&text); err != nil {
		return "no server exception recorded: " + err.Error()
	}
	return text
}

// A range request reads the current AND the prior comparable window; both
// come from the SAME single pass, each window picks the latest version of a
// unit (append-only history, argMax), and a unit that overlaps both windows
// counts in each.
func TestTeamThemeMixCurrentAndPriorWindowsShareOnePassAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-two-windows"
	at := ts(2026, 9, 2, 0, 0, 0)
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("w-a"), orgID, "acme/w-a", "github", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-w", repoUUID("w-a"), "acme/w-a", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at); err != nil {
		t.Fatalf("seed ownership: %v", err)
	}
	seed := func(id string, from, to time.Time, computedAt time.Time, effort float64, themes map[string]float64) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, from, to, effort, themes, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":["%s#pr1"]}`, repoUUID("w-a")), computedAt, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", id, err)
		}
	}
	feature := map[string]float64{"feature_delivery": 1.0}
	risk := map[string]float64{"risk": 1.0}
	seed("cur", ts(2026, 8, 10, 0, 0, 0), ts(2026, 8, 10, 0, 0, 0), at, 10, feature)
	seed("prior", ts(2026, 7, 10, 0, 0, 0), ts(2026, 7, 10, 0, 0, 0), at, 8, risk)
	seed("prior-stale", ts(2026, 7, 12, 0, 0, 0), ts(2026, 7, 12, 0, 0, 0), at.Add(-time.Hour), 999, risk)
	seed("prior-stale", ts(2026, 7, 12, 0, 0, 0), ts(2026, 7, 12, 0, 0, 0), at, 2, risk)
	seed("both", ts(2026, 7, 20, 0, 0, 0), ts(2026, 8, 5, 0, 0, 0), at, 4, feature)
	seed("outside", ts(2026, 5, 1, 0, 0, 0), ts(2026, 5, 2, 0, 0, 0), at, 500, risk)

	start, end := ts(2026, 8, 1, 0, 0, 0), ts(2026, 9, 1, 0, 0, 0)
	var facts []contextfabric.CanonicalFact
	m := measureWorkUnitInvestmentsReads(t, ctx, direct, func() {
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
			Kind:     contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{teamSubject("team-w")},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v\nserver: %s", err, lastServerException(ctx, direct))
		}
		facts = result.Facts
	})
	if m.statements != 1 {
		t.Fatalf("range read issued %d statements over work_unit_investments, want 1 pass for both windows", m.statements)
	}
	var found bool
	for _, f := range facts {
		cur, ok := f.Fields[contextfabric.FactFieldTheme(contextfabric.ThemeFeatureDelivery)]
		if !ok {
			continue
		}
		found = true
		// current window: cur (10) + both (4), all feature.
		if cur.Number == nil || *cur.Number != 1.0 {
			t.Fatalf("current feature share = %v, want 1.0", cur.Number)
		}
		// prior window: prior (8 risk) + prior-stale's LATEST (2 risk) + both
		// (4 feature); outside excluded; the stale 999 never counted.
		priorRisk := f.Fields[contextfabric.FactFieldPriorTheme(contextfabric.ThemeRisk)].Number
		priorFeature := f.Fields[contextfabric.FactFieldPriorTheme(contextfabric.ThemeFeatureDelivery)].Number
		if priorRisk == nil || priorFeature == nil || *priorRisk < 10.0/14-1e-9 || *priorRisk > 10.0/14+1e-9 || *priorFeature < 4.0/14-1e-9 || *priorFeature > 4.0/14+1e-9 {
			t.Fatalf("prior shares risk=%v feature=%v, want %v / %v", priorRisk, priorFeature, 10.0/14, 4.0/14)
		}
	}
	if !found {
		t.Fatalf("no team fact carries theme fields: %#v", facts)
	}
}

// More repositories than one chunk are read in several statements, each a
// single pass, and none is dropped.
func TestRepositoryThemeMixAcrossChunksIsCompleteAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-chunks"
	const n = 100 // > one chunk of repositories
	at := ts(2026, 9, 2, 0, 0, 0)
	repoBatch, err := direct.PrepareBatch(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced)`)
	if err != nil {
		t.Fatalf("prepare repos: %v", err)
	}
	wuBatch, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)`)
	if err != nil {
		t.Fatalf("prepare wu: %v", err)
	}
	subjects := make([]contextfabric.SubjectRef, 0, n)
	for i := 0; i < n; i++ {
		label := fmt.Sprintf("chunk-%03d", i)
		if err := repoBatch.Append(repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatalf("append repo: %v", err)
		}
		if err := wuBatch.Append("wu-"+label, at, at, nil, 2.0, map[string]float64{"operational": 1.0}, map[string]float64{},
			fmt.Sprintf(`{"issues":[],"prs":["%s#pr1"]}`, repoUUID(label)), at, orgID); err != nil {
			t.Fatalf("append wu: %v", err)
		}
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label})
	}
	if err := repoBatch.Send(); err != nil {
		t.Fatalf("send repos: %v", err)
	}
	if err := wuBatch.Send(); err != nil {
		t.Fatalf("send wu: %v", err)
	}
	var facts int
	m := measureWorkUnitInvestmentsReads(t, ctx, direct, func() {
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v\nserver: %s", err, lastServerException(ctx, direct))
		}
		facts = len(result.Facts)
	})
	if facts != n {
		t.Fatalf("facts = %d, want %d (no repository may be dropped across chunks)", facts, n)
	}
	if m.statements != 2 {
		t.Fatalf("statements = %d, want 2 (100 repositories in chunks of 90)", m.statements)
	}
}
