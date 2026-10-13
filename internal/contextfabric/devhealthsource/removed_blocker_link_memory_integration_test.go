package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The projector caps every statement at 192 MiB. The relation-current rule
// needs the read times of every blocker endpoint of the organization, so it is
// evaluated by ONE statement per projection call (the removed-link set); the
// page statements receive only that set. This row drains a seeded organization
// of blocker links through the production statements under the cap, at two
// sizes, and reports the two statements apart: the set statement's peak and
// the page statements' peak.
func TestRemovedBlockerLinkRuleStaysUnderTheProjectorStatementCap(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	const smallLinks, largeLinks = 5000, 20000
	smallSet, smallPage := measureRuleStatements(t, ctx, query, direct, "95140000-0000-4000-8000-0000000000f1", "95140000-0000-4000-8000-0000000000f2", smallLinks)
	largeSet, largePage := measureRuleStatements(t, ctx, query, direct, "95140000-0000-4000-8000-0000000000f3", "95140000-0000-4000-8000-0000000000f4", largeLinks)
	ratio := float64(largeLinks) / smallLinks
	t.Logf("SLOPE set statement: %d bytes at %d links, %d bytes at %d links, %.2fx for %.0fx the links", smallSet, smallLinks, largeSet, largeLinks, float64(largeSet)/float64(smallSet), ratio)
	t.Logf("SLOPE page statements: %d bytes at %d links, %d bytes at %d links, %.2fx for %.0fx the links", smallPage, smallLinks, largePage, largeLinks, float64(largePage)/float64(smallPage), ratio)
	if float64(largeSet) > 1.15*ratio*float64(smallSet) {
		t.Errorf("the set statement grew %.2fx for %.0fx the links, over the linear slope", float64(largeSet)/float64(smallSet), ratio)
	}
	if float64(largePage) > 1.15*ratio*float64(smallPage) {
		t.Errorf("the page statements grew %.2fx for %.0fx the links, over the linear slope", float64(largePage)/float64(smallPage), ratio)
	}
}

// measureRuleStatements returns the peak memory of the removed-link set
// statement and of the dependency page statements for one seeded size.
func measureRuleStatements(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, org, repo string, links int) (setPeak, pagePeak uint64) {
	t.Helper()
	const (
		cap       = uint64(192 << 20)
		maxPeak   = cap / 8 * 7
		wallLimit = 20 * time.Minute
	)
	at := time.Now().UTC().Truncate(time.Second).Add(-6 * time.Hour)
	mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo, "acme/memory-row", nil, at, nil, at, org, "jira")
	mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('jira:PROJECTKEY-', toString(number)), ?, ?, 'issue', 'open', '', '', 'jira', '', ?, ? FROM numbers(?)`, repo, org, at, at, uint64(2*links))
	mustExec(t, ctx, direct, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced)
SELECT concat('jira:PROJECTKEY-', toString(2 * number)), concat('jira:PROJECTKEY-', toString(2 * number + 1)), 'blocks', 'blocks', ?, ? + toIntervalSecond(1 + number % 3600) FROM numbers(?)`, org, at, uint64(links))
	// Every tenth blocker syncs again without its link: those links are gone.
	mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('jira:PROJECTKEY-', toString(2 * number)), ?, ?, 'issue', 'open', '', '', 'jira', '', ?, ? + toIntervalHour(4) FROM numbers(?) WHERE number % 10 = 0`, repo, org, at, at, uint64(links))

	capped := contextpacket.NewMemoryCappedQueryClient(query, cap)
	source, err := devhealthsource.NewClickHouseProjectionSource(capped)
	if err != nil {
		t.Fatal(err)
	}
	start := serverNow(t, ctx, direct)
	deadline := time.Now().Add(wallLimit)
	cursor, pages, tombstones := "", 0, 0
	for ; time.Now().Before(deadline); pages++ {
		batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v%s", pages, err, rawCauses(err))
		}
		if !ok {
			break
		}
		cursor = batch.NextCursor
		for _, tomb := range batch.Tombstones {
			if tomb.Kind == "relationship" {
				tombstones++
			}
		}
	}
	if time.Now().After(deadline) {
		t.Fatalf("did not catch up in %s (%d pages)", wallLimit, pages)
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	peakOf := func(marker string) (uint64, uint64) {
		var statements, peak uint64
		if err := direct.QueryRow(ctx, `SELECT count(), max(memory_usage) FROM system.query_log
WHERE type = 'QueryFinish' AND query_start_time_microseconds >= toDateTime64(?, 6, 'UTC') AND position(query, ?) > 0 AND query NOT LIKE 'INSERT%'`,
			start.UTC().Format("2006-01-02 15:04:05.000000"), marker).Scan(&statements, &peak); err != nil {
			t.Fatal(err)
		}
		return statements, peak
	}
	setCount, setPeak := peakOf("AS ended_links")
	pageCount, pagePeak := peakOf("toInt64(p[4])")
	t.Logf("PEAK at %d links over %d pages: %d set statements, peak %d bytes; %d page statements, peak %d bytes; cap %d bytes", links, pages, setCount, setPeak, pageCount, pagePeak, cap)
	if setCount == 0 || pageCount == 0 {
		t.Fatalf("the measurement did not happen: %d set statements and %d page statements were logged", setCount, pageCount)
	}
	if removed := links / 10; tombstones < removed {
		t.Fatalf("%d relationship tombstones for %d removed links: a removed link kept its edge", tombstones, removed)
	}
	for name, peak := range map[string]uint64{"set": setPeak, "page": pagePeak} {
		if peak >= maxPeak {
			t.Fatalf("a %s statement peaked at %d bytes, want under %d (7/8 of the projector cap)", name, peak, maxPeak)
		}
	}
	return setPeak, pagePeak
}

// rawCauses lists the unsanitized causes under err, for a failing read.
func rawCauses(err error) string {
	var out string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		out += "\n  cause: " + e.Error()
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	return out
}
