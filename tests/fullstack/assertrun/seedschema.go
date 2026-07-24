package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// chSchema is the effective ClickHouse schema (table -> column set) produced by replaying a
// migration directory's *.sql files in lexical filename order, which is the order
// dev-hops's migrator applies them in. It intentionally understands only the small DDL
// subset the ops migrations actually use: CREATE TABLE (column list only; ENGINE/ORDER
// BY/etc. are irrelevant here), ALTER TABLE ADD/DROP COLUMN, and DROP TABLE.
type chSchema struct {
	tables map[string]map[string]bool
}

func newCHSchema() *chSchema {
	return &chSchema{tables: map[string]map[string]bool{}}
}

func (s *chSchema) tableExists(name string) bool {
	_, ok := s.tables[normalizeTableName(name)]
	return ok
}

func (s *chSchema) columnExists(table, column string) bool {
	cols, ok := s.tables[normalizeTableName(table)]
	if !ok {
		return false
	}
	return cols[column]
}

func (s *chSchema) createTable(name string) map[string]bool {
	table := normalizeTableName(name)
	cols := map[string]bool{}
	s.tables[table] = cols
	return cols
}

func (s *chSchema) dropTable(name string) {
	delete(s.tables, normalizeTableName(name))
}

func (s *chSchema) addColumn(table, column string) {
	if cols, ok := s.tables[normalizeTableName(table)]; ok {
		cols[column] = true
	}
}

func (s *chSchema) dropColumn(table, column string) {
	if cols, ok := s.tables[normalizeTableName(table)]; ok {
		delete(cols, column)
	}
}

var (
	createTableRE  = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\S+)\s*\(`)
	alterTableRE   = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(\S+)\s+(.*)$`)
	dropTableRE    = regexp.MustCompile(`(?is)^DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(\S+)`)
	addColumnRE    = regexp.MustCompile(`(?is)^ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?(\S+)`)
	dropColumnRE   = regexp.MustCompile(`(?is)^DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?(\S+)`)
	renameColumnRE = regexp.MustCompile(`(?is)^RENAME\s+COLUMN\s+(?:IF\s+EXISTS\s+)?(\S+)\s+TO\s+(\S+)`)
	// Skip these DDL row-shape entries inside a CREATE TABLE column list -- they name
	// constraints/indexes, not columns.
	nonColumnEntryRE = regexp.MustCompile(`(?i)^(INDEX|CONSTRAINT|PRIMARY\s+KEY|PROJECTION)\b`)
)

// applyMigrationSQL parses one migration file's contents and mutates schema in place.
func applyMigrationSQL(schema *chSchema, sql string) error {
	sql = stripSQLLineComments(sql)
	for _, raw := range splitSQLStatements(sql) {
		stmt := strings.TrimSpace(raw)
		if stmt == "" {
			continue
		}
		if err := applyStatement(schema, stmt); err != nil {
			return err
		}
	}
	return nil
}

func applyStatement(schema *chSchema, stmt string) error {
	switch {
	case createTableRE.MatchString(stmt):
		return applyCreateTable(schema, stmt)
	case dropTableRE.MatchString(stmt):
		m := dropTableRE.FindStringSubmatch(stmt)
		schema.dropTable(m[1])
	case alterTableRE.MatchString(stmt):
		return applyAlterTable(schema, stmt)
	default:
		// Every other statement (SET, INSERT, CREATE DATABASE, comments-only, ...) is
		// irrelevant to column shape; ignore it.
	}
	return nil
}

func applyCreateTable(schema *chSchema, stmt string) error {
	m := createTableRE.FindStringSubmatch(stmt)
	openIdx := strings.Index(stmt, "(")
	if openIdx < 0 {
		return fmt.Errorf("CREATE TABLE %s: no column list", m[1])
	}
	closeIdx, err := findMatchingParen(stmt, openIdx)
	if err != nil {
		return fmt.Errorf("CREATE TABLE %s: %w", m[1], err)
	}
	body := []rune(stmt)[openIdx+1 : closeIdx]
	cols := schema.createTable(m[1])
	for _, entry := range splitTopLevel(string(body), ',', true) {
		entry = strings.TrimSpace(entry)
		if entry == "" || nonColumnEntryRE.MatchString(entry) {
			continue
		}
		if name := firstIdentifier(entry); name != "" {
			cols[name] = true
		}
	}
	return nil
}

