package devhealthfacts_test

// The repository theme-mix statement reads work_unit_investments and, when the
// organization has a complete membership run, work_unit_membership. Every
// table the statement reads counts toward its max_bytes_to_read, so these rows
// measure the real server's read_bytes (system.query_log): the bytes do not
// depend on cache state, the membership scope is read once and then
// remembered, and a statement over its budget is refused by name with the
// measured facts. A measurement that did not happen fails; it never skips.

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
	budgetFixtureRepos        = 8
	budgetFixtureUnits        = 12000
	budgetFixtureNodesPerUnit = 9
)

func seedBudgetFixtureOrg(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, units int, withMembership bool) {
	t.Helper()
	at := ts(2026, 9, 10, 0, 0, 0)
	for i := 0; i < budgetFixtureRepos; i++ {
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
			refs += fmt.Sprintf(`"%s#pr%d"`, repoUUID(fmt.Sprintf("cw-%d", (i+k)%budgetFixtureRepos)), 1000+i*16+k)
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
	if !withMembership {
		return
	}
	if err := direct.Exec(ctx, `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-1", at); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	members, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)`)
	if err != nil {
		t.Fatalf("prepare membership: %v", err)
	}
	for i := 0; i < units; i++ {
		for n := 0; n < budgetFixtureNodesPerUnit; n++ {
			if err := members.Append(orgID, "issue", fmt.Sprintf("issue-%06d-%d", i, n), fmt.Sprintf("wu-%06d", i), "theme", "feature_delivery", at, "run-1"); err != nil {
				t.Fatalf("append membership: %v", err)
			}
		}
	}
	if err := members.Send(); err != nil {
		t.Fatalf("send membership: %v", err)
	}
}

func budgetFixtureSubjects() []contextfabric.SubjectRef {
	subjects := make([]contextfabric.SubjectRef, 0, budgetFixtureRepos)
	for i := 0; i < budgetFixtureRepos; i++ {
		label := fmt.Sprintf("cw-%d", i)
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label})
	}
	return subjects
}

type budgetReadTotals struct{ statements, readBytes, readRows uint64 }

// budgetQueryLogTotals sums the SELECTs the server logged against table.
func budgetQueryLogTotals(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, table string) budgetReadTotals {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var r budgetReadTotals
	if err := direct.QueryRow(ctx, `SELECT count(), sum(read_bytes), sum(read_rows) FROM system.query_log
WHERE type = 'QueryFinish' AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.' || ?))`, table).Scan(&r.statements, &r.readBytes, &r.readRows); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	return r
}

func budgetDelta(after, before budgetReadTotals) budgetReadTotals {
	return budgetReadTotals{after.statements - before.statements, after.readBytes - before.readBytes, after.readRows - before.readRows}
}

func readBudgetFixtureMix(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, provider contextfabric.FactProvider, orgID string) (investments, membership budgetReadTotals) {
	t.Helper()
	start, end := ts(2026, 8, 11, 0, 0, 0), ts(2026, 9, 10, 0, 0, 0)
	beforeInv := budgetQueryLogTotals(t, ctx, direct, "work_unit_investments")
	beforeMem := budgetQueryLogTotals(t, ctx, direct, "work_unit_membership")
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment, Subjects: budgetFixtureSubjects(),
	})
	if err != nil {
		t.Fatalf("ReadFacts(%s): %v\nserver: %s", orgID, err, lastServerException(ctx, direct))
	}
	if len(result.Facts) == 0 {
		t.Fatalf("no investment facts served for %s: %+v\nserver: %s", orgID, result, lastServerException(ctx, direct))
	}
	investments = budgetDelta(budgetQueryLogTotals(t, ctx, direct, "work_unit_investments"), beforeInv)
	membership = budgetDelta(budgetQueryLogTotals(t, ctx, direct, "work_unit_membership"), beforeMem)
	if investments.statements == 0 || investments.readBytes == 0 {
		t.Fatalf("measurement did not happen for %s: %+v", orgID, investments)
	}
	return investments, membership
}

