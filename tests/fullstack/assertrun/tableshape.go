package main

import (
	"fmt"
	"strings"
)

// tableShape is what a row-count probe needs to know about a table before it can be counted
// correctly. Both facts vary across the Dev Health schema and getting either wrong turns a
// green fixture into an opaque ClickHouse exception:
//
//   - Not every table carries both org_id and repo_id. work_item_dependencies, for example,
//     is keyed by (org_id, source_work_item_id, target_work_item_id, relationship_type) and
//     has no repo_id at all, so a repo-scoped count against it is a hard error.
//   - FINAL is only valid on a ReplacingMergeTree. file_hotspot_daily and
//     file_complexity_snapshots are aggregating tables and reject it outright.
//
// Rather than hard-code two lists that silently rot when a migration lands, this asks the
// running ClickHouse. The probes are metadata reads against system tables and are instant.
type tableShape struct {
	HasOrgID    bool
	HasRepoID   bool
	SupportsAll bool // FINAL is meaningful for this engine
}

func describeTable(table string, probeWords []string) (tableShape, error) {
	columns, err := runProbeCommand(probeWords, fmt.Sprintf(
		"SELECT groupArray(name) FROM system.columns WHERE database = currentDatabase() AND table = '%s' AND name IN ('org_id','repo_id')", table))
	if err != nil {
		return tableShape{}, fmt.Errorf("describe %s columns: %w", table, err)
	}
	engine, err := runProbeCommand(probeWords, fmt.Sprintf(
		"SELECT engine FROM system.tables WHERE database = currentDatabase() AND name = '%s'", table))
	if err != nil {
		return tableShape{}, fmt.Errorf("describe %s engine: %w", table, err)
	}
	if strings.TrimSpace(engine) == "" {
		return tableShape{}, fmt.Errorf("table %s does not exist in the migrated schema", table)
	}
	return tableShape{
		HasOrgID:    strings.Contains(columns, "'org_id'"),
		HasRepoID:   strings.Contains(columns, "'repo_id'"),
		SupportsAll: strings.Contains(engine, "Replacing") || strings.Contains(engine, "Collapsing"),
	}, nil
}

// rowCountSQL builds the count for one seeded table, scoped the way that table can actually
// be scoped.
func rowCountSQL(table, slug, repoID, orgID string, shape tableShape) string {
	final := ""
	if shape.SupportsAll {
		final = " FINAL"
	}
	if table == "repos" {
		return fmt.Sprintf("SELECT count() FROM repos%s WHERE org_id = %s AND repo = '%s'", final, quotedOrgID(orgID), slug)
	}
	switch {
	case shape.HasOrgID && shape.HasRepoID:
		return fmt.Sprintf("SELECT count() FROM %s%s WHERE org_id = %s AND repo_id = '%s'", table, final, quotedOrgID(orgID), repoID)
	case shape.HasRepoID:
		// No org_id column: the row is reachable only through its repository, and repo_id is
		// already org-scoped because repos itself is filtered by org_id.
		return fmt.Sprintf("SELECT count() FROM %s%s WHERE repo_id = '%s'", table, final, repoID)
	case shape.HasOrgID:
		// No repo_id column at all (work_item_dependencies and friends). The count is
		// org-scoped only; the manifest lists such a table under every repository, so the
		// same org-wide number is expected for each.
		return fmt.Sprintf("SELECT count() FROM %s%s WHERE org_id = %s", table, final, quotedOrgID(orgID))
	default:
		return fmt.Sprintf("SELECT count() FROM %s%s", table, final)
	}
}
