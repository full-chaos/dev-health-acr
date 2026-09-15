package devhealthfacts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-go/readers"
)

// TestChaos5751WorkItemReaderPhysicalAndMemoryBudgetLimitsAgainstRealClickHouse
// executes the released reader's production work-item statements with each
// independent server-side budget lowered to a tiny, controlled value. The
// existing CHAOS-5751 integration test checks that the shipped settings text
// contains the fixed 10,000-row and 512 MiB ceilings and behaviorally checks
// max_result_rows. This test supplies deliberately lower values only to make
// the physical-row and memory failure paths observable on a two-row fixture;
// it does not claim that the shipped ceilings themselves can be exceeded by
// this small corpus.
func TestChaos5751WorkItemReaderPhysicalAndMemoryBudgetLimitsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	repoID := "ea198fbc-1945-3717-05d8-eb78866b4e90"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoID, orgID, "limits/physical-memory", "github", at); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, completed_at, last_synced) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"LIMIT-A", repoID, orgID, "github", "limit A", "open", at, at, at, at,
		"LIMIT-B", repoID, orgID, "github", "limit B", "open", at, at, at, at); err != nil {
		t.Fatalf("seed two work items: %v", err)
	}

	ids := []string{repoID + ":LIMIT-A", repoID + ":LIMIT-B"}
	scope := readers.AuthorizationScope{
		RepositorySelectors: &readers.RepositorySelectorScope{
			Granted: readers.RepositorySelectorSet{All: true},
		},
	}
	reads := []struct {
		name string
		call func(readers.Settings) (int, error)
	}{
		{
			name: "status",
			call: func(settings readers.Settings) (int, error) {
				rows, err := readers.ReadWorkItemStatusWithScopeAndRowLimit(ctx, query, orgID, ids, scope, settings, len(ids)+1)
				return len(rows), err
			},
		},
		{
			name: "title",
			call: func(settings readers.Settings) (int, error) {
				rows, err := readers.ReadWorkItemTitleWithScopeAndRowLimit(ctx, query, orgID, ids, scope, settings, len(ids)+1)
				return len(rows), err
			},
		},
		{
			name: "completion",
			call: func(settings readers.Settings) (int, error) {
				rows, err := readers.ReadWorkItemCompletionWithScopeAndRowLimit(ctx, query, orgID, ids, readers.TimeBound{}, scope, settings, len(ids)+1)
				return len(rows), err
			},
		},
	}

	for _, read := range reads {
		read := read
		t.Run("two-row control completes under independent ceilings/"+read.name, func(t *testing.T) {
			rows, err := read.call(readers.Settings{
				MaxRowsToRead:  uint64(len(ids) + 1),
				MaxMemoryUsage: 512 << 20,
				MaxResultRows:  uint64(len(ids) + 1),
			})
			if err != nil {
				t.Fatalf("bounded two-row control error = %v", err)
			}
			if rows != len(ids) {
				t.Fatalf("bounded two-row control rows = %d, want %d", rows, len(ids))
			}
		})

		t.Run("physical-row ceiling throws before result ceiling/"+read.name, func(t *testing.T) {
			rows, err := read.call(readers.Settings{
				MaxRowsToRead:  1,
				MaxMemoryUsage: 512 << 20,
				MaxResultRows:  100,
			})
			if err == nil {
				t.Fatalf("physical-row ceiling returned %d rows without an error", rows)
			}
			if rows != 0 {
				t.Fatalf("physical-row ceiling returned %d rows with an error, want 0", rows)
			}
			var serverErr *clickhousedriver.Exception
			if !errors.As(err, &serverErr) {
				t.Fatalf("physical-row ceiling error = %v, want a native ClickHouse exception", err)
			}
			if serverErr.Code != 158 {
				t.Fatalf("physical-row ceiling native error code = %d, want 158 (TOO_MANY_ROWS)", serverErr.Code)
			}
			t.Logf("physical-row budget failure (%s): native_error_code=%d error=%v", read.name, serverErr.Code, err)
		})

		t.Run("memory ceiling throws before result ceiling/"+read.name, func(t *testing.T) {
			rows, err := read.call(readers.Settings{
				MaxRowsToRead: 100,
				// One byte is a principled lower boundary. It proves that this
				// production-shaped reader carries the memory setting to
				// ClickHouse and propagates native enforcement without large
				// fixture data. It does not claim exhaustion of the shipped
				// 512 MiB ceiling.
				MaxMemoryUsage: 1,
				MaxResultRows:  100,
			})
			if err == nil {
				t.Fatalf("memory ceiling returned %d rows without an error", rows)
			}
			if rows != 0 {
				t.Fatalf("memory ceiling returned %d rows with an error, want 0", rows)
			}
			var serverErr *clickhousedriver.Exception
			if !errors.As(err, &serverErr) {
				t.Fatalf("memory ceiling error = %v, want a native ClickHouse exception", err)
			}
			if serverErr.Code != 241 {
				t.Fatalf("memory ceiling native error code = %d, want 241 (MEMORY_LIMIT_EXCEEDED)", serverErr.Code)
			}
			t.Logf("memory budget failure (%s): native_error_code=%d error=%v", read.name, serverErr.Code, err)
		})
	}
}
