package devhealthfacts

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// changeFailureStatusColumnsRecheck is how long a negative probe stands before
// ClickHouse is asked again.
const changeFailureStatusColumnsRecheck = time.Minute

// changeFailureStatusColumnsStatement counts the two columns the status-based
// counts live in. A table without both is the shape before the ops migration
// that adds them; a statement that names a missing column fails, so the reader
// names them only after this answers 2.
const changeFailureStatusColumnsStatement = `SELECT count() FROM system.columns
WHERE database = currentDatabase() AND table = 'repo_change_failure_daily'
	AND has({names:Array(String)}, name)`

// changeFailureStatusColumns is the list of columns the new statement form
// names; a column added by the same migration is one more entry.
var changeFailureStatusColumns = []string{"failed_deployments_status", "deployments_status_known"}

// changeFailureStatusColumnsProbe caches whether repo_change_failure_daily has
// the status columns. A positive answer is final for the process; a negative
// one is re-asked after changeFailureStatusColumnsRecheck, so a roll of the ops
// migration is picked up without a restart. A failed probe keeps the last
// known answer; with none the answer is "no" for this call only and is not
// cached, so a flaky probe cannot pin either shape.
type changeFailureStatusColumnsProbe struct {
	mu        sync.Mutex
	known     bool
	available bool
	checkedAt time.Time
}

func (p *changeFailureStatusColumnsProbe) availableAt(ctx context.Context, client contextpacket.ClickHouseQueryClient, now time.Time) bool {
	if p == nil || client == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.known && (p.available || now.Sub(p.checkedAt) < changeFailureStatusColumnsRecheck) {
		return p.available
	}
	count, err := countStatusColumns(ctx, client)
	if err != nil {
		return p.known && p.available
	}
	p.known, p.available, p.checkedAt = true, count == uint64(len(changeFailureStatusColumns)), now
	return p.available
}

func countStatusColumns(ctx context.Context, client contextpacket.ClickHouseQueryClient) (uint64, error) {
	rows, err := client.Query(ctx, changeFailureStatusColumnsStatement, []contextpacket.ClickHouseBinding{{Name: "names", Value: changeFailureStatusColumns}})
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, fmt.Errorf("devhealthfacts: change failure column probe returned no row: %w", rows.Err())
	}
	var count uint64
	if err := rows.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
