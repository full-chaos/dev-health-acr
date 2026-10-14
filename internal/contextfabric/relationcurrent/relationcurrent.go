package relationcurrent

import (
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/dependencyrelation"
)

// A relation the provider removed leaves its work_item_dependencies row behind
// (the table is insert-only). A sync of an item writes again only the relations
// that item still has, so a relation is gone when an item that WRITES it has a
// later sync that did not write it. This file is that rule in SQL, the same
// rule as workitemmetrics.RelationIsCurrent and RelationWriters in the ops Go
// module (internal/jobs/metrics/workitemmetrics/blocked.go), with one named
// difference: where the writer cannot be derived (a text-reading provider's
// row with no stored writer and an unrecognized raw value) ops takes both
// items as writers, which ends a live relation early; here the relation ends
// only when BOTH items have a later sync, so a live relation is never dropped.
// The oracle test pins the match and names the difference.

// RelationshipType is the relationship_type value the rule applies to; ops
// RelationIsCurrent covers this type only.
const RelationshipType = "blocks"

// EndpointIDsSQL is the ids of both ends of every RelationshipType row of the
// organization, so a statement's read of work items follows the relations, not
// the size of the work item table.
const EndpointIDsSQL = `(SELECT arrayJoin([be.source_work_item_id, be.target_work_item_id]) FROM work_item_dependencies AS be FINAL WHERE be.org_id = {org_id:String} AND lower(ifNull(be.relationship_type, '')) = '` + RelationshipType + `')`

const (
	WriterSource    = 1
	WriterTarget    = 2
	WriterBoth      = 3
	WriterUndecided = 4
)

// textProviderSQL is workitemmetrics.textRelationProvider over an id
// expression: the provider whose normalizer reads relations from text, else ”.
func textProviderSQL(idExpr string) string {
	prefix := "splitByChar(':', " + idExpr + ")[1]"
	return "multiIf(" + prefix + " IN ('gh', 'ghpr'), 'github', " + prefix + " = 'gitlab', 'gitlab', '')"
}

// phraseSQL is the row's raw value lower-cased with every run of
// Unicode white space (unicode.IsSpace) collapsed to one space and trimmed.
func phraseSQL(d string) string {
	return `trim(BOTH ' ' FROM replaceRegexpAll(lowerUTF8(` + d + `.relationship_type_raw), '[\\t\\n\\x0B\\f\\r \\x{85}\\x{A0}\\x{1680}\\x{2000}-\\x{200A}\\x{2028}\\x{2029}\\x{202F}\\x{205F}\\x{3000}]+', ' '))`
}

// WriterCodeSQL is which of the row's two items write it: the stored
// relation_writer when it is source, target or both, else derived from the row.
func WriterCodeSQL(d string) string {
	src, tgt := d+".source_work_item_id", d+".target_work_item_id"
	srcProvider, tgtProvider := textProviderSQL(src), textProviderSQL(tgt)
	phrase, raw := phraseSQL(d), d+".relationship_type_raw"
	return `multiIf(ifNull(` + d + `.relation_writer, '') = 'source', 1, ifNull(` + d + `.relation_writer, '') = 'target', 2, ifNull(` + d + `.relation_writer, '') = 'both', 3,
    startsWith(` + tgt + `, 'extkey:'), 1,
    ` + srcProvider + ` != ` + tgtProvider + `, 3,
    ` + srcProvider + ` = '', 3,
    ` + srcProvider + ` = 'github' AND (startsWith(` + phrase + `, 'blocked by') OR startsWith(` + phrase + `, 'depends on')), 2,
    ` + srcProvider + ` = 'github' AND startsWith(` + phrase + `, 'blocks'), 1,
    ` + srcProvider + ` = 'gitlab' AND ` + raw + ` = 'blocked by', 2,
    ` + srcProvider + ` = 'gitlab' AND ` + raw + ` = 'blocking', 1,
    4)`
}

