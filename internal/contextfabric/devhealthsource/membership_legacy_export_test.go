package devhealthsource

import (
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The membership statement and its project resolution as they were before
// the page was cut ahead of the resolution: the oracle the answer-equivalence
// cell compares the producer's statement with, byte for byte from acr main
// c3a32334.

const legacyResolvedProjectsSubquery = `(
  SELECT provider, id, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT provider, id, join_key FROM (
      SELECT provider, id, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT provider, id, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
  LIMIT 1 BY provider, join_key
)`

func legacyMembershipStatement(cursor cursorState, ingest, pruned bool) string {
	var incrementalSettings string
	if pruned && !cursor.Since.IsZero() {
		// Measured on a real server: with the subjects pruned, the memory
		// left is the per-thread buffers of the three arms (about 50 MB at
		// 4 threads, 17 MB at one), not the data.
		incrementalSettings = " SETTINGS max_threads = 1"
	}
	cursorColumn := "observed_at"
	if ingest {
		cursorColumn = "ingest_at"
	}
	rowKey := membershipRowKey()
	return legacyMembershipRows(cursor, ingest, pruned) + sincePredicate(cursor, cursorColumn, rowKey) + orderBy(cursorColumn, rowKey) + incrementalSettings
}

func legacyMembershipRows(cursor cursorState, ingest, pruned bool) string {
	var changedTouches, changedSuperseded, changedPresence string
	if pruned {
		changedTouches, changedSuperseded, changedPresence = changedMembershipFilters(cursor, ingest)
	}
	// toDateTime64(0, 3, 'UTC') matches project_membership_transitions.
	// occurred_at's own declared scale (DateTime64(3)) -- the NULL branch of
	// transition arm's `valid_to` (membershipIntervalsSubquery) is derived
	// from that column via leadInFrame, so the ifNull default must share its
	// scale for the two UNION arms to agree on one column type.
	// ingest: the cursor pages on the ingest stamp (the transitions'
	// ingested_at, folded with the adjacent touches, and the presence view's
	// last_synced) selected as the LAST column; otherwise on observed_at.
	ingestSelect, transitionIngest, columnIngest, supersededIngest := "", "", "", ""
	if ingest {
		ingestSelect = ", ingest_at"
		transitionIngest, columnIngest, supersededIngest = ", m.ingest_at AS ingest_at", ", m.last_synced AS ingest_at", ", m.ingest_at AS ingest_at"
	}
	return `SELECT subject_kind, repo_id_str, subject_id, repo_slug, observed_at, event_id, source, provider, project_id, resolved_project_id, key_resolution_count, valid_to_present, valid_to_value, is_malformed, is_duplicate_add` + ingestSelect + `
FROM (
  SELECT m.subject_kind AS subject_kind, toString(m.repo_id) AS repo_id_str, m.subject_id AS subject_id, ifNull(r.repo, '') AS repo_slug,
    m.observed_at AS observed_at, m.event_id AS event_id, m.source AS source, m.provider AS provider, m.project_id AS project_id, p.id AS resolved_project_id, p.key_resolution_count AS key_resolution_count,
    isNotNull(m.valid_to) AS valid_to_present, ifNull(m.valid_to, toDateTime64(0, 3, 'UTC')) AS valid_to_value, m.is_malformed AS is_malformed, m.is_duplicate_add AS is_duplicate_add` + transitionIngest + `
  FROM ` + membershipIntervalsSubquery(ingest, changedTouches) + ` AS m
  LEFT JOIN ` + legacyResolvedProjectsSubquery + ` AS p ON p.provider = m.provider AND p.join_key = m.project_id
  LEFT JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
  UNION ALL
  SELECT m.subject_kind AS subject_kind, toString(m.repo_id) AS repo_id_str, m.subject_id AS subject_id, ifNull(r.repo, '') AS repo_slug,
    m.observed_at AS observed_at, '' AS event_id, m.source AS source, m.provider AS provider, m.project_id AS project_id, p.id AS resolved_project_id, p.key_resolution_count AS key_resolution_count,
    0 AS valid_to_present, toDateTime64(0, 3, 'UTC') AS valid_to_value, 0 AS is_malformed, 0 AS is_duplicate_add` + columnIngest + `
  FROM project_membership_presence AS m
  LEFT JOIN ` + legacyResolvedProjectsSubquery + ` AS p ON p.provider = m.provider AND p.join_key = m.project_id
  LEFT JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
  WHERE m.org_id = {org_id:String} AND m.source = 'work_item_column'` + changedPresence + `
  UNION ALL
  SELECT 'work_item' AS subject_kind, toString(m.repo_id) AS repo_id_str, m.subject_id AS subject_id, ifNull(r.repo, '') AS repo_slug,
    m.observed_at AS observed_at, '' AS event_id, 'column_superseded' AS source, m.provider AS provider, m.project_id AS project_id, p.id AS resolved_project_id, p.key_resolution_count AS key_resolution_count,
    0 AS valid_to_present, toDateTime64(0, 3, 'UTC') AS valid_to_value, 0 AS is_malformed, 0 AS is_duplicate_add` + supersededIngest + `
  FROM ` + supersededColumnsSubquery(ingest, changedSuperseded) + ` AS m
  LEFT JOIN ` + legacyResolvedProjectsSubquery + ` AS p ON p.provider = m.provider AND p.join_key = m.project_id
  LEFT JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
)
WHERE 1 = 1`
}

// LegacyMembershipStatementForTest renders the oracle statement at a cursor,
// with the same bindings MembershipStatementForTest gives.
func LegacyMembershipStatementForTest(orgID string, since time.Time, after string, limit int, ingest, pruned bool) (string, []contextpacket.ClickHouseBinding) {
	cursor := cursorState{Since: since, After: after}
	return legacyMembershipStatement(cursor, ingest, pruned), rowLimitBindings(orgID, cursor, limit)
}

// MembershipEdgeCountSQLForTest returns the edge count statement and the same
// count read over the oracle's row source.
func MembershipEdgeCountSQLForTest(ingest bool) (current, legacy string) {
	current = membershipEdgeCountSQL(ingest)
	rows := membershipRows(cursorState{}, ingest, false)
	if !strings.Contains(current, rows) {
		panic("membershipEdgeCountSQL no longer embeds membershipRows")
	}
	return current, strings.Replace(current, rows, legacyMembershipRows(cursorState{}, ingest, false), 1)
}
