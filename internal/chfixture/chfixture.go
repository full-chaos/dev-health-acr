// Package chfixture holds the single source of truth for the ClickHouse
// container image and version floor every acr testcontainers-backed
// integration fixture uses.
//
// CHAOS-4549 (chris, RULED 2026-08-29 "26"): acr's fixtures pinned
// clickhouse/clickhouse-server:24.8 while prod runs 26.7.5.10 with the new
// analyzer ON, on a floating tag (CHAOS-4519 -- prod's exact patch drifts,
// so "prod's version" is a moving target). Testing two majors behind what
// ships is not cosmetic: it changes what SQL is accepted at all. CHAOS-4521b
// shipped a multi-arm `JOIN ... ON (... OR ...)` that is valid on 26.7
// (prod, kiac) and rejected on 24.8 under every analyzer setting
// (`Code: 403 Unsupported JOIN ON conditions`) -- caught only because acr's
// pinned fixtures ran it, after four review rounds of "executed against a
// real ClickHouse" proofs that had all run against 26.7.
//
// Raising the pin (Option A) removes the only CI gate that rejected that
// shape. The portability rule survives the raise as a STANDING RULE --
// every JOIN ON clause is a plain column equality; alternatives are
// UNION ALL arms, never OR -- enforced statically now (see
// TestChaos4549AllJoinOnClausesArePortable in devhealthfacts and
// devhealthsource) instead of by an old-engine fixture that merely happened
// to reject the shape as a side effect.
//
// One constant, every fixture reads it -- do not duplicate the image tag or
// the version floor at a call site.
//
// This package also holds the STATIC replacement for the portability gate
// the pin raise removes -- see JoinONViolations below.
package chfixture

import (
	"strconv"
	"strings"
)

// Image is the ClickHouse server container image every acr testcontainers
// fixture starts. Pinned to the exact patch prod ran when this ticket was
// ruled (26.7.5.10) rather than a floating tag, so a CI run is reproducible;
// bump it by hand as prod's floor advances -- see VersionFloor.
const Image = "clickhouse/clickhouse-server:26.7.5.10"

// VersionFloor is the minimum accepted "major.minor" reported by
// `SELECT version()`. Prod floats on a `latest`-style tag (CHAOS-4519), so
// the fixture asserts the server is AT LEAST this floor rather than an
// exact prefix match: prod can advance patch or minor without breaking CI,
// but CI must never silently sit BEHIND what is actually deployed.
const VersionFloor = "26.7"

// AtLeastVersionFloor reports whether serverVersion -- ClickHouse's
// `SELECT version()` string, e.g. "26.7.5.10" -- is at or above
// VersionFloor by numeric (major, minor) comparison. A string prefix check
// would wrongly reject "26.10.1.1" as "less than" "26.7.x" (lexical "10" <
// "7"), so this parses both components as integers.
func AtLeastVersionFloor(serverVersion string) bool {
	major, minor, ok := majorMinor(serverVersion)
	if !ok {
		return false
	}
	floorMajor, floorMinor, ok := majorMinor(VersionFloor)
	if !ok {
		// VersionFloor is a package constant, not user input; a malformed
		// floor is a programming error, not a runtime condition to hide.
		panic("chfixture: VersionFloor is not a valid major.minor version: " + VersionFloor)
	}
	if major != floorMajor {
		return major > floorMajor
	}
	return minor >= floorMinor
}

