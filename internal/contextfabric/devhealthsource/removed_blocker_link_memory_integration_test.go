package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The projector caps every statement at 192 MiB. The relation-current rule
// builds, per statement, the read times of every blocker endpoint of the
// organization, so a statement that carries it follows the number of blocks
// links, not the page size. This row drains a seeded organization of blocker
// links through the production statement under that cap and holds the peak of
// the statements that carry the rule to 7/8 of it.
func TestRemovedBlockerLinkRuleStaysUnderTheProjectorStatementCap(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	const (
		org       = "95140000-0000-4000-8000-0000000000f1"
		repo      = "95140000-0000-4000-8000-0000000000f2"
		links     = 20000
		cap       = uint64(192 << 20)
		maxPeak   = cap / 8 * 7
		wallLimit = 20 * time.Minute
	)
	at := time.Now().UTC().Truncate(time.Second).Add(-6 * time.Hour)
	mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo, "acme/memory-row", nil, at, nil, at, org, "jira")
	mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('jira:PROJECTKEY-', toString(number)), ?, ?, 'issue', 'open', '', '', 'jira', '', ?, ? + toIntervalSecond(number % 3600) FROM numbers(?)`, repo, org, at, at, uint64(2*links))
	mustExec(t, ctx, direct, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced)
SELECT concat('jira:PROJECTKEY-', toString(2 * number)), concat('jira:PROJECTKEY-', toString(2 * number + 1)), 'blocks', 'blocks', ?, ? + toIntervalSecond(number % 3600) FROM numbers(?)`, org, at, uint64(links))

	capped := contextpacket.NewMemoryCappedQueryClient(query, cap)
	source, err := devhealthsource.NewClickHouseProjectionSource(capped)
	if err != nil {
		t.Fatal(err)
	}
	start := serverNow(t, ctx, direct)
	deadline := time.Now().Add(wallLimit)
	cursor, pages := "", 0
	for ; time.Now().Before(deadline); pages++ {
		batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if !ok {
			break
		}
		cursor = batch.NextCursor
	}
	if time.Now().After(deadline) {
		t.Fatalf("did not catch up in %s (%d pages)", wallLimit, pages)
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var statements, peak uint64
	if err := direct.QueryRow(ctx, `SELECT count(), max(memory_usage) FROM system.query_log
WHERE type = 'QueryFinish' AND query_start_time_microseconds >= toDateTime64(?, 6, 'UTC') AND position(query, 'work_item_relations_read') > 0 AND query NOT LIKE 'INSERT%'`,
		start.UTC().Format("2006-01-02 15:04:05.000000")).Scan(&statements, &peak); err != nil {
		t.Fatal(err)
	}
	t.Logf("PEAK: %d statements carrying the rule over %d pages and %d links; peak %d bytes against the %d byte cap", statements, pages, links, peak, cap)
	if statements == 0 {
		t.Fatal("no statement carrying the rule was logged: the measurement did not happen")
	}
	if peak >= maxPeak {
		t.Fatalf("a statement carrying the rule peaked at %d bytes, want under %d (7/8 of the projector cap)", peak, maxPeak)
	}
}