func applyAlterTable(schema *chSchema, stmt string) error {
	m := alterTableRE.FindStringSubmatch(stmt)
	table, rest := m[1], m[2]
	if !schema.tableExists(table) {
		// A migration altering a table this replay never saw CREATE'd (e.g. the migration
		// directory starts mid-history) is not this tool's problem to diagnose; create it
		// permissively so ADD COLUMN still records real columns instead of being lost.
		schema.createTable(table)
	}
	cols := schema.tables[normalizeTableName(table)]
	for _, clause := range splitTopLevel(rest, ',', true) {
		clause = strings.TrimSpace(clause)
		switch {
		case addColumnRE.MatchString(clause):
			col := addColumnRE.FindStringSubmatch(clause)[1]
			cols[firstIdentifier(col)] = true
		case dropColumnRE.MatchString(clause):
			col := dropColumnRE.FindStringSubmatch(clause)[1]
			delete(cols, firstIdentifier(col))
		case renameColumnRE.MatchString(clause):
			m := renameColumnRE.FindStringSubmatch(clause)
			delete(cols, firstIdentifier(m[1]))
			cols[firstIdentifier(m[2])] = true
		default:
			// MODIFY COLUMN, COMMENT COLUMN, CODEC, TTL, etc. -- these change a column's type
			// or metadata, never its name or existence, so they are genuinely column-set
			// neutral for this tool's purpose (catching a seed file referencing a column that
			// does not exist). RENAME COLUMN is the one ALTER clause that changes the column
			// *set* without an ADD/DROP keyword, so it is handled explicitly above rather than
			// falling in here (Codex finding 12).
		}
	}
	return nil
}

// unhandledDDL is one piece of migration DDL the replay could not (fully) interpret. Table is
// set whenever the affected table name is actually known (a literal identifier the code just
// does not know the DDL *kind* for), even though the DDL's effect on that table's columns is
// unknown; it is "" only when attribution itself failed (e.g. a templated Python ALTER whose
// table list could not be resolved at all) -- genuinely no idea which table(s) might be
// affected. Callers (verify-seed-schema) must treat both cases as high-risk: a note with a
// known table can be judged against the tables the seed actually writes to, but a note with
// an unknown table can never be safely assumed unrelated.
type unhandledDDL struct {
	File    string
	Table   string // "" when the affected table could not be determined at all
	Message string
}

func (u unhandledDDL) String() string {
	if u.Table != "" {
		return fmt.Sprintf("%s: %s (table: %s)", u.File, u.Message, u.Table)
	}
	return fmt.Sprintf("%s: %s (table: unknown -- cannot rule out any table)", u.File, u.Message)
}

// replayMigrationsDir builds the effective schema from every *.sql/*.py file directly inside
// dir, applied in lexical filename order (dev-hops's migrator's own ordering contract: e.g.
// 027_*.py runs between 026_*.sql and 028_*.sql, and it adds columns later files and the seed
// depend on). The second return value lists DDL applyMigrationPython could not fully
// interpret -- callers must surface these rather than silently trusting a replay that may be
// an incomplete picture of the schema (see pymigration.go).
func replayMigrationsDir(dir string) (*chSchema, []unhandledDDL, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read migrations dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "__init__.py" {
			continue
		}
		if strings.HasSuffix(e.Name(), ".sql") || strings.HasSuffix(e.Name(), ".py") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("no migration files found in %s", dir)
	}
	schema := newCHSchema()
	var unhandled []unhandledDDL
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		if strings.HasSuffix(name, ".py") {
			unhandled = append(unhandled, applyMigrationPython(schema, string(data), name)...)
			continue
		}
		if err := applyMigrationSQL(schema, string(data)); err != nil {
			return nil, nil, fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return schema, unhandled, nil
}

// --- seed INSERT verification ---

// seedIssue is one seed-vs-schema mismatch: either a table/column that does not exist in the
// effective schema, or a VALUES tuple whose arity does not match its column list.
type seedIssue struct {
	File    string
	Table   string
	Column  string // set for a bad-column issue
	Tuple   int    // set (1-based) for an arity issue
	Wanted  int
	Got     int
	Message string
}

func (i seedIssue) String() string {
	if i.Column != "" {
		return fmt.Sprintf("%s: %s.%s does not exist in the effective ClickHouse schema", i.File, i.Table, i.Column)
	}
	return fmt.Sprintf("%s: %s VALUES tuple #%d has %d value(s), expected %d (column list arity)", i.File, i.Table, i.Tuple, i.Got, i.Wanted)
}

