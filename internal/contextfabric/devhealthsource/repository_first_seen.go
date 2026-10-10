package devhealthsource

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// repositoryCreatedStart is the start a repos row states on its own. The ops
// ingest rewrites created_at to the sync stamp on every sync, so a created_at
// that is not earlier than last_synced says nothing about when the repository
// began; it yields no start at all.
func repositoryCreatedStart(createdAt, lastSynced time.Time) *time.Time {
	if createdAt.IsZero() || createdAt.Unix() <= 0 || !createdAt.Before(lastSynced) {
		return nil
	}
	return requiredTime(createdAt)
}

const repositoryFirstSeenStatement = `SELECT repo_id_text, min(created_at_utc) FROM (
SELECT toString(repo_id) AS repo_id_text, toDateTime64(created_at, 3, 'UTC') AS created_at_utc
FROM git_pull_requests
PREWHERE org_id = {org_id:String} AND repo_id IN (SELECT arrayJoin(arrayMap(x -> toUUID(x), {repo_ids:Array(String)})))
WHERE created_at > toDateTime64(0, 3, 'UTC')
UNION ALL
SELECT toString(repo_id) AS repo_id_text, toDateTime64(created_at, 3, 'UTC') AS created_at_utc
FROM work_items
PREWHERE org_id = {org_id:String} AND repo_id IN (SELECT arrayJoin(arrayMap(x -> toUUID(x), {repo_ids:Array(String)})))
WHERE created_at > toDateTime64(0, 3, 'UTC')
) GROUP BY repo_id_text`

// applyRepositoryFirstSeen settles each repository entity's ValidFrom to the
// earliest start the source can show: its own created_at while that is
// earlier than the sync, and the earliest pull request or work item created
// for it. One grouped statement serves the whole page. When that read fails
// the page keeps the start the row states on its own, and the failure is
// logged.
func applyRepositoryFirstSeen(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, items []candidate) {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.entity != nil {
			ids = append(ids, item.sortKey)
		}
	}
	if len(ids) == 0 {
		return
	}
	evidence, err := readRepositoryFirstSeen(ctx, client, orgID, ids)
	if err != nil {
		logTableReadFailure(ctx, slog.Default(), "clickhouse", orgID, "repository_first_seen", err)
		return
	}
	for _, item := range items {
		if item.entity == nil {
			continue
		}
		item.entity.ValidFrom = earlierStart(item.entity.ValidFrom, evidence[strings.ToLower(item.sortKey)])
	}
}

func readRepositoryFirstSeen(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, ids []string) (map[string]*time.Time, error) {
	rows, err := client.Query(ctx, repositoryFirstSeenStatement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "repo_ids", Value: ids}})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[string]*time.Time, len(ids))
	for rows.Next() {
		var id string
		var first time.Time
		if err := rows.Scan(&id, &first); err != nil {
			return nil, err
		}
		found[strings.ToLower(id)] = requiredTime(first)
	}
	return found, rows.Err()
}

func earlierStart(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.Before(*a):
		return b
	}
	return a
}