// The bytes a statement reads are a property of the table state, not of the
// server's cache state: cold and warm reads are equal, and the read grows
// with the organization's history.
func TestThemeMixReadBytesColdVersusWarmAndByOrgSizeAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)

	const small, large = "org-cw-small", "org-cw-large"
	seedBudgetFixtureOrg(t, ctx, direct, small, budgetFixtureUnits, false)
	seedBudgetFixtureOrg(t, ctx, direct, large, 2*budgetFixtureUnits, false)

	drop := func() {
		t.Helper()
		for _, s := range []string{`SYSTEM DROP MARK CACHE`, `SYSTEM DROP UNCOMPRESSED CACHE`} {
			if err := direct.Exec(ctx, s); err != nil {
				t.Fatalf("%s (cache drop did not happen): %v", s, err)
			}
		}
	}
	drop()
	cold, _ := readBudgetFixtureMix(t, ctx, direct, provider, small)
	warm, _ := readBudgetFixtureMix(t, ctx, direct, provider, small)
	drop()
	coldAgain, _ := readBudgetFixtureMix(t, ctx, direct, provider, small)
	drop()
	bigCold, _ := readBudgetFixtureMix(t, ctx, direct, provider, large)
	bigWarm, _ := readBudgetFixtureMix(t, ctx, direct, provider, large)
	t.Logf("small cold=%d warm=%d cold2=%d large cold=%d warm=%d (large/small %.2fx)", cold.readBytes, warm.readBytes, coldAgain.readBytes, bigCold.readBytes, bigWarm.readBytes, float64(bigWarm.readBytes)/float64(warm.readBytes))

	if cold.readBytes != warm.readBytes || coldAgain.readBytes != warm.readBytes {
		t.Errorf("read_bytes depends on cache state: cold=%d warm=%d cold2=%d", cold.readBytes, warm.readBytes, coldAgain.readBytes)
	}
	if bigCold.readBytes != bigWarm.readBytes {
		t.Errorf("large org read_bytes depends on cache state: cold=%d warm=%d", bigCold.readBytes, bigWarm.readBytes)
	}
	if ratio := float64(bigWarm.readBytes) / float64(warm.readBytes); ratio < 1.8 || ratio > 2.2 {
		t.Errorf("a twice-as-large organization read %.2fx the bytes, want about 2x", ratio)
	}
}

// With a complete membership run recorded, the scope must be read from
// work_unit_membership once (<= 1.3x one pass over the columns it needs) and
// then remembered: a second read of the same run touches work_unit_membership
// not at all and costs what the investments table alone costs.
func TestThemeMixMembershipScopeIsReadOnceAndRememberedAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)

	const base, scoped = "org-ms-base", "org-ms-scoped"
	seedBudgetFixtureOrg(t, ctx, direct, base, budgetFixtureUnits, false)
	seedBudgetFixtureOrg(t, ctx, direct, scoped, budgetFixtureUnits, true)

	baseline, _ := readBudgetFixtureMix(t, ctx, direct, provider, base)

	// One pass over the membership columns the scope reads.
	beforePass := budgetQueryLogTotals(t, ctx, direct, "work_unit_membership")
	var distinct uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM (SELECT DISTINCT work_unit_id FROM work_unit_membership WHERE org_id = ? AND run_id = 'run-1')`, scoped).Scan(&distinct); err != nil {
		t.Fatalf("membership pass: %v", err)
	}
	onePass := budgetDelta(budgetQueryLogTotals(t, ctx, direct, "work_unit_membership"), beforePass).readBytes
	if onePass == 0 || distinct != budgetFixtureUnits {
		t.Fatalf("measurement did not happen: membership pass read %d bytes for %d units", onePass, distinct)
	}

	first, firstMembership := readBudgetFixtureMix(t, ctx, direct, provider, scoped)
	second, secondMembership := readBudgetFixtureMix(t, ctx, direct, provider, scoped)
	t.Logf("investments-only=%d membership-one-pass=%d first-total=%d first-membership=%d second-total=%d second-membership-statements=%d",
		baseline.readBytes, onePass, first.readBytes, firstMembership.readBytes, second.readBytes, secondMembership.statements)

	if firstMembership.statements == 0 {
		t.Fatal("the first read did not load the membership scope: measurement did not happen")
	}
	if float64(firstMembership.readBytes) > 1.3*float64(onePass) {
		t.Errorf("first read scanned membership %d bytes, want <= 1.3 x one pass (%d)", firstMembership.readBytes, onePass)
	}
	if secondMembership.statements != 0 || secondMembership.readBytes != 0 {
		t.Errorf("second read touched work_unit_membership (%d statements, %d bytes), want none: the scope is remembered", secondMembership.statements, secondMembership.readBytes)
	}
	if float64(second.readBytes) > 1.3*float64(baseline.readBytes) {
		t.Errorf("second read cost %d bytes, want <= 1.3 x the investments-only read (%d)", second.readBytes, baseline.readBytes)
	}
}
