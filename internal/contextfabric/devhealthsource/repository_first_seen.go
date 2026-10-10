package devhealthsource

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// repositoryCreatedStart is the start a repos row states on its own. The ops
// ingest rewrites created_at to the sync stamp on every sync, so a created_at
// that is not earlier than last_synced says nothing about when the repository
// began; it yields no start at all.
func repositoryCreatedStart(createdAt, lastSynced time.Time) *time.Time {
	if !createdAt.After(time.Unix(0, 0)) || !createdAt.Before(lastSynced) {
		return nil
	}
	return requiredTime(createdAt)
}

// Each of the two tables is a ReplacingMergeTree on last_synced keyed by
// (org_id, repo_id, number) and (org_id, repo_id, work_item_id): before parts
// merge, an obsolete version of a row sits beside the current one, so the
// current created_at of each row is taken first (argMax on last_synced) and
// only then the minimum over the repository. FINAL is not used: it would
// read every column of every part.
const repositoryFirstSeenStatement = `SELECT repo_id_text, min(current_created_at), argMin(source_name, current_created_at) FROM (
SELECT toString(repo_id) AS repo_id_text, 'pull_request' AS source_name, toDateTime64(argMax(created_at, last_synced), 3, 'UTC') AS current_created_at
FROM git_pull_requests
PREWHERE org_id = {org_id:String} AND repo_id IN (SELECT arrayJoin(arrayMap(x -> toUUID(x), {repo_ids:Array(String)})))
GROUP BY repo_id, number
UNION ALL
SELECT toString(repo_id) AS repo_id_text, 'work_item' AS source_name, toDateTime64(argMax(created_at, last_synced), 3, 'UTC') AS current_created_at
FROM work_items
PREWHERE org_id = {org_id:String} AND repo_id IN (SELECT arrayJoin(arrayMap(x -> toUUID(x), {repo_ids:Array(String)})))
GROUP BY repo_id, work_item_id
) WHERE current_created_at > toDateTime64(0, 3, 'UTC') GROUP BY repo_id_text`

const (
	startBasisCreatedAt   = "created_at"
	startBasisPullRequest = "pull_request"
	startBasisWorkItem    = "work_item"
	startBasisNone        = "none"
)

type firstSeenEvidence struct {
	at     *time.Time
	source string
}

type startLoggerKey struct{}

func withStartLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, startLoggerKey{}, logger)
}

func startLogger(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(startLoggerKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

// applyRepositoryFirstSeen settles each repository entity's ValidFrom to the
// earliest start the source can show: its own created_at while that is
// earlier than the sync, and the earliest pull request or work item created
// for it. One grouped statement serves the whole page. When that read fails
// the page keeps the start the row states on its own, and the failure is
// logged. One line per page counts the repositories by what decided their start: INFO
// on a page of a from-zero build where evidence decided a start, DEBUG on every
// other page (a re-stamped repository is read again on every sync, so a steady
// tick would log one INFO line per page forever).
func applyRepositoryFirstSeen(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, items []candidate) {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.sortKey)
	}
	if len(ids) == 0 {
		return
	}
	logger := startLogger(ctx)
	evidence, err := readRepositoryFirstSeen(ctx, client, orgID, ids)
	evidenceRead := err == nil
	if err != nil {
		logTableReadFailure(ctx, logger, "clickhouse", orgID, "repository_first_seen", err)
	}
	basis := map[string]int{}
	for _, item := range items {
		found := evidence[strings.ToLower(item.sortKey)]
		decided := startBasisNone
		if item.entity.ValidFrom != nil {
			decided = startBasisCreatedAt
		}
		if found.at != nil && (item.entity.ValidFrom == nil || found.at.Before(*item.entity.ValidFrom)) {
			item.entity.ValidFrom = found.at
			decided = found.source
		}
		basis[decided]++
	}
	level := slog.LevelDebug
	if cursor.Since.IsZero() && basis[startBasisPullRequest]+basis[startBasisWorkItem] > 0 {
		level = slog.LevelInfo
	}
	logger.Log(ctx, level, "devhealthsource repository start decided",
		"source", contextfabric.SanitizeLogAttr("clickhouse"), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"from_zero", cursor.Since.IsZero(), "repositories", len(items), "evidence_read", evidenceRead,
		"basis_created_at", basis[startBasisCreatedAt], "basis_pull_request", basis[startBasisPullRequest],
		"basis_work_item", basis[startBasisWorkItem], "basis_none", basis[startBasisNone])
}

func readRepositoryFirstSeen(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, ids []string) (map[string]firstSeenEvidence, error) {
	rows, err := client.Query(ctx, repositoryFirstSeenStatement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "repo_ids", Value: ids}})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[string]firstSeenEvidence, len(ids))
	for rows.Next() {
		var id, source string
		var first time.Time
		if err := rows.Scan(&id, &first, &source); err != nil {
			return nil, err
		}
		found[strings.ToLower(id)] = firstSeenEvidence{at: requiredTime(first), source: source}
	}
	return found, rows.Err()
}
