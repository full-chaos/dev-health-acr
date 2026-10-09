package devhealthfacts_test

// Measurement: does the repository theme-mix read cost the same bytes with
// cold and warm caches, and how does it grow with the organisation's history?
// read_bytes and read_rows come from the real server's query_log; a
// measurement that did not happen fails, it never skips.

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

const coldWarmRepos = 8

func seedColdWarmOrg(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, units int) {
	t.Helper()
	at := ts(2026, 9, 10, 0, 0, 0)
	for i := 0; i < coldWarmRepos; i++ {
		label := fmt.Sprintf("cw-%d", i)
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
	}
	batch, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)`)
	if err != nil {
		t.Fatalf("prepare batch: %v", err)
	}
	themes := []string{"feature_delivery", "operational", "maintenance", "quality", "risk"}
	for i := 0; i < units; i++ {
		dist := map[string]float64{themes[i%5]: 0.6, themes[(i+2)%5]: 0.4}
		refs := ""
		for k := 0; k < 14; k++ {
			if k > 0 {
				refs += ","
			}
			refs += fmt.Sprintf(`"%s#pr%d"`, repoUUID(fmt.Sprintf("cw-%d", (i+k)%coldWarmRepos)), 1000+i*16+k)
		}
		evidence := fmt.Sprintf(`{"issues":["linear:CH-%d"],"prs":[%s]}`, i, refs)
		// History spreads over ~400 days: only the last 30 are in the asked window.
		from := at.Add(-time.Duration(i%400) * 24 * time.Hour)
		if err := batch.Append(fmt.Sprintf("wu-%06d", i), from, from, nil, float64(1+i%9), dist, map[string]float64{"bugfix": 0.25}, evidence, at, orgID); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send batch: %v", err)
	}
}

type readTotals struct{ statements, readBytes, readRows uint64 }

func orgReadTotals(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) readTotals {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var r readTotals
	if err := direct.QueryRow(ctx, `SELECT count(), sum(read_bytes), sum(read_rows) FROM system.query_log
WHERE type = 'QueryFinish' AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_unit_investments'))`).Scan(&r.statements, &r.readBytes, &r.readRows); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	return r
}

func TestThemeMixReadBytesColdVersusWarmAndByOrgSizeAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)

	const small, large = "org-cw-small", "org-cw-large"
	seedColdWarmOrg(t, ctx, direct, small, 12000)
	seedColdWarmOrg(t, ctx, direct, large, 24000)

	subjects := make([]contextfabric.SubjectRef, 0, coldWarmRepos)
	for i := 0; i < coldWarmRepos; i++ {
		label := fmt.Sprintf("cw-%d", i)
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label})
	}
	start, end := ts(2026, 8, 11, 0, 0, 0), ts(2026, 9, 10, 0, 0, 0)
	measure := func(orgID string) readTotals {
		t.Helper()
		before := orgReadTotals(t, ctx, direct)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts(%s): %v\nserver: %s", orgID, err, lastServerException(ctx, direct))
		}
		if len(result.Facts) == 0 {
			t.Fatalf("no investment facts served for %s", orgID)
		}
		after := orgReadTotals(t, ctx, direct)
		d := readTotals{after.statements - before.statements, after.readBytes - before.readBytes, after.readRows - before.readRows}
		if d.statements == 0 || d.readBytes == 0 {
			t.Fatalf("measurement did not happen for %s: %+v", orgID, d)
		}
		return d
	}
	drop := func() {
		t.Helper()
		for _, s := range []string{`SYSTEM DROP MARK CACHE`, `SYSTEM DROP UNCOMPRESSED CACHE`, `SYSTEM DROP QUERY CONDITION CACHE`} {
			if err := direct.Exec(ctx, s); err != nil {
				t.Logf("%s: %v", s, err)
			}
		}
	}

	drop()
	cold := measure(small)
	warm := measure(small)
	drop()
	coldAgain := measure(small)
	bigCold := func() readTotals { drop(); return measure(large) }()
	bigWarm := measure(large)
	t.Logf("MEASURE small cold:  statements=%d read_bytes=%d read_rows=%d", cold.statements, cold.readBytes, cold.readRows)
	t.Logf("MEASURE small warm:  statements=%d read_bytes=%d read_rows=%d", warm.statements, warm.readBytes, warm.readRows)
	t.Logf("MEASURE small cold2: statements=%d read_bytes=%d read_rows=%d", coldAgain.statements, coldAgain.readBytes, coldAgain.readRows)
	t.Logf("MEASURE large cold:  statements=%d read_bytes=%d read_rows=%d", bigCold.statements, bigCold.readBytes, bigCold.readRows)
	t.Logf("MEASURE large warm:  statements=%d read_bytes=%d read_rows=%d", bigWarm.statements, bigWarm.readBytes, bigWarm.readRows)
	t.Logf("MEASURE large/small read_bytes = %.2fx", float64(bigWarm.readBytes)/float64(warm.readBytes))

	if cold.readBytes != warm.readBytes || coldAgain.readBytes != warm.readBytes {
		t.Errorf("read_bytes depends on cache state: cold=%d warm=%d cold2=%d", cold.readBytes, warm.readBytes, coldAgain.readBytes)
	}
	if bigCold.readBytes != bigWarm.readBytes {
		t.Errorf("large org read_bytes depends on cache state: cold=%d warm=%d", bigCold.readBytes, bigWarm.readBytes)
	}
}