// majorMinor parses the leading two dot-separated integer components of a
// ClickHouse version string. It ignores any trailing components (patch,
// build) -- the floor is deliberately major.minor only, per CHAOS-4549's
// ruling.
func majorMinor(version string) (major, minor int, ok bool) {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// JoinONViolations is CHAOS-4549's static replacement for the portability
// gate that raising the fixture pin removes.
//
// RULE (structural, not a denylist -- team-lead's 2026-08-29 correction:
// a denylist of known-bad shapes like " OR " or "has(" drifts the moment a
// new non-portable shape ships that nobody thought to add). Every
// JOIN ... ON clause must be a top-level AND-conjunction of conjuncts, and
// every conjunct must be EXACTLY "<operand> = <operand>", where an operand
// is a qualified column, a numeric or string literal, or a function call of
// any arity whose every argument is itself a portable operand
// (recursively) -- so "m.is_active = 1", "toString(e.org_id) = a.b", and
// "ifNull(a.team_id, empty-string) = t.id" are all portable. This is deliberately
// PERMISSIVE about what an operand may be: literals and multi-argument/
// nested function calls are exactly the shapes already proven portable to
// the pre-26 analyzer by the SQL already on main (team-lead's 2026-08-29
// scope correction -- an earlier, stricter draft of this rule rejected
// several already-shipped, already-CI-proven ON clauses and would have
// forced unrelated production rewrites into a CI-pin PR). What actually
// fails is the SHAPE of the whole clause, not any operand within it: OR
// combining conditions, any comparator other than a single "=" per
// conjunct (<>, <, >, IN, NOT), and a bare boolean-function conjunct with
// no "=" at all (a lone "has(...)" predicate, not "has(...) = 1") --
// exactly the class CHAOS-4521b's OR-arm join and dev-health-go's own
// has(...)-in-ON incident both belong to.
//
// statement is the full, already-rendered SQL text (real bind-time
// interpolation already applied by the caller, not a template); this
// function only reads it. It returns every conjunct that fails the rule,
// plus how many ON clauses and conjuncts it examined, so a caller can log
// real coverage instead of a guard that silently checked nothing.
//
// SQL line comments (`-- ...`) are stripped before the ON/AND split: this
// codebase's producers carry many of them, and prose containing the plain
// word " ON " or " AND " (both ordinary English words, not just SQL
// keywords) would otherwise register as a phantom clause boundary.
func JoinONViolations(statement string) (violations []string, clauses, conjuncts int) {
	statement = stripLineComments(statement)
	for _, onClause := range strings.Split(statement, " ON ")[1:] {
		condition := strings.SplitN(onClause, "\n", 2)[0]
		clauses++
		for _, conjunct := range splitTopLevelAnd(condition) {
			conjuncts++
			if !isPortableConjunct(conjunct) {
				violations = append(violations, strings.TrimSpace(conjunct))
			}
		}
	}
	return violations, clauses, conjuncts
}

// stripLineComments removes every `-- ...` SQL line comment (to end of
// line), so their prose can never be mistaken for query structure.
func stripLineComments(statement string) string {
	lines := strings.Split(statement, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// splitTopLevelAnd splits condition on " AND " that is not nested inside
// parentheses, so a (hypothetical) multi-argument function call's own
// internal " AND " -- there is none in the single-argument shape this rule
// allows, but the split is written to stay correct regardless of what
// ends up inside parens -- is never mistaken for a conjunct boundary.
func splitTopLevelAnd(condition string) []string {
	const sep = " AND "
	var conjuncts []string
	depth := 0
	start := 0
	for i := 0; i < len(condition); i++ {
		switch condition[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && i+len(sep) <= len(condition) && condition[i:i+len(sep)] == sep {
			conjuncts = append(conjuncts, condition[start:i])
			i += len(sep) - 1
			start = i + 1
		}
	}
	conjuncts = append(conjuncts, condition[start:])
	return conjuncts
}

// isPortableConjunct reports whether conjunct is exactly
// "<operand> = <operand>" under the JoinONViolations rule. Splitting on
// "=" and requiring exactly two parts already rejects OR/IN/NOT/<>/other
// comparisons, a bare boolean-function conjunct (no "=" at all), and
// multi-way chains without naming any of them: none of those shapes
// produce exactly one "=" with two operands around it. "<=" and ">="
// still contain one "=" character, but the stray "<"/">" left dangling on
// one side then fails isPortableOperand's requirement that the operand
// parse in full -- see its comment.
func isPortableConjunct(conjunct string) bool {
	parts := strings.Split(conjunct, "=")
	if len(parts) != 2 {
		return false
	}
	return isPortableOperand(parts[0]) && isPortableOperand(parts[1])
}

// isPortableOperand reports whether operand parses IN FULL as the operand
// grammar: a qualified or bare column, a numeric or string literal, or a
// function call of any arity whose every argument is itself a portable
// operand. "In full" is what rejects a stray leftover character (a
// dangling "<" from "<=", trailing prose, an unbalanced paren) -- a
// partial parse that stops short of consuming the whole string is a
// non-match, not a portable operand with extra text after it.
func isPortableOperand(operand string) bool {
	p := &operandParser{s: strings.TrimSpace(operand)}
	return p.operand() && p.pos == len(p.s)
}

// operandParser is a minimal recursive-descent parser for the CHAOS-4549
// operand grammar (column | literal | func(operand, ...)). It has no
// notion of AND/OR/comparison operators at all -- boolean logic simply
// cannot appear inside an operand, so "nested boolean logic" fails to
// parse rather than needing its own rejection rule.
type operandParser struct {
	s   string
	pos int
}

func (p *operandParser) skipSpace() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
}

func (p *operandParser) operand() bool {
	p.skipSpace()
	if p.pos >= len(p.s) {
		return false
	}
	switch c := p.s[p.pos]; {
	case c == '\'':
		return p.stringLiteral()
	case c == '-' || (c >= '0' && c <= '9'):
		return p.numberLiteral()
	case isIdentStart(c):
		return p.identOrCall()
	default:
		return false
	}
}

// identOrCall consumes an identifier, then either a ".<identifier>"
// qualifier (a column reference) or a "(<operand>, ...)" argument list (a
// function call of any arity) if one immediately follows -- otherwise the
// bare identifier itself is the operand.
func (p *operandParser) identOrCall() bool {
	if !p.ident() {
		return false
	}
	if p.pos < len(p.s) && p.s[p.pos] == '.' {
		p.pos++
		return p.ident()
	}
	p.skipSpace()
	if p.pos >= len(p.s) || p.s[p.pos] != '(' {
		return true
	}
	p.pos++
	p.skipSpace()
	if p.pos < len(p.s) && p.s[p.pos] == ')' {
		p.pos++
		return true
	}
	for {
		if !p.operand() {
			return false
		}
		p.skipSpace()
		if p.pos < len(p.s) && p.s[p.pos] == ',' {
			p.pos++
			continue
		}
		break
	}
	p.skipSpace()
	if p.pos < len(p.s) && p.s[p.pos] == ')' {
		p.pos++
		return true
	}
	return false
}

func (p *operandParser) ident() bool {
	start := p.pos
	if p.pos >= len(p.s) || !isIdentStart(p.s[p.pos]) {
		return false
	}
	p.pos++
	for p.pos < len(p.s) && isIdentByte(p.s[p.pos]) {
		p.pos++
	}
	return p.pos > start
}

func (p *operandParser) numberLiteral() bool {
	start := p.pos
	if p.pos < len(p.s) && p.s[p.pos] == '-' {
		p.pos++
	}
	digits := 0
	for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
		p.pos++
		digits++
	}
	if p.pos < len(p.s) && p.s[p.pos] == '.' {
		p.pos++
		for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
			p.pos++
			digits++
		}
	}
	if digits == 0 {
		p.pos = start
		return false
	}
	return true
}

// stringLiteral consumes a SQL string literal delimited by single quotes,
// treating a doubled quote mark as an escaped quote rather than the
// closing delimiter.
func (p *operandParser) stringLiteral() bool {
	if p.pos >= len(p.s) || p.s[p.pos] != '\'' {
		return false
	}
	p.pos++
	for p.pos < len(p.s) {
		if p.s[p.pos] == '\'' {
			if p.pos+1 < len(p.s) && p.s[p.pos+1] == '\'' {
				p.pos += 2
				continue
			}
			p.pos++
			return true
		}
		p.pos++
	}
	return false
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentByte(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
