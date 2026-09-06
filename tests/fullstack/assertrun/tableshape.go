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
//   - FINAL is only meaningful on a de-duplicating engine (ReplacingMergeTree,
//     CollapsingMergeTree). A plain MergeTree table -- file_hotspot_daily is the one still
//     seeded here -- rejects it outright.
//
// Rather than hard-code two lists that silently rot when a migration lands, this asks the
// running ClickHouse. The probes are metadata reads against system tables and are instant.
// That is not a nicety: ops migration 087 turned file_complexity_snapshots into a
// ReplacingMergeTree, and because the engine is read here rather than listed in this file,
// the probe followed it without an edit. What did NOT follow is the manifest's expected
// COUNT -- see Engine below and countingContext in verifyfixture.go.
type tableShape struct {
	HasOrgID    bool
	HasRepoID   bool
	SupportsAll bool // FINAL is meaningful for this engine
	// Engine is the engine name system.tables reported, verbatim, kept so a probe failure can
	// say how the count was taken. FINAL counts DISTINCT sorting-key tuples; without it the
	// count is of raw inserted rows. An ops migration that changes a table's engine therefore
	// moves the expectation from one to the other, and a bare "did not match" gives no hint
	// that the engine, rather than the seed, is what moved.
	Engine string
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
	engine = strings.TrimSpace(engine)
	if engine == "" {
		return tableShape{}, fmt.Errorf("table %s does not exist in the migrated schema", table)
	}
	return tableShape{
		HasOrgID:    strings.Contains(columns, "'org_id'"),
		HasRepoID:   strings.Contains(columns, "'repo_id'"),
		SupportsAll: strings.Contains(engine, "Replacing") || strings.Contains(engine, "Collapsing"),
		Engine:      engine,
	}, nil
}

// countingContext describes, for a probe message, HOW the count was taken. It is appended to
// a row-count mismatch so the failure names the engine it observed instead of leaving the
// reader to guess whether the seed, the scoping or the engine moved.
func countingContext(shape tableShape) string {
	if shape.Engine == "" {
		return ""
	}
	if shape.SupportsAll {
		return fmt.Sprintf(" (counted WITH FINAL because system.tables reports engine %s, so this is a count of distinct sorting-key tuples, not of raw inserted rows)", shape.Engine)
	}
	return fmt.Sprintf(" (counted WITHOUT FINAL because system.tables reports engine %s, so this is a count of raw inserted rows)", shape.Engine)
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
