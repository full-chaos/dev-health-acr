package contextpacket

import (
	"context"
	"fmt"
	"time"
)

// IngestBasis says what stored time, if any, proves a catalog source was
// synced. last_ingested_at is a stored sync time; an event time (a commit, a
// deploy, a work item update) never stands for it, because a quiet repository
// that synced an hour ago is fresh.
type IngestBasis int

const (
	// IngestBasisNone: the source's tables store no sync time (a computed_at is
	// compute time). The watermark carries no last_ingested_at and the status
	// is "unknown", never a stale verdict.
	IngestBasisNone IngestBasis = iota
	// IngestBasisRunStamp: a time rewritten on every repository sync run
	// (repos.last_synced). Recent is fresh, old is stale.
	IngestBasisRunStamp
	// IngestBasisWriteTime: the newest last_synced (work_items also
	// ingested_at) of the source's own rows. It is a row WRITE time: a sync that
	// finds nothing new rewrites nothing, and no other table says which sync
	// wrote the row, so a recent time proves freshness and an old one proves
	// nothing. Recent is fresh, old is unknown, never stale.
	IngestBasisWriteTime
)

var sourceIngestBasis = map[string]IngestBasis{
	"repository_freshness.v1":           IngestBasisRunStamp,
	"git_commits.v1":                    IngestBasisWriteTime,
	"git_commit_files.v1":               IngestBasisWriteTime,
	"pull_requests.v1":                  IngestBasisWriteTime,
	"pull_request_reviews.v1":           IngestBasisWriteTime,
	"ci_pipeline_runs.v1":               IngestBasisWriteTime,
	"deployments.v1":                    IngestBasisWriteTime,
	"work_items.v1":                     IngestBasisWriteTime,
	"work_item_dependencies.v1":         IngestBasisWriteTime,
	"work_graph.v1":                     IngestBasisWriteTime,
	"incidents.v1":                      IngestBasisWriteTime,
	"ai_workflow_runs.v1":               IngestBasisNone,
	"ai_workflow_artifacts.v1":          IngestBasisNone,
	"ai_review_outcomes.v1":             IngestBasisNone,
	"deployment_incident_provenance.v1": IngestBasisNone,
	"file_hotspots.v1":                  IngestBasisNone,
	"file_complexity.v1":                IngestBasisNone,
}

// IngestBasisOf is the basis of a catalog source; a source the table does not
// name has none.
func IngestBasisOf(sourceID string) IngestBasis {
	return sourceIngestBasis[sourceID]
}

const ingestAsOf = `{as_of:Nullable(DateTime64(3, 'UTC'))}`

func ingestTail(column string) string {
	return ` AND (` + ingestAsOf + ` IS NULL OR ` + column + ` <= ` + ingestAsOf + `)`
}

const joinedRepo = ` INNER JOIN repos AS repo FINAL ON repo.id = %[1]s.repo_id AND repo.org_id = %[1]s.org_id WHERE %[1]s.org_id = {org_id:String} AND repo.org_id = {org_id:String} AND repo.id = {repo_id:UUID}`

func joinedIngest(table, alias string) string {
	return fmt.Sprintf("SELECT max(%[1]s.last_synced), count() FROM %[2]s AS %[1]s FINAL"+joinedRepo+ingestTail(alias+".last_synced"), alias, table)
}

// sourceIngestStatements read the newest stored sync time of a source's rows
// in the repository. They ignore branch, task, commit and file narrowing: a
// sync is repository-wide, and freshness describes the source, not the
// subset of rows a request kept.
var sourceIngestStatements = map[string]string{
	"repository_freshness.v1":   `SELECT max(last_synced), count() FROM repos FINAL WHERE org_id = {org_id:String} AND id = {repo_id:UUID} AND repo = {repo_slug:String}` + ingestTail("last_synced"),
	"git_commits.v1":            joinedIngest("git_commits", "c"),
	"git_commit_files.v1":       joinedIngest("git_commit_stats", "c"),
	"pull_requests.v1":          joinedIngest("git_pull_requests", "p"),
	"pull_request_reviews.v1":   joinedIngest("git_pull_request_reviews", "r"),
	"ci_pipeline_runs.v1":       joinedIngest("ci_pipeline_runs", "c"),
	"deployments.v1":            joinedIngest("deployments", "d"),
	"work_items.v1":             `SELECT max(greatest(last_synced, ingested_at)), count() FROM work_items FINAL WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID}` + ingestTail("greatest(last_synced, ingested_at)"),
	"work_item_dependencies.v1": `SELECT max(d.last_synced), count() FROM work_item_dependencies AS d FINAL INNER JOIN work_items AS w FINAL ON d.source_work_item_id = w.work_item_id WHERE d.org_id = {org_id:String} AND w.org_id = {org_id:String} AND w.repo_id = {repo_id:UUID}` + ingestTail("d.last_synced"),
	"work_graph.v1":             `SELECT max(last_synced), count() FROM work_graph_edges FINAL WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID}` + ingestTail("last_synced"),
	"incidents.v1":              `SELECT max(i.last_synced), count() FROM operational_incidents AS i FINAL INNER JOIN operational_service_repository_mappings AS m FINAL ON i.org_id = m.org_id AND i.service_id = m.service_id WHERE i.org_id = {org_id:String} AND m.org_id = {org_id:String} AND m.repo_id = {repo_id:UUID} AND m.is_active = 1 AND i.is_deleted = 0 AND m.valid_from <= coalesce(` + ingestAsOf + `, now64(6)) AND (m.valid_to IS NULL OR m.valid_to > coalesce(` + ingestAsOf + `, now64(6)))` + ingestTail("i.last_synced"),
}

// SourceIngestExecutor is optionally implemented by an executor that can read
// a source's stored sync time.
type SourceIngestExecutor interface {
	QueryLatestIngested(context.Context, SourceQuery, []ClickHouseBinding) (time.Time, bool, error)
}

// QueryLatestIngested returns the newest stored sync time of the source's rows
// in the plan's repository, and false when the source holds no row.
func (e *ClickHouseSourceExecutor) QueryLatestIngested(ctx context.Context, query SourceQuery, bindings []ClickHouseBinding) (_ time.Time, _ bool, err error) {
	if e == nil || e.client == nil {
		return time.Time{}, false, fmt.Errorf("contextpacket: clickhouse query client is required")
	}
	statement, known := sourceIngestStatements[query.ID]
	if !known {
		return time.Time{}, false, fmt.Errorf("contextpacket: source %s stores no sync time", query.ID)
	}
	rows, err := e.client.Query(ctx, statement, bindings)
	if err != nil {
		return time.Time{}, false, &sourceExecutionError{phase: SourceQueryPhaseQuery, sourceID: query.ID, cause: err}
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = &sourceExecutionError{phase: SourceQueryPhaseClose, sourceID: query.ID, cause: closeErr}
		}
	}()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return time.Time{}, false, &sourceExecutionError{phase: SourceQueryPhaseIteration, sourceID: query.ID, cause: err}
		}
		return time.Time{}, false, nil
	}
	var latest time.Time
	var count uint64
	if scanErr := rows.Scan(&latest, &count); scanErr != nil {
		return time.Time{}, false, &sourceExecutionError{phase: SourceQueryPhaseScan, sourceID: query.ID, cause: scanErr}
	}
	if count == 0 {
		return time.Time{}, false, nil
	}
	return latest.UTC(), true, nil
}
