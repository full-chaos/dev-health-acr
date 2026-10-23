package devhealthschema

import (
	"fmt"
	"strings"
)

// OpsCreate is one ops baseline CREATE TABLE statement split into the facets
// ProductionColumns and EngineFull declare.
type OpsCreate struct {
	Table   string
	Columns []Column
	Engine  string
}

// ParseOpsCreate splits a CREATE TABLE statement as ops baseline/head.json
// stores it. Column COMMENT clauses and a table-level COMMENT are dropped:
// system.columns.type and system.tables.engine_full do not carry them.
func ParseOpsCreate(statement string) (OpsCreate, error) {
	const prefix = "CREATE TABLE "
	if !strings.HasPrefix(statement, prefix) {
		return OpsCreate{}, fmt.Errorf("not a CREATE TABLE statement: %.40q", statement)
	}
	rest := statement[len(prefix):]
	open := strings.Index(rest, " (")
	if open < 0 {
		return OpsCreate{}, fmt.Errorf("no column list in %.40q", statement)
	}
	out := OpsCreate{Table: rest[:open]}
	body := rest[open+2:]
	closeAt := matchingParen(body)
	if closeAt < 0 {
		return OpsCreate{}, fmt.Errorf("unbalanced column list in %s", out.Table)
	}
	for _, part := range splitTopLevel(body[:closeAt]) {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "`") {
			return OpsCreate{}, fmt.Errorf("%s: column without backtick name: %.40q", out.Table, part)
		}
		end := strings.Index(part[1:], "`")
		name := part[1 : 1+end]
		out.Columns = append(out.Columns, Column{Name: name, Type: stripComment(strings.TrimSpace(part[end+2:]))})
	}
	tail := strings.TrimSpace(body[closeAt+1:])
	if !strings.HasPrefix(tail, "ENGINE = ") {
		return OpsCreate{}, fmt.Errorf("%s: no ENGINE clause", out.Table)
	}
	out.Engine = stripComment(strings.TrimPrefix(tail, "ENGINE = "))
	return out, nil
}

// matchingParen returns the index of the ')' closing an already-opened '(',
// skipping quoted text.
func matchingParen(s string) int {
	depth := 1
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '\\':
			i++
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitTopLevel(s string) []string {
	var parts []string
	depth, start := 0, 0
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '\\':
			i++
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// stripComment removes a trailing COMMENT '...' clause.
func stripComment(s string) string {
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '\\':
			i++
		case c == '\'':
			quoted = !quoted
		case !quoted && strings.HasPrefix(s[i:], " COMMENT '"):
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}
