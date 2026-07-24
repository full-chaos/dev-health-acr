package main

import (
	"regexp"
	"strings"
)

// The ops ClickHouse migration directory holds Python migrations alongside the SQL ones, and
// they make real schema changes. Replaying only *.sql produces a schema that is confidently
// wrong: `027_add_org_id_to_sorting_keys.py` adds org_id to a list of tables that migration
// 024 missed (git_commits, git_pull_requests, ci_pipeline_runs, deployments, …) and then
// rebuilds each sorting key so org_id leads it.
//
// These files cannot be executed here, so this reads the shapes that actually occur:
//
//	NAME = [ "table", ... ]                             a table list a loop iterates
//	ALTER TABLE `{table}` ADD/DROP/RENAME COLUMN ...     the templated DDL that loop issues
//	ALTER TABLE `literal` ADD/DROP/RENAME COLUMN ...     a directly-issued statement
//	CREATE TABLE `literal` / DROP TABLE `literal`        a directly-issued statement on a real
//	                                                      (non-shadow) table
//
// Several of the real migrations (010, 042, 049, 055, 061, 067) rebuild a table via SHOW
// CREATE TABLE + a shadow "<table>_new" copy + EXCHANGE TABLES -- a well-established,
// documented pattern that changes ORDER BY/dedup keys, never the column *set* (the shadow's
// DDL is derived from the live SHOW CREATE TABLE text, so it starts from and preserves the
// real table's columns). Because the shadow name and the rebuilt DDL are both runtime values
// (an f-string variable, or a string built from a live query result), they are not literal
// identifiers this file can match at all -- and are not flagged, since they are also
// column-set-neutral by construction. Anything with a literal, non-shadow table name that
// this file does not otherwise recognize IS flagged, whether or not it turns out to matter,
// per the rule below.
//
// Anything it cannot fully interpret is reported rather than ignored — a replay that silently
// skips a migration is worse than none, because it turns into confident false failures (or,
// just as bad, false passes for a seed that would fail against the real schema). See
// unhandledDDL in seedschema.go for how callers are expected to treat a report: fail closed
// when the affected table is unknown or is one the seed actually writes to; a note about a
// definitely-unrelated table may stay a warning.

