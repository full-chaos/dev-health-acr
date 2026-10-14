package orgpurge

import (
	"regexp"
	"sort"
)

// Statements returns every SQL statement Run issues against a table, with the
// table-dependent ones expanded for every purged table. It is built from the
// same functions and constants Run executes.
func Statements() []string {
	var statements []string
	for _, spec := range purgeTables {
		for _, child := range spec.CascadedByDeviceCode {
			statements = append(statements, cascadeCountSQL(spec, child))
		}
		statements = append(statements, countSQL(spec), deleteSQL(spec))
	}
	return append(statements, auditCountSQL, legalHoldCountSQL, markerUpsertSQL, purgedAuditInsertSQL, incompleteAuditInsertSQL)
}

var (
	deleteStatement   = regexp.MustCompile(`(?is)^\s*DELETE FROM acr\.([a-z0-9_]+)`)
	insertStatement   = regexp.MustCompile(`(?is)^\s*INSERT INTO acr\.([a-z0-9_]+)`)
	conflictUpdate    = regexp.MustCompile(`(?is)\bON CONFLICT\b.*\bDO UPDATE\b`)
	anyTableReference = regexp.MustCompile(`acr\.([a-z0-9_]+)`)
)

// Privileges derives, from Statements alone, the table privileges the purge
// needs: DELETE and INSERT for the statement's own target, UPDATE for an upsert,
// and SELECT for every table a statement reads (a WHERE clause on the target, a
// sub-select, a count) and for an upsert's conflict check.
func Privileges() map[string][]string {
	need := map[string]map[string]bool{}
	add := func(table, privilege string) {
		if need[table] == nil {
			need[table] = map[string]bool{}
		}
		need[table][privilege] = true
	}
	for _, statement := range Statements() {
		switch {
		case deleteStatement.MatchString(statement):
			target := deleteStatement.FindStringSubmatch(statement)[1]
			add(target, "DELETE")
			add(target, "SELECT")
		case insertStatement.MatchString(statement):
			target := insertStatement.FindStringSubmatch(statement)[1]
			add(target, "INSERT")
			if conflictUpdate.MatchString(statement) {
				add(target, "UPDATE")
				add(target, "SELECT")
			}
		}
		if !deleteStatement.MatchString(statement) && !insertStatement.MatchString(statement) {
			for _, match := range anyTableReference.FindAllStringSubmatch(statement, -1) {
				add(match[1], "SELECT")
			}
			continue
		}
		for _, match := range anyTableReference.FindAllStringSubmatch(statement, -1) {
			table := match[1]
			if deleteStatement.MatchString(statement) && table == deleteStatement.FindStringSubmatch(statement)[1] {
				continue
			}
			if insertStatement.MatchString(statement) && table == insertStatement.FindStringSubmatch(statement)[1] {
				continue
			}
			add(table, "SELECT")
		}
	}
	result := map[string][]string{}
	for table, privileges := range need {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			if privileges[privilege] {
				result[table] = append(result[table], privilege)
			}
		}
		sort.Strings(result[table])
	}
	return result
}
