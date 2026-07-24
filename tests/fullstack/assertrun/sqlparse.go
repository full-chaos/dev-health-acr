package main

import (
	"fmt"
	"strings"
)

// This file has no knowledge of ClickHouse semantics beyond DDL/DML syntax shape: it is a
// small, generic SQL tokenizer (comment stripping, paren-depth/quote-aware splitting) shared
// by chSchema's migration replay and the seed file's INSERT parsing.

// stripSQLLineComments removes "-- ..." to end-of-line, but only outside single-quoted
// strings, so a literal containing "--" is never corrupted.
func stripSQLLineComments(sql string) string {
	var out strings.Builder
	inString := false
	runes := []rune(sql)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if inString {
			out.WriteRune(c)
			if c == '\'' {
				if i+1 < len(runes) && runes[i+1] == '\'' { // doubled-quote escape
					out.WriteRune(runes[i+1])
					i++
					continue
				}
				inString = false
			} else if c == '\\' && i+1 < len(runes) {
				out.WriteRune(runes[i+1])
				i++
			}
			continue
		}
		if c == '\'' {
			inString = true
			out.WriteRune(c)
			continue
		}
		if c == '-' && i+1 < len(runes) && runes[i+1] == '-' {
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			if i < len(runes) {
				out.WriteRune('\n')
			}
			continue
		}
		out.WriteRune(c)
	}
	return out.String()
}

// splitSQLStatements splits sql on ';' at paren-depth 0, outside single-quoted strings.
// Comments must already be stripped.
func splitSQLStatements(sql string) []string {
	return splitTopLevel(sql, ';', false)
}

// splitTopLevel splits s on sep at paren-depth 0, outside single-quoted strings. Empty
// trailing/leading fragments (as after a final ';') are dropped when dropEmpty is requested
// implicitly for statement splitting via the caller trimming results; here we always drop
// blank fragments so callers never see a purely-whitespace "statement". requireNonBlank
// controls whether zero-length (after trim) fragments are kept -- callers splitting a fixed
// arity list (e.g. a VALUES tuple) want requireNonBlank=false so an accidental empty element
// still counts toward arity instead of silently vanishing.
func splitTopLevel(s string, sep byte, requireNonBlank bool) []string {
	var out []string
	var current strings.Builder
	depth := 0
	inString := false
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if inString {
			current.WriteRune(c)
			if c == '\'' {
				if i+1 < len(runes) && runes[i+1] == '\'' {
					current.WriteRune(runes[i+1])
					i++
					continue
				}
				inString = false
			} else if c == '\\' && i+1 < len(runes) {
				current.WriteRune(runes[i+1])
				i++
			}
			continue
		}
		switch {
		case c == '\'':
			inString = true
			current.WriteRune(c)
		case c == '(' || c == '[':
			depth++
			current.WriteRune(c)
		case c == ')' || c == ']':
			depth--
			current.WriteRune(c)
		case byte(c) == sep && c < 128 && depth == 0:
			frag := current.String()
			if !requireNonBlank || strings.TrimSpace(frag) != "" {
				out = append(out, frag)
			}
			current.Reset()
		default:
			current.WriteRune(c)
		}
	}
	if frag := current.String(); !requireNonBlank || strings.TrimSpace(frag) != "" {
		out = append(out, frag)
	}
	return out
}

// findMatchingParen returns the index (into runes(s)) of the ')' matching the '(' at openIdx.
func findMatchingParen(s string, openIdx int) (int, error) {
	runes := []rune(s)
	if openIdx < 0 || openIdx >= len(runes) || runes[openIdx] != '(' {
		return -1, fmt.Errorf("index %d is not an opening paren", openIdx)
	}
	depth := 0
	inString := false
	for i := openIdx; i < len(runes); i++ {
		c := runes[i]
		if inString {
			if c == '\'' {
				if i+1 < len(runes) && runes[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			} else if c == '\\' {
				i++
			}
			continue
		}
		switch c {
		case '\'':
			inString = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return -1, fmt.Errorf("no matching closing paren for index %d", openIdx)
}

// firstIdentifier returns the leading identifier token of s (letters, digits, underscore,
// optionally backtick-quoted), or "" if s does not start with one.
func firstIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if s[0] == '`' {
		end := strings.IndexByte(s[1:], '`')
		if end < 0 {
			return ""
		}
		return s[1 : 1+end]
	}
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	return s[:i]
}

// normalizeTableName strips backticks and any "database." qualifier, since seed INSERT
// statements always use the bare table name.
func normalizeTableName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "`")
	if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
		name = name[idx+1:]
	}
	return strings.Trim(name, "`")
}
