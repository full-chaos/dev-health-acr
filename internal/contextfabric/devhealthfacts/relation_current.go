package devhealthfacts

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

const (
	relationWriterSource    = 1
	relationWriterTarget    = 2
	relationWriterBoth      = 3
	relationWriterUndecided = 4
)

// relationTextProviderSQL is workitemmetrics.textRelationProvider over an id
// expression: the provider whose normalizer reads relations from text, else ”.
func relationTextProviderSQL(idExpr string) string {
	prefix := "splitByChar(':', " + idExpr + ")[1]"
	return "multiIf(" + prefix + " IN ('gh', 'ghpr'), 'github', " + prefix + " = 'gitlab', 'gitlab', '')"
}

// relationPhraseSQL is the row's raw value lower-cased with every run of
// Unicode white space (unicode.IsSpace) collapsed to one space and trimmed.
func relationPhraseSQL(d string) string {
	return `trim(BOTH ' ' FROM replaceRegexpAll(lowerUTF8(` + d + `.relationship_type_raw), '[\\t\\n\\x0B\\f\\r \\x{85}\\x{A0}\\x{1680}\\x{2000}-\\x{200A}\\x{2028}\\x{2029}\\x{202F}\\x{205F}\\x{3000}]+', ' '))`
}

// relationWriterCodeSQL is which of the row's two items write it: the stored
// relation_writer when it is source, target or both, else derived from the row.
func relationWriterCodeSQL(d string) string {
	src, tgt := d+".source_work_item_id", d+".target_work_item_id"
	srcProvider, tgtProvider := relationTextProviderSQL(src), relationTextProviderSQL(tgt)
	phrase, raw := relationPhraseSQL(d), d+".relationship_type_raw"
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

// relationEndedSQL is true when the provider no longer reports the row. It
// needs the two joins of relationCurrentJoinsSQL.
func relationEndedSQL(d string) string {
	a := "(rws.present = 1 AND " + d + ".last_synced < rws.synced)"
	b := "(rwt.present = 1 AND " + d + ".last_synced < rwt.synced)"
	return `multiIf(` + relationWriterCodeSQL(d) + ` = 1, ` + a + `, ` + relationWriterCodeSQL(d) + ` = 2, ` + b + `, ` + relationWriterCodeSQL(d) + ` = 3, ` + a + ` OR ` + b + `, ` + a + ` AND ` + b + `)`
}

// relationItemReadSQL is, per blocker edge endpoint, the time its relations
// were last read: max(relations_read_at) when a pass that reads relations
// wrote it, else its latest work_items sync. The latest sync is max(last_synced) over the item's versions
// (work_items is replaced by last_synced), so no FINAL merge is read. present
// is 1 for an item with a
// work_items row (a left join gives a default, not NULL, for a missing one).
func relationItemReadSQL(alias string) string {
	return `(SELECT w.work_item_id AS work_item_id, if(r.seen = 1, r.read_at, w.synced) AS synced, 1 AS present
    FROM (SELECT work_item_id, max(last_synced) AS synced FROM work_items WHERE org_id = {org_id:String} AND work_item_id IN ` + blockerEndpointIDsSQL + ` GROUP BY work_item_id) AS w
    LEFT JOIN (SELECT work_item_id, max(relations_read_at) AS read_at, 1 AS seen FROM work_item_relations_read WHERE org_id = {org_id:String} AND work_item_id IN ` + blockerEndpointIDsSQL + ` GROUP BY work_item_id) AS r ON r.work_item_id = w.work_item_id) AS ` + alias
}

// relationCurrentJoinsSQL joins a blocker edge row d to the read time of its
// source (rws) and target (rwt) item.
func relationCurrentJoinsSQL(d string) string {
	return `
  LEFT JOIN ` + relationItemReadSQL("rws") + ` ON rws.work_item_id = ` + d + `.source_work_item_id
  LEFT JOIN ` + relationItemReadSQL("rwt") + ` ON rwt.work_item_id = ` + d + `.target_work_item_id`
}

// relationEndedKeysSQL is the keys of the blocker rows the provider no longer
// reports. Every reader tests membership in this one subquery, so the two item
// read-time tables are built once per statement, however many arms read edges.
func relationEndedKeysSQL() string {
	return `(SELECT de.source_work_item_id, de.target_work_item_id, de.relationship_type
    FROM work_item_dependencies AS de FINAL` + relationCurrentJoinsSQL("de") + `
    WHERE de.org_id = {org_id:String} AND lower(ifNull(de.relationship_type, '')) = '` + blockerRelationshipType + `' AND ` + relationEndedSQL("de") + `)`
}

// relationCurrentSQL keeps the rows the provider still reports.
func relationCurrentSQL(d string) string {
	return ` AND (` + d + `.source_work_item_id, ` + d + `.target_work_item_id, ` + d + `.relationship_type) NOT IN ` + relationEndedKeysSQL()
}