var (
	pyListAssignment = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)\s*=\s*\[([^\]]*)\]`)
	pyStringLiteral  = regexp.MustCompile(`"([a-z_][a-z0-9_]*)"|'([a-z_][a-z0-9_]*)'`)
	// The migrations wrap DDL across lines inside triple-quoted strings and f-strings, so
	// every separator here must tolerate arbitrary whitespace including newlines.
	pyTemplatedAlter  = regexp.MustCompile("(?i)ALTER\\s+TABLE\\s+`\\{table\\}`\\s+(ADD|DROP)\\s+COLUMN(?:\\s+IF\\s+(?:NOT\\s+)?EXISTS)?\\s*")
	pyTemplatedRename = regexp.MustCompile("(?i)ALTER\\s+TABLE\\s+`\\{table\\}`\\s+RENAME\\s+COLUMN(?:\\s+IF\\s+EXISTS)?\\s+`?([a-z_][a-z0-9_]*)`?\\s+TO\\s+`?([a-z_][a-z0-9_]*)`?")
	pyLiteralAlter    = regexp.MustCompile("(?is)ALTER\\s+TABLE\\s+`?([a-z_][a-z0-9_]*)`?\\s+(ADD|DROP)\\s+COLUMN(?:\\s+IF\\s+(?:NOT\\s+)?EXISTS)?\\s+`?([a-z_][a-z0-9_]*)`?")
	pyLiteralRename   = regexp.MustCompile("(?is)ALTER\\s+TABLE\\s+`?([a-z_][a-z0-9_]*)`?\\s+RENAME\\s+COLUMN(?:\\s+IF\\s+EXISTS)?\\s+`?([a-z_][a-z0-9_]*)`?\\s+TO\\s+`?([a-z_][a-z0-9_]*)`?")
	pyColumnAfter     = regexp.MustCompile(`^\s*(?:f?"|f?')?\s*([a-z_][a-z0-9_]*)\s`)
	pyLoopOverList    = regexp.MustCompile(`for\s+\w+\s+in\s+([A-Z][A-Z0-9_]*)\s*:`)
	// Residual detection: any literal (real-identifier, not "{table}"-templated) ALTER TABLE
	// clause at all, so a clause type this file does not otherwise recognize (MODIFY COLUMN
	// with a shape effect this tool cannot reason about, COMMENT COLUMN with an odd argument,
	// a future clause type, ...) still gets reported rather than silently passing through.
	pyAnyLiteralAlterTable = regexp.MustCompile("(?is)ALTER\\s+TABLE\\s+`?([a-z_][a-z0-9_]*)`?")
	pyLiteralCreateTable   = regexp.MustCompile("(?is)CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?`?([a-z_][a-z0-9_]*)`?")
	pyLiteralDropTable     = regexp.MustCompile("(?is)DROP\\s+TABLE\\s+(?:IF\\s+EXISTS\\s+)?`?([a-z_][a-z0-9_]*)`?")

	// These migrations narrate their algorithm in prose -- a module-level docstring at the
	// very top of the file (e.g. 027, 042), or a function-level one immediately after a
	// "def ...():" line (e.g. 010) -- and that prose routinely says things like "DROP TABLE
	// if concurrent access..." or "4. CREATE TABLE table_new ...". Real DDL never appears
	// there (it is always either a literal client.command("""...""") argument, matched
	// directly by the regexes above, or a dynamically-built string this file cannot see at
	// all); stripping docstrings before the *residual* CREATE/DROP/unrecognized-ALTER scan
	// keeps that scan from misreading narration as code.
	pyModuleDocstringRE = regexp.MustCompile(`(?s)\A\s*("""(?:.*?)"""|'''(?:.*?)''')`)
	pyBlockDocstringRE  = regexp.MustCompile(`(?s):[ \t]*\n[ \t]*("""(?:.*?)"""|'''(?:.*?)''')`)
)

// notAPlausibleTableOrColumnName rejects a captured identifier that is actually a stray
// SQL/Python keyword rather than a real name. Go's RE2 engine has no lookahead, so a pattern
// like `DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?` + capture cannot itself refuse to let "IF" satisfy
// the capture when what follows it is a templated (non-literal) name it cannot match, e.g.
// `DROP TABLE IF EXISTS \`{shadow}\“ -- the optional "IF EXISTS" branch can be skipped so
// "IF" itself becomes the "table name" instead. This denylist is the correction: every one of
// these is either a SQL/Python keyword or an English word that turned up from a genuinely
// unquoted, non-literal statement leaking through the pattern above, never a real ClickHouse
// identifier in this codebase.
var notAPlausibleTableOrColumnName = map[string]bool{
	"if": true, "exists": true, "not": true, "and": true, "or": true,
	"the": true, "to": true, "from": true, "with": true, "as": true, "in": true, "for": true,
	"select": true, "where": true, "table": true, "column": true, "columns": true,
	"drop": true, "create": true, "alter": true, "add": true, "rename": true,
	"statement": true, "output": true, "omitted": true, "ddl": true, "only": true, "after": true,
	"def": true, "class": true, "return": true, "client": true, "command": true,
}

func isPlausibleIdentifier(name string) bool {
	return !notAPlausibleTableOrColumnName[strings.ToLower(name)]
}