// itemReadSQL is, per blocker edge endpoint, the time its relations
// were last read: max(relations_read_at) when a pass that reads relations
// wrote it, else its latest work_items sync (max(last_synced) over the item's
// versions: work_items is replaced by last_synced, so no FINAL merge is read).
// present is 1 for an item with a work_items row (a left join gives a default,
// not NULL, for a missing one).
func itemReadSQL(alias, endpoints string) string {
	return `(SELECT w.work_item_id AS work_item_id, if(r.seen = 1, r.read_at, w.synced) AS synced, 1 AS present
    FROM (SELECT work_item_id, max(last_synced) AS synced FROM work_items WHERE org_id = {org_id:String} AND work_item_id IN ` + endpoints + ` GROUP BY work_item_id) AS w
    LEFT JOIN (SELECT work_item_id, max(relations_read_at) AS read_at, 1 AS seen FROM work_item_relations_read WHERE org_id = {org_id:String} AND work_item_id IN ` + endpoints + ` GROUP BY work_item_id) AS r ON r.work_item_id = w.work_item_id) AS ` + alias
}

// RowsSQL is one row per blocker relation with ended (the provider no
// longer reports it) and code (WriterCodeSQL). Both items' read times
// come from ONE read of the item tables: each relation is split into its two
// ends, the ends are joined to the read times, and the ends are folded back.
func RowsSQL() string {
	return rowsSQL(`lower(ifNull(de.relationship_type, '')) = '`+RelationshipType+`'`, EndpointIDsSQL)
}

// projectionTypes holds the canonical relation keys (dependencyrelation.KeySQL)
// the graph projection treats as the blocks relation: the type in any letter
// case and both inverse spellings, which converge on one graph edge.
const projectionTypes = `('blocks:fwd', 'blocks:inv')`

// projectionEndpointIDsSQL is EndpointIDsSQL over the projection's spellings.
var projectionEndpointIDsSQL = `(SELECT arrayJoin([be.source_work_item_id, be.target_work_item_id]) FROM work_item_dependencies AS be FINAL WHERE be.org_id = {org_id:String} AND ` + dependencyrelation.KeySQL("be.relationship_type") + ` IN ` + projectionTypes + `)`

// ProjectionRowsSQL is RowsSQL over every spelling of the blocks relation, so a
// twin spelling is judged by the same rule instead of being assumed live.
func ProjectionRowsSQL() string {
	return rowsSQL(dependencyrelation.KeySQL("de.relationship_type")+` IN `+projectionTypes, projectionEndpointIDsSQL)
}

func rowsSQL(typeCondition, endpoints string) string {
	return `(SELECT e.s AS s, e.t AS t, e.rt AS rt, e.code AS code,
      multiIf(e.code = 1, sp = 1 AND e.ls < ssync, e.code = 2, tp = 1 AND e.ls < tsync, e.code = 3, (sp = 1 AND e.ls < ssync) OR (tp = 1 AND e.ls < tsync), (sp = 1 AND e.ls < ssync) AND (tp = 1 AND e.ls < tsync)) AS ended,
      multiIf(e.code = 1, ssync, e.code = 2, tsync, e.code = 3, least(if(sp = 1 AND e.ls < ssync, ssync, tsync), if(tp = 1 AND e.ls < tsync, tsync, ssync)), greatest(ssync, tsync)) AS ended_at,
      maxIf(w.synced, e.side = 1) AS ssync, maxIf(w.synced, e.side = 2) AS tsync, sumIf(w.present, e.side = 1) AS sp, sumIf(w.present, e.side = 2) AS tp
    FROM (SELECT de.source_work_item_id AS s, de.target_work_item_id AS t, de.relationship_type AS rt, de.last_synced AS ls, ` + WriterCodeSQL("de") + ` AS code,
        arrayJoin([(1, de.source_work_item_id), (2, de.target_work_item_id)]) AS end_pair, end_pair.1 AS side, end_pair.2 AS end_id
      FROM work_item_dependencies AS de FINAL
      WHERE de.org_id = {org_id:String} AND ` + typeCondition + `) AS e
    LEFT JOIN ` + itemReadSQL("w", endpoints) + ` ON w.work_item_id = e.end_id
    GROUP BY e.s, e.t, e.rt, e.ls, e.code)`
}

// BlockerEndpointIDsSQL is EndpointIDsSQL over every spelling the graph
// projection maps onto a blocks edge (dependencyrelation.BlockerPredicateSQL:
// the same set as projectionEndpointIDsSQL, one trim and one lower per row).
var BlockerEndpointIDsSQL = `(SELECT arrayJoin([be.source_work_item_id, be.target_work_item_id]) FROM work_item_dependencies AS be FINAL WHERE be.org_id = {org_id:String} AND ` + dependencyrelation.BlockerPredicateSQL("be.relationship_type") + `)`

