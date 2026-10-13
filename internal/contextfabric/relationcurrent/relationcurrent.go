package relationcurrent

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
func itemReadSQL(alias string) string {
	return `(SELECT w.work_item_id AS work_item_id, if(r.seen = 1, r.read_at, w.synced) AS synced, 1 AS present
    FROM (SELECT work_item_id, max(last_synced) AS synced FROM work_items WHERE org_id = {org_id:String} AND work_item_id IN ` + EndpointIDsSQL + ` GROUP BY work_item_id) AS w
    LEFT JOIN (SELECT work_item_id, max(relations_read_at) AS read_at, 1 AS seen FROM work_item_relations_read WHERE org_id = {org_id:String} AND work_item_id IN ` + EndpointIDsSQL + ` GROUP BY work_item_id) AS r ON r.work_item_id = w.work_item_id) AS ` + alias
}

// RowsSQL is one row per blocker relation with ended (the provider no
// longer reports it) and code (WriterCodeSQL). Both items' read times
// come from ONE read of the item tables: each relation is split into its two
// ends, the ends are joined to the read times, and the ends are folded back.
func RowsSQL() string {
	return `(SELECT e.s AS s, e.t AS t, e.rt AS rt, e.code AS code,
      multiIf(e.code = 1, sp = 1 AND e.ls < ssync, e.code = 2, tp = 1 AND e.ls < tsync, e.code = 3, (sp = 1 AND e.ls < ssync) OR (tp = 1 AND e.ls < tsync), (sp = 1 AND e.ls < ssync) AND (tp = 1 AND e.ls < tsync)) AS ended,
      multiIf(e.code = 1, ssync, e.code = 2, tsync, e.code = 3, least(if(sp = 1 AND e.ls < ssync, ssync, tsync), if(tp = 1 AND e.ls < tsync, tsync, ssync)), greatest(ssync, tsync)) AS ended_at,
      maxIf(w.synced, e.side = 1) AS ssync, maxIf(w.synced, e.side = 2) AS tsync, sumIf(w.present, e.side = 1) AS sp, sumIf(w.present, e.side = 2) AS tp
    FROM (SELECT de.source_work_item_id AS s, de.target_work_item_id AS t, de.relationship_type AS rt, de.last_synced AS ls, ` + WriterCodeSQL("de") + ` AS code,
        arrayJoin([(1, de.source_work_item_id), (2, de.target_work_item_id)]) AS end_pair, end_pair.1 AS side, end_pair.2 AS end_id
      FROM work_item_dependencies AS de FINAL
      WHERE de.org_id = {org_id:String} AND lower(ifNull(de.relationship_type, '')) = '` + RelationshipType + `') AS e
    LEFT JOIN ` + itemReadSQL("w") + ` ON w.work_item_id = e.end_id
    GROUP BY e.s, e.t, e.rt, e.ls, e.code)`
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
