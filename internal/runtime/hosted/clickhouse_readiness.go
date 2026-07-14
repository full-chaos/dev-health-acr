package hosted

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func checkClickHouseRuntime(ctx context.Context, ping func(context.Context) error, client contextpacket.ClickHouseQueryClient) (resultErr error) {
	if err := ping(ctx); err != nil {
		return errors.New("ClickHouse runtime is unavailable")
	}
	rows, err := client.Query(ctx, `SELECT toString(id), repo, ifNull(ref, '') FROM repos FINAL WHERE org_id = {org_id:String} AND repo = {repo_slug:String} LIMIT 1`, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: "acr-readiness-probe"}, {Name: "repo_slug", Value: "acr/readiness-probe"},
	})
	if err != nil {
		return errors.New("ClickHouse runtime catalog is unavailable")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			resultErr = errors.Join(resultErr, errors.New("ClickHouse runtime catalog is unavailable"))
		}
	}()
	for rows.Next() {
		var repositoryID, repository, branch string
		if err := rows.Scan(&repositoryID, &repository, &branch); err != nil {
			return errors.New("ClickHouse runtime catalog is unavailable")
		}
	}
	if err := rows.Err(); err != nil {
		return errors.New("ClickHouse runtime catalog is unavailable")
	}
	return nil
}
