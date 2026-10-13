package devhealthsource_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

func TestIssueLinkInShapeReadVolumeProbe(t *testing.T) {
	ctx := context.Background()
	_, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	now := time.Now().UTC()
	for _, id := range []string{"CHAOS-77", "CHAOS-78", "PROJ-5"} {
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, wgOther, wgOrg, "t", "open", "", "", "linear", "", now, now); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, wgRepo, id, uint32(311), float32(0.9), "native", "", now, wgOrg); err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}
	filler := func(from, count int) {
		for _, repo := range []string{wgOther, wgRepo} {
			if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) SELECT concat('CHAOS-', toString(%d + number)), toUUID('%s'), '%s', 'filler', 'open', '', '', 'linear', '', now64(3), now64(3) FROM numbers(%d)`, from, repo, wgOrg, count)); err != nil {
				t.Fatalf("filler: %v", err)
			}
		}
	}
	measure := func() (uint64, uint64) {
		stmt := `SELECT /* PROBE1161 */ w.work_item_id, w.title FROM work_items AS w FINAL WHERE w.org_id = ? AND w.work_item_id IN (SELECT work_item_id FROM work_graph_issue_pr WHERE org_id = ?)`
		run := func() {
			rows, err := direct.Query(ctx, stmt, wgOrg, wgOrg)
			if err != nil {
				t.Fatalf("probe statement: %v", err)
			}
			for rows.Next() {
			}
			rows.Close()
		}
		run()
		mark := time.Now().UnixMicro()
		run()
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush: %v", err)
		}
		var mem, read uint64
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_bytes) FROM system.query_log WHERE event_time_microseconds > fromUnixTimestamp64Micro(?) AND type = 'QueryFinish' AND position(query, 'PROBE1161') > 0 AND query_kind = 'Select'`, mark).Scan(&mem, &read); err != nil || read == 0 {
			t.Fatalf("measurement did not happen: %v read=%d", err, read)
		}
		return mem, read
	}
	filler(1000000, 125000)
	smallMem, smallRead := measure()
	filler(2000000, 187500)
	largeMem, largeRead := measure()
	t.Fatalf("PROBE1161-SHAPE read_bytes %d -> %d (x%.2f); memory %d -> %d (x%.2f) from 250k to 625k filler", smallRead, largeRead, float64(largeRead)/float64(smallRead), smallMem, largeMem, float64(largeMem)/float64(smallMem))
}