// BlockerRowsSQL is RowsSQL over every spelling of the blocks relation, for the
// fact readers: a pair stored as blocked_by is judged by the same rule as the
// blocks row, and the graph projection judges it the same way.
func BlockerRowsSQL() string {
	return rowsSQL(dependencyrelation.BlockerPredicateSQL("de.relationship_type"), BlockerEndpointIDsSQL)
}

// BlockerEndedKeysSQL is the keys of the blocker rows, of any spelling, the
// provider no longer reports.
func BlockerEndedKeysSQL() string {
	return `(SELECT s, t, rt FROM ` + BlockerRowsSQL() + ` WHERE ended)`
}

// BlockerCurrentSQL keeps the blocker rows, of any spelling, the provider still
// reports. A pair with a live twin keeps the live row, so the edge stays.
func BlockerCurrentSQL(d string) string {
	return ` AND (` + d + `.source_work_item_id, ` + d + `.target_work_item_id, ` + d + `.relationship_type) NOT IN ` + BlockerEndedKeysSQL()
}

// EndedKeysSQL is the keys of the blocker rows the provider no longer
// reports. Every reader tests membership in this one subquery.
func EndedKeysSQL() string {
	return `(SELECT s, t, rt FROM ` + RowsSQL() + ` WHERE ended)`
}

// CurrentSQL keeps the rows the provider still reports.
func CurrentSQL(d string) string {
	return ` AND (` + d + `.source_work_item_id, ` + d + `.target_work_item_id, ` + d + `.relationship_type) NOT IN ` + EndedKeysSQL()
}

// MaxEndedLinks bounds the removed-link set one call carries. The largest
// organization measured holds 2,103 blocks relations, 229 of them removed; the
// bound is about 87 times that. A set over it is cut to the NEWEST removals
// (the most recent end time first, ties by key) and the cut is disclosed
// (EndedLinksSQL returns the total beside the kept keys). A removed link cut
// from the set keeps its edge until the bound is raised and the projection
// rebuilt: removed rows stay in ClickHouse, so the set does not shrink by
// itself.
const MaxEndedLinks = 20000

// A relation the end rule ends is retracted from the graph only when nothing
// else states it. Two spellings of one relation share one graph edge: a blocks
// row (a, b) and an inverse-spelling row (b, a) are the same edge, and a row
// of the same pair under another letter case too. The projection judges every
// spelling by the same rule (ProjectionRowsSQL) and leaves a row out of the
// set while any twin of its edge is still reported.
//
// EndedLinksSQL is, for the organization, ONE row: an array with one element
// per relation the provider no longer reports (at most bound, newest
// first), the total number of such relations, and the number of removed rows
// left out because a twin of their edge is still reported. An element is
// hex(source), hex(target), hex(relationship_type) and the end time in unix
// microseconds, dot separated. Hex keeps any work item id inside the client's
// array binding encoding, and one row keeps the read inside the result-row
// budget however many links were removed. The first column is aliased
// ended_links, the marker a statement carries so a test client can route it.
func EndedLinksSQL(bound int) string {
	key := `concat(hex(s), '.', hex(t), '.', hex(rt), '.', toString(toInt64(toUnixTimestamp64Micro(ended_at))))`
	return fmt.Sprintf(`SELECT arraySlice(arrayMap(x -> x.2, arrayReverseSort(x -> (x.1, x.2), groupArrayIf(tuple(toInt64(toUnixTimestamp64Micro(ended_at)), %s), ended AND live_same_edge = 0))), 1, %d) AS ended_links,
  countIf(ended AND live_same_edge = 0) AS ended_total, countIf(ended AND live_same_edge > 0) AS ended_twin_live
FROM (SELECT s, t, rt, ended, ended_at,
    sum(toUInt8(NOT ended)) OVER (PARTITION BY if(%s = 'blocks:inv', t, s), if(%s = 'blocks:inv', s, t)) AS live_same_edge
  FROM %s)`, key, bound, dependencyrelation.KeySQL("rt"), dependencyrelation.KeySQL("rt"), ProjectionRowsSQL())
}