// stripPythonDocstrings removes docstrings (module-level, at the very start of the file, and
// function/class-level, immediately after a ":") from source, leaving everything else --
// including every real client.command(...) DDL argument, which is never a bare docstring
// statement -- untouched. applyMigrationPython strips once, up front, so every scan sees
// consistent text and byte offsets.
func stripPythonDocstrings(source string) string {
	source = pyModuleDocstringRE.ReplaceAllString(source, "")
	source = pyBlockDocstringRE.ReplaceAllStringFunc(source, func(m string) string {
		// Keep the leading ":" (and whatever separates it from the string) so line/column
		// bookkeeping elsewhere (none currently relies on it, but this is cheap insurance)
		// and subsequent regex anchoring stay sane; only blank out the quoted text itself.
		colon := strings.IndexByte(m, ':')
		return m[:colon+1]
	})
	return source
}

// looksLikeShadowTable reports whether name matches the "<table>_new" / "*shadow*" naming
// convention every real shadow-table-rebuild migration in this codebase uses for its
// temporary copy. Those tables are created and dropped within the same migration and never
// observed by anything downstream (the seed certainly never INSERTs into one), so DDL that
// only ever names one is not useful to report.
func looksLikeShadowTable(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "_new") || strings.Contains(lower, "shadow")
}