var insertRE = regexp.MustCompile(`(?is)^INSERT\s+INTO\s+(\S+)\s*\(`)

// seedTouchedTables returns the set of (normalized) table names seedSQL's INSERT INTO
// statements write to. Used to decide whether an unhandledDDL note concerns a table the seed
// actually cares about (fail closed) or a genuinely unrelated one (stays a warning).
func seedTouchedTables(seedSQL string) stringSet {
	touched := newStringSet()
	cleaned := stripSQLLineComments(seedSQL)
	for _, raw := range splitSQLStatements(cleaned) {
		stmt := strings.TrimSpace(raw)
		if insertRE.MatchString(stmt) {
			touched.add(normalizeTableName(insertRE.FindStringSubmatch(stmt)[1]))
		}
	}
	return touched
}

// verifySeedAgainstSchema parses every INSERT INTO statement in seedSQL and checks it against
// schema: the table must exist, every named column must exist on it, and every VALUES tuple
// must have exactly as many elements as the column list.
func verifySeedAgainstSchema(schema *chSchema, seedSQL, fileName string) ([]seedIssue, error) {
	var issues []seedIssue
	cleaned := stripSQLLineComments(seedSQL)
	for _, raw := range splitSQLStatements(cleaned) {
		stmt := strings.TrimSpace(raw)
		if stmt == "" || !insertRE.MatchString(stmt) {
			continue
		}
		m := insertRE.FindStringSubmatch(stmt)
		table := m[1]
		openIdx := strings.Index(stmt, "(")
		closeIdx, err := findMatchingParen(stmt, openIdx)
		if err != nil {
			return nil, fmt.Errorf("%s: INSERT INTO %s: %w", fileName, table, err)
		}
		colList := []rune(stmt)[openIdx+1 : closeIdx]
		var columns []string
		for _, c := range splitTopLevel(string(colList), ',', true) {
			if name := firstIdentifier(c); name != "" {
				columns = append(columns, name)
			}
		}

		if !schema.tableExists(table) {
			issues = append(issues, seedIssue{File: fileName, Table: normalizeTableName(table), Column: "<table>", Message: "table does not exist"})
			continue
		}
		for _, col := range columns {
			if !schema.columnExists(table, col) {
				issues = append(issues, seedIssue{File: fileName, Table: normalizeTableName(table), Column: col})
			}
		}

		valuesIdx := findKeyword(stmt, closeIdx+1, "VALUES")
		if valuesIdx < 0 {
			return nil, fmt.Errorf("%s: INSERT INTO %s: no VALUES clause found", fileName, table)
		}
		tuples, err := splitValueTuples(stmt[valuesIdx+len("VALUES"):])
		if err != nil {
			return nil, fmt.Errorf("%s: INSERT INTO %s: %w", fileName, table, err)
		}
		for idx, tuple := range tuples {
			got := len(splitTopLevel(tuple, ',', false))
			if got != len(columns) {
				issues = append(issues, seedIssue{
					File: fileName, Table: normalizeTableName(table),
					Tuple: idx + 1, Wanted: len(columns), Got: got,
				})
			}
		}
	}
	return issues, nil
}

// findKeyword finds the next case-insensitive whole-word occurrence of keyword in s at or
// after from, returning -1 if not found.
func findKeyword(s string, from int, keyword string) int {
	if from > len(s) {
		return -1
	}
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(keyword) + `\b`)
	loc := re.FindStringIndex(s[from:])
	if loc == nil {
		return -1
	}
	return from + loc[0]
}

// splitValueTuples splits the text after VALUES into its parenthesized tuples, e.g.
// " (1, 'a'), (2, 'b');" -> ["1, 'a'", "2, 'b'"].
func splitValueTuples(s string) ([]string, error) {
	var tuples []string
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		for i < len(runes) && runes[i] != '(' {
			i++
		}
		if i >= len(runes) {
			break
		}
		closeIdx, err := findMatchingParen(s, i)
		if err != nil {
			return nil, err
		}
		tuples = append(tuples, string(runes[i+1:closeIdx]))
		i = closeIdx + 1
	}
	if len(tuples) == 0 {
		return nil, fmt.Errorf("no value tuples found")
	}
	return tuples, nil
}