// With a complete membership run recorded, the mix statement also reads
// work_unit_membership. Every table the statement touches counts toward its
// max_bytes_to_read, so the membership table must be read once, not once per
// reference in the scope fragment.
func TestThemeMixTotalReadBytesWithMembershipScopeAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)

	const base, scoped = "org-ms-base", "org-ms-scoped"
	const units, nodesPerUnit = 12000, 6
	seedColdWarmOrg(t, ctx, direct, base, units)
	seedColdWarmOrg(t, ctx, direct, scoped, units)
	at := ts(2026, 9, 10, 0, 0, 0)
	if err := direct.Exec(ctx, `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, scoped, "run-1", at); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	batch, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)`)
	if err != nil {
		t.Fatalf("prepare membership: %v", err)
	}
	for i := 0; i < units; i++ {
		for n := 0; n < nodesPerUnit; n++ {
			if err := batch.Append(scoped, "repo", fmt.Sprintf("cw-%d", (i+n)%coldWarmRepos), fmt.Sprintf("wu-%06d", i), "theme", "feature_delivery", at, "run-1"); err != nil {
				t.Fatalf("append membership: %v", err)
			}
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send membership: %v", err)
	}

	subjects := make([]contextfabric.SubjectRef, 0, coldWarmRepos)
	for i := 0; i < coldWarmRepos; i++ {
		label := fmt.Sprintf("cw-%d", i)
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label})
	}
	start, end := ts(2026, 8, 11, 0, 0, 0), ts(2026, 9, 10, 0, 0, 0)
	read := func(orgID string) readTotals {
		t.Helper()
		before := orgReadTotals(t, ctx, direct)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts(%s): %v\nserver: %s", orgID, err, lastServerException(ctx, direct))
		}
		if len(result.Facts) == 0 {
			t.Fatalf("no investment facts served for %s", orgID)
		}
		after := orgReadTotals(t, ctx, direct)
		return readTotals{after.statements - before.statements, after.readBytes - before.readBytes, after.readRows - before.readRows}
	}
	baseline := read(base)
	withScope := read(scoped)
	if baseline.readBytes == 0 || withScope.readBytes == 0 {
		t.Fatalf("measurement did not happen: base=%+v scoped=%+v", baseline, withScope)
	}

	// One pass over the membership columns the scope reads.
	before := orgReadTotalsAll(t, ctx, direct)
	var rows uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM (SELECT DISTINCT work_unit_id FROM work_unit_membership WHERE org_id = ? AND run_id = 'run-1')`, scoped).Scan(&rows); err != nil {
		t.Fatalf("membership pass: %v", err)
	}
	after := orgReadTotalsAll(t, ctx, direct)
	onePass := after - before
	if onePass == 0 {
		t.Fatalf("measurement did not happen: membership single pass read 0 bytes")
	}
	t.Logf("MEASURE investments-only=%d scoped-total=%d membership-one-pass=%d membership-in-statement=%d ratio=%.2fx statements=%d",
		baseline.readBytes, withScope.readBytes, onePass, int64(withScope.readBytes)-int64(baseline.readBytes),
		float64(int64(withScope.readBytes)-int64(baseline.readBytes))/float64(onePass), withScope.statements)
	t.Errorf("MEASUREMENT-REPORT temporary")
	if float64(withScope.readBytes) > 1.3*float64(baseline.readBytes+onePass) {
		t.Fatalf("statement read %d bytes with membership scope, want <= 1.3 x (investments %d + one membership pass %d): membership is scanned more than once",
			withScope.readBytes, baseline.readBytes, onePass)
	}
}

func orgReadTotalsAll(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) uint64 {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var b uint64
	if err := direct.QueryRow(ctx, `SELECT sum(read_bytes) FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND has(tables, concat(currentDatabase(), '.work_unit_membership'))`).Scan(&b); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	return b
}
