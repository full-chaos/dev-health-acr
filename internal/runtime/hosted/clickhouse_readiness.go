package hosted

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

const clickHouseReadinessSchemaProbe = `SELECT r.ref_sha256, p.head_branch_sha256, p.base_branch_sha256, c.branch_sha256, f.ref_sha256 FROM repos AS r CROSS JOIN git_pull_requests AS p CROSS JOIN ci_pipeline_runs AS c CROSS JOIN file_complexity_snapshots AS f LIMIT 0`

func checkClickHouseRuntime(ctx context.Context, ping func(context.Context) error, client contextpacket.ClickHouseQueryClient) (resultErr error) {
	if err := ping(ctx); err != nil {
		return errors.New("ClickHouse runtime is unavailable")
	}
	rows, err := client.Query(ctx, clickHouseReadinessSchemaProbe, nil)
	if err != nil {
		return errors.New("ClickHouse runtime catalog is unavailable")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			resultErr = errors.Join(resultErr, errors.New("ClickHouse runtime catalog is unavailable"))
		}
	}()
	if err := rows.Err(); err != nil {
		return errors.New("ClickHouse runtime catalog is unavailable")
	}
	return nil
}
