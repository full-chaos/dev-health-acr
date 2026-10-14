package devhealthfacts

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/columnprobe"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// changeFailureStatusColumns is the list of columns the new statement form
// names; a column added by the same migration is one more entry.
var changeFailureStatusColumns = []string{"failed_deployments_status", "deployments_status_known"}

// changeFailureStatusColumnsProbe caches whether repo_change_failure_daily has
// the status columns, through the shared columnprobe policy: a positive answer
// is final for the process, a negative one is re-asked after
// columnprobe.Recheck, a failed probe never pins either shape.
type changeFailureStatusColumnsProbe struct{ columnprobe.Probe }

func (p *changeFailureStatusColumnsProbe) availableAt(ctx context.Context, client contextpacket.ClickHouseQueryClient, now time.Time) bool {
	if p == nil || client == nil {
		return false
	}
	p.Configure("change_failure_status_columns", "repo_change_failure_daily", changeFailureStatusColumns)
	return p.Available(ctx, func(ctx context.Context, statement string, names []string) (uint64, error) {
		return countColumns(ctx, client, statement, names)
	}, now)
}

func countColumns(ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, names []string) (uint64, error) {
	rows, err := client.Query(ctx, statement, []contextpacket.ClickHouseBinding{{Name: "names", Value: names}})
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, fmt.Errorf("devhealthfacts: column probe returned no row: %w", rows.Err())
	}
	var count uint64
	if err := rows.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
