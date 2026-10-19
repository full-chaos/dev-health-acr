package relationcurrent

import (
	"fmt"
	"strings"

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

// unknownWriterKinds are the relationship_type_raw values of non-blocks rows
// whose writer the stored row does not show and whose item can be read again
// without the link being written again: a pass can skip the read that writes
// it (jira_dev_status past the per-run request cap, gitlab_closing_reference
// on a failed closed_by fetch, github_comment_linear_url when comments are
// not read), or one item alone writes the row and no writer is stored
// (linear_attachment, github_closing_reference). The end rule never ends such
// a row, so its edge stays, and the projection counts the rows it kept. The
// writer and read-time rule for these kinds belongs to ops (follow-up 10166).
var unknownWriterKinds = []string{"github_closing_reference", "github_comment_linear_url", "gitlab_closing_reference", "jira_dev_status", "linear_attachment"}

// projectionWriterCodeSQL is WriterCodeSQL for a blocks row of any spelling.
// A row of another type ends by its stored relation_writer, by the item whose
// text names an external key, or else only when BOTH items were read again
// (WriterUndecided): ops derives writers for the blocks relation only.
func projectionWriterCodeSQL(d string) string {
	writer := `ifNull(` + d + `.relation_writer, '')`
	return `multiIf(` + dependencyrelation.BlockerPredicateSQL(d+".relationship_type") + `, ` + WriterCodeSQL(d) + `,
    ` + writer + ` = 'source', 1, ` + writer + ` = 'target', 2, ` + writer + ` = 'both', 3,
    startsWith(` + d + `.target_work_item_id, 'extkey:'), 1,
    4)`
}

// projectionKeptSQL is 1 for a non-blocks row of an unknownWriterKinds raw
// value.
func projectionKeptSQL(d string) string {
	kinds := make([]string, 0, len(unknownWriterKinds))
	for _, kind := range unknownWriterKinds {
		kinds = append(kinds, "'"+kind+"'")
	}
	return `toUInt8(NOT ` + dependencyrelation.BlockerPredicateSQL(d+".relationship_type") + ` AND ifNull(` + d + `.relationship_type_raw, '') IN (` + strings.Join(kinds, ", ") + `))`
}

// projectionTypeConditionSQL keeps every row the graph projection reads as a
// dependency edge: any type but the ignored ones (ignoredKeys, as
// dependencyrelation.KeySQL renders them).
func projectionTypeConditionSQL(alias string, ignoredKeys []string) string {
	if len(ignoredKeys) == 0 {
		return `1`
	}
	quoted := make([]string, 0, len(ignoredKeys))
	for _, key := range ignoredKeys {
		quoted = append(quoted, "'"+key+"'")
	}
	return dependencyrelation.KeySQL(alias+".relationship_type") + ` NOT IN (` + strings.Join(quoted, ", ") + `)`
}

// ProjectionRowsSQL is RowsSQL over every dependency type the graph projection
// asserts (all but ignoredTypes), with a kept column: 1 for a row of an
// unknownWriterKinds raw value, which is never ended. A twin spelling is
// judged by the same rule instead of being assumed live.
func ProjectionRowsSQL(ignoredTypes []string) string {
	ignoredKeys := make([]string, 0, len(ignoredTypes))
	for _, typ := range ignoredTypes {
		ignoredKeys = append(ignoredKeys, dependencyrelation.Key(typ))
	}
	endpoints := `(SELECT arrayJoin([be.source_work_item_id, be.target_work_item_id]) FROM work_item_dependencies AS be FINAL WHERE be.org_id = {org_id:String} AND ` + projectionTypeConditionSQL("be", ignoredKeys) + `)`
	return rowsWith(projectionTypeConditionSQL("de", ignoredKeys), endpoints, projectionWriterCodeSQL("de"), projectionKeptSQL("de"))
}

func rowsSQL(typeCondition, endpoints string) string {
	return rowsWith(typeCondition, endpoints, WriterCodeSQL("de"), "")
}

// rowsWith is the rule over the rows of typeCondition with writer code code;
// a non-empty kept expression adds a kept column.
func rowsWith(typeCondition, endpoints, code, kept string) string {
	keptOut, keptIn, keptGroup := "", "", ""
	if kept != "" {
		keptOut, keptIn, keptGroup = ` e.kept AS kept,`, ` `+kept+` AS kept,`, `, e.kept`
	}
	return `(SELECT e.s AS s, e.t AS t, e.rt AS rt, e.code AS code,` + keptOut + `
      multiIf(e.code = 1, sp = 1 AND e.ls < ssync, e.code = 2, tp = 1 AND e.ls < tsync, e.code = 3, (sp = 1 AND e.ls < ssync) OR (tp = 1 AND e.ls < tsync), (sp = 1 AND e.ls < ssync) AND (tp = 1 AND e.ls < tsync)) AS ended,
      multiIf(e.code = 1, ssync, e.code = 2, tsync, e.code = 3, least(if(sp = 1 AND e.ls < ssync, ssync, tsync), if(tp = 1 AND e.ls < tsync, tsync, ssync)), greatest(ssync, tsync)) AS ended_at,
      maxIf(w.synced, e.side = 1) AS ssync, maxIf(w.synced, e.side = 2) AS tsync, sumIf(w.present, e.side = 1) AS sp, sumIf(w.present, e.side = 2) AS tp
    FROM (SELECT de.source_work_item_id AS s, de.target_work_item_id AS t, de.relationship_type AS rt, de.last_synced AS ls, ` + code + ` AS code,` + keptIn + `
        arrayJoin([(1, de.source_work_item_id), (2, de.target_work_item_id)]) AS end_pair, end_pair.1 AS side, end_pair.2 AS end_id
      FROM work_item_dependencies AS de FINAL
      WHERE de.org_id = {org_id:String} AND ` + typeCondition + `) AS e
    LEFT JOIN ` + itemReadSQL("w", endpoints) + ` ON w.work_item_id = e.end_id
    GROUP BY e.s, e.t, e.rt, e.ls, e.code` + keptGroup + `)`
}

// BlockerEndpointIDsSQL is EndpointIDsSQL over every spelling the graph
// projection maps onto a blocks edge (dependencyrelation.BlockerPredicateSQL:
// KeySQL IN (blocks:fwd, blocks:inv), one trim and one lower per row).
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
// bound is about 87 times that, and the set now holds removed links of every
// dependency type. A set over it is cut to the NEWEST removals (the most
// recent end time first, ties by key) and the cut is disclosed (EndedLinksSQL
// returns the total beside the kept keys). A removed link cut from the set
// keeps its edge until the bound is raised and the projection rebuilt: removed
// rows stay in ClickHouse, so the set does not shrink by itself.
const MaxEndedLinks = 20000

// A relation the end rule ends is retracted from the graph only when nothing
// else states it. Two spellings of one relation share one graph edge: a row
// (a, b) and an inverse-spelling row (b, a) of the same type are the same
// edge, and a row of the same pair under another letter case or an alias of
// the type too (the edge is the canonical type of dependencyrelation.Key with
// the endpoints in its direction). The projection judges every spelling by the
// same rule (ProjectionRowsSQL) and leaves a row out of the set while any twin
// of its edge is still reported or kept.
//
// EndedLinksSQL is, for the organization, ONE row: an array with one element
// per relation the provider no longer reports (at most bound, newest first),
// the total number of such relations, the number of removed rows left out
// because a twin of their edge is still reported, and the number of rows the
// rule would end but keeps because their writer is not known
// (unknownWriterKinds). ignoredTypes are the dependency types the projection
// asserts no edge for; their rows are not read. An element is hex(source),
// hex(target), hex(relationship_type) and the end time in unix microseconds,
// dot separated. Hex keeps any work item id inside the client's array binding
// encoding, and one row keeps the read inside the result-row budget however
// many links were removed. The first column is aliased ended_links, the marker
// a statement carries so a test client can route it.
func EndedLinksSQL(bound int, ignoredTypes []string) string {
	key := `concat(hex(s), '.', hex(t), '.', hex(rt), '.', toString(toInt64(toUnixTimestamp64Micro(ended_at))))`
	return fmt.Sprintf(`SELECT arraySlice(arrayMap(x -> x.2, arrayReverseSort(x -> (x.1, x.2), groupArrayIf(tuple(toInt64(toUnixTimestamp64Micro(ended_at)), %s), gone = 1 AND live_same_edge = 0))), 1, %d) AS ended_links,
  countIf(gone = 1 AND live_same_edge = 0) AS ended_total, countIf(gone = 1 AND live_same_edge > 0) AS ended_twin_live, countIf(ended AND kept = 1) AS ended_unknown_writer
FROM (SELECT s, t, rt, ended, kept, gone, ended_at,
    sum(toUInt8(gone = 0)) OVER (PARTITION BY edge_type, edge_from, edge_to) AS live_same_edge
  FROM (SELECT s, t, rt, ended, kept, ended_at, toUInt8(ended AND kept = 0) AS gone,
      substring(k, 1, length(k) - 4) AS edge_type, if(endsWith(k, ':inv'), t, s) AS edge_from, if(endsWith(k, ':inv'), s, t) AS edge_to
    FROM (SELECT s, t, rt, ended, kept, ended_at, %s AS k FROM %s)))`, key, bound, dependencyrelation.KeySQL("rt"), ProjectionRowsSQL(ignoredTypes))
}
