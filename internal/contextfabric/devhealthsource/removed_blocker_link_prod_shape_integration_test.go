package devhealthsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The largest organization measured holds 2,103 blocks relations. When every
// one of them is a removed link (the worst case of the removed-link set), the
// call must read the set in one row, project nothing for the links and retract
// each edge. The element size is reported: it is the size of the array bound
// into every page statement.
func TestEveryBlockerLinkOfTheLargestOrganizationRemovedIsRetracted(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	const (
		org   = "95140000-0000-4000-8000-0000000000e1"
		repo  = "95140000-0000-4000-8000-0000000000e2"
		links = 2103
	)
	at := time.Now().UTC().Truncate(time.Second).Add(-6 * time.Hour)
	idPrefix := "jira:" + strings.Repeat("P", 60) + "-"
	mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo, "acme/prod-shape", nil, at, nil, at, org, "jira")
	mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat(?, toString(number)), ?, ?, 'issue', 'open', '', '', 'jira', '', ?, ? FROM numbers(?)`, idPrefix, repo, org, at, at, uint64(2*links))
	mustExec(t, ctx, direct, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced)
SELECT concat(?, toString(2 * number)), concat(?, toString(2 * number + 1)), 'blocks', 'blocks', ?, ? + toIntervalSecond(1) FROM numbers(?)`, idPrefix, idPrefix, org, at, uint64(links))

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	drain := func(cursor string) (string, int, int) {
		edges, tombstones := 0, 0
		for i := 0; i < 400; i++ {
			batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
			if err != nil {
				t.Fatalf("NextProjectionBatch: %v%s", err, rawCauses(err))
			}
			if !ok {
				return cursor, edges, tombstones
			}
			cursor = batch.NextCursor
			for _, r := range batch.Relationships {
				if r.Type == contractsv1.ContextFabricRelationshipBlocks {
					edges++
				}
			}
			for _, tomb := range batch.Tombstones {
				if tomb.Kind == "relationship" {
					tombstones++
				}
			}
		}
		t.Fatal("did not converge")
		return "", 0, 0
	}
	cursor, edges, _ := drain("")
	if edges != links {
		t.Fatalf("%d blocks edges projected before the links were removed, want %d", edges, links)
	}
	mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat(?, toString(number)), ?, ?, 'issue', 'open', '', '', 'jira', '', ?, ? + toIntervalHour(2) FROM numbers(?)`, idPrefix, repo, org, at, at, uint64(2*links))
	_, edgesAfter, tombstones := drain(cursor)
	if edgesAfter != 0 || tombstones < links {
		t.Fatalf("after every link was removed: %d blocks edges asserted and %d relationship tombstones, want 0 and at least %d", edgesAfter, tombstones, links)
	}
	keyBytes := links * (2*2*len(idPrefix+"0000") + 2*len("blocks") + 17 + 3)
	t.Logf("SET SIZE: %d removed links, about %d bytes of keys bound into each page statement", links, keyBytes)
}