// applyMigrationPython folds a Python migration's recognisable schema effects into the
// schema. It returns an unhandledDDL entry for every piece of DDL it could not (fully)
// interpret, so the caller can surface the uncertainty (and, for verify-seed-schema, fail
// closed when it matters) instead of silently trusting an incomplete replay.
func applyMigrationPython(schema *chSchema, source, fileName string) (unhandled []unhandledDDL) {
	// Strip docstrings once, up front, so every scan below (including the ADD/DROP/RENAME
	// matchers, not just the residual CREATE/DROP/unrecognized-ALTER one) sees the same
	// offsets and the same text -- real DDL is never a bare docstring statement, so this
	// cannot affect anything this file is meant to recognize.
	source = stripPythonDocstrings(source)

	lists := map[string][]string{}
	for _, m := range pyListAssignment.FindAllStringSubmatch(source, -1) {
		var items []string
		for _, lit := range pyStringLiteral.FindAllStringSubmatch(m[2], -1) {
			name := lit[1]
			if name == "" {
				name = lit[2]
			}
			items = append(items, name)
		}
		lists[m[1]] = items
	}

	consumed := map[[2]int]bool{} // byte ranges already attributed, so the residual ALTER TABLE scan does not double-report them

	// A templated `ALTER TABLE \`{table}\`` ADD/DROP is issued once per element of whichever
	// list the enclosing loop walks. Attribute it to the nearest preceding `for ... in LIST:`.
	for _, loc := range pyTemplatedAlter.FindAllStringIndex(source, -1) {
		consumed[[2]int{loc[0], loc[1]}] = true
		match := pyTemplatedAlter.FindStringSubmatch(source[loc[0]:loc[1]])
		column := columnAfterTemplatedAlter(source, loc[1])
		listName := nearestLoopList(source, loc[0])
		tables, ok := lists[listName]
		if !ok || column == "" {
			unhandled = append(unhandled, unhandledDDL{File: fileName, Message: "templated ALTER TABLE it could not attribute to a table list"})
			continue
		}
		for _, table := range tables {
			if !schema.tableExists(table) {
				continue // the real migration skips tables that do not exist
			}
			if strings.EqualFold(match[1], "ADD") {
				schema.addColumn(table, column)
			} else {
				schema.dropColumn(table, column)
			}
		}
	}

	// Templated RENAME COLUMN, same list-attribution strategy.
	for _, loc := range pyTemplatedRename.FindAllStringIndex(source, -1) {
		consumed[[2]int{loc[0], loc[1]}] = true
		match := pyTemplatedRename.FindStringSubmatch(source[loc[0]:loc[1]])
		listName := nearestLoopList(source, loc[0])
		tables, ok := lists[listName]
		if !ok {
			unhandled = append(unhandled, unhandledDDL{File: fileName, Message: "templated RENAME COLUMN it could not attribute to a table list"})
			continue
		}
		for _, table := range tables {
			if !schema.tableExists(table) {
				continue
			}
			schema.dropColumn(table, match[1])
			schema.addColumn(table, match[2])
		}
	}

	for _, loc := range pyLiteralRename.FindAllStringIndex(source, -1) {
		consumed[[2]int{loc[0], loc[1]}] = true
		m := pyLiteralRename.FindStringSubmatch(source[loc[0]:loc[1]])
		if looksLikeShadowTable(m[1]) {
			continue
		}
		if !schema.tableExists(m[1]) {
			continue
		}
		schema.dropColumn(m[1], m[2])
		schema.addColumn(m[1], m[3])
	}

	for _, loc := range pyLiteralAlter.FindAllStringIndex(source, -1) {
		consumed[[2]int{loc[0], loc[1]}] = true
		m := pyLiteralAlter.FindStringSubmatch(source[loc[0]:loc[1]])
		if looksLikeShadowTable(m[1]) {
			continue
		}
		if !schema.tableExists(m[1]) {
			continue
		}
		if strings.EqualFold(m[2], "ADD") {
			schema.addColumn(m[1], m[3])
		} else {
			schema.dropColumn(m[1], m[3])
		}
	}

	// Residual scan: any literal ALTER TABLE on a real (non-shadow) table not already
	// consumed by one of the recognized clause forms above -- an unrecognized clause type,
	// which might change the column set in a way this file cannot reason about.
	for _, loc := range pyAnyLiteralAlterTable.FindAllStringIndex(source, -1) {
		if overlapsConsumed(loc, consumed) {
			continue
		}
		m := pyAnyLiteralAlterTable.FindStringSubmatch(source[loc[0]:loc[1]])
		table := m[1]
		if looksLikeShadowTable(table) || !isPlausibleIdentifier(table) {
			continue
		}
		unhandled = append(unhandled, unhandledDDL{File: fileName, Table: table, Message: "ALTER TABLE clause of an unrecognized kind (not ADD/DROP/RENAME COLUMN)"})
	}

	for _, loc := range pyLiteralCreateTable.FindAllStringIndex(source, -1) {
		m := pyLiteralCreateTable.FindStringSubmatch(source[loc[0]:loc[1]])
		if looksLikeShadowTable(m[1]) || !isPlausibleIdentifier(m[1]) {
			continue
		}
		unhandled = append(unhandled, unhandledDDL{File: fileName, Table: m[1], Message: "literal CREATE TABLE this replay does not interpret"})
	}
	for _, loc := range pyLiteralDropTable.FindAllStringIndex(source, -1) {
		m := pyLiteralDropTable.FindStringSubmatch(source[loc[0]:loc[1]])
		if looksLikeShadowTable(m[1]) || !isPlausibleIdentifier(m[1]) {
			continue
		}
		unhandled = append(unhandled, unhandledDDL{File: fileName, Table: m[1], Message: "literal DROP TABLE this replay does not interpret"})
	}

	return unhandled
}

func overlapsConsumed(loc []int, consumed map[[2]int]bool) bool {
	for span := range consumed {
		if loc[0] >= span[0] && loc[0] < span[1] {
			return true
		}
	}
	return false
}

// columnAfterTemplatedAlter reads the column name that follows the ALTER clause, tolerating
// the f-string continuation the migrations use to wrap long DDL across lines.
func columnAfterTemplatedAlter(source string, from int) string {
	rest := source[from:]
	if idx := strings.IndexAny(rest, "\n"); idx >= 0 && strings.TrimSpace(rest[:idx]) == "\"" {
		rest = rest[idx+1:]
	}
	m := pyColumnAfter.FindStringSubmatch(rest)
	if m == nil {
		return ""
	}
	return m[1]
}

func nearestLoopList(source string, before int) string {
	loops := pyLoopOverList.FindAllStringSubmatchIndex(source[:before], -1)
	if len(loops) == 0 {
		return ""
	}
	last := loops[len(loops)-1]
	return source[last[2]:last[3]]
}
