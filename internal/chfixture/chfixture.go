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
// NORMALIZE ONCE, READ EVERYWHERE (codex R5 review): four separate scans
// -- the old stripLineComments, clauseEnd's ')' check, splitTopLevelAnd's
// " AND " match, and the qualifier-phrase terminator match -- each had
// their OWN case/quote/whitespace blind spot, found and patched one at a
// time (R4's quote-unaware ON search, team-lead's quote-unaware
// depthProfile, R5's quote-unaware comment strip, case-sensitive AND
// split, and whitespace-sensitive "LEFT JOIN" phrase match). Patching
// scans individually does not converge: every scan that reads raw bytes
// is a fresh chance to reintroduce the same class of bug. So there is now
// exactly ONE normalization pass (normalize, below), and every step from
// here on -- JOIN/ON search, terminators, the ")" boundary, the AND
// split, the "=" split, the operand parser -- reads ONLY its output (the
// normalized text plus the quoted/depth masks computed from THAT text),
// never the raw statement and never its own ad-hoc case or whitespace
// handling.
//
// JOIN/ON DETECTION (codex R4 review): a bare "ON" search used to match
// ANYWHERE in the statement, including inside a string literal ("SELECT
// if(flag, 'ON', 'OFF') ..." registered a phantom clause) and with no
// JOIN anywhere nearby (a JOIN using USING(...) instead of ON has no
// condition at all, and must not silently grab a later, unrelated ON
// belonging to a different clause). Detection anchors on the word "JOIN"
// first; for each JOIN, it looks for the very next "ON" at that SAME
// paren depth, but only if no clause-terminator (another JOIN, WHERE,
// ...) or a closing paren appears first -- a JOIN with no ON contributes
// nothing to check.
//
// CONDITION BOUNDARY (team-lead's 2026-08-29 R1 finding): the condition
// used to end at the first newline, so a multi-line ON ("ON a = b\n  AND
// has(x, y)" or "...\n  OR c = d") was checked only on its first line --
// the violating second line was never seen, which is exactly the shape
// that made an earlier "0 violations" sweep partly an artifact of this
// bug rather than proof. The condition now runs from just after "ON" to
// the next top-level (same paren depth as the ON token) clause-boundary
// keyword -- see clauseTerminators -- or a closing paren that drops below
// that depth, or end of statement.
func JoinONViolations(statement string) (violations []string, clauses, conjuncts int) {
	normalized, quoted, depths := normalize(statement)

	pos := 0
	for {
		joinStart, ok := nextWord(normalized, quoted, pos, "JOIN")
		if !ok {
			break
		}
		depth := depths[joinStart]
		searchFrom := joinStart + len("JOIN")

		onPos := firstWordAtDepth(normalized, quoted, depths, searchFrom, depth, "ON")
		boundary := clauseEnd(normalized, quoted, depths, searchFrom, depth, clauseTerminators)
		if onPos < 0 || onPos >= boundary {
			// No ON before the next clause/JOIN/depth-drop -- e.g. a
			// USING(...) join, or a JOIN with an unconditional ON-less
			// target. Nothing to check for THIS join; keep scanning from
			// right after its own "JOIN" token.
			pos = searchFrom
			continue
		}

		conditionStart := onPos + len("ON")
		conditionEnd := clauseEnd(normalized, quoted, depths, conditionStart, depth, clauseTerminators)
		condition := strings.TrimSpace(normalized[conditionStart:conditionEnd])
		pos = conditionEnd
		if condition == "" {
			continue
		}
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

// normalize is the single preprocessing pass every other function in this
// file reads from -- see the doc comment on JoinONViolations above for
// why. It strips `--` line comments and collapses every run of
// whitespace to one space and uppercases every byte, ALL of it OUTSIDE
// single-quoted string literals; literal contents (case, internal
// whitespace, a `--` inside the literal) are copied through completely
// unchanged, both because their value never matters to this guard and
// because rewriting them would desync the quoted/depth masks computed
// from the SAME normalized text from what those bytes actually are.
// quoted and depths are then computed once, from the normalized text, and
// threaded through every downstream call -- nothing downstream calls
// quoteProfile or depthProfile again, or re-derives case/whitespace
// handling of its own.
func normalize(statement string) (normalized string, quoted []bool, depths []int) {
	var b strings.Builder
	b.Grow(len(statement))
	inString := false
	lastWasSpace := false
	for i := 0; i < len(statement); i++ {
		c := statement[i]
		if inString {
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(statement) && statement[i+1] == '\'' {
					b.WriteByte(statement[i+1])
					i++
					continue
				}
				inString = false
			}
			continue
		}
		switch {
		case c == '\'':
			inString = true
			b.WriteByte(c)
			lastWasSpace = false
		case c == '-' && i+1 < len(statement) && statement[i+1] == '-':
			// Line comment (quote-aware: only reached when NOT inString) --
			// skip to end of line or end of statement, then collapse the
			// whole comment (and the newline that ends it, if any) into the
			// same single space whitespace-collapsing already produces.
			for i < len(statement) && statement[i] != '\n' {
				i++
			}
			if !lastWasSpace {
				b.WriteByte(' ')
				lastWasSpace = true
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if !lastWasSpace {
				b.WriteByte(' ')
				lastWasSpace = true
			}
		default:
			if c >= 'a' && c <= 'z' {
				c -= 'a' - 'A'
			}
			b.WriteByte(c)
			lastWasSpace = false
		}
	}
	normalized = b.String()
	quoted = quoteProfile(normalized)
	depths = depthProfile(normalized, quoted)
	return normalized, quoted, depths
}

// clauseTerminators are the keywords that end a JOIN ON condition (and,
// reused, mark where a JOIN's search for its own ON gives up). Every
// join-type qualifier (INNER, LEFT, RIGHT, FULL, CROSS, ANY, ALL, ASOF,
// SEMI, ANTI, ARRAY) is listed as its two-word "... JOIN" phrase, never
// the bare qualifier alone (codex R4 review, generalizing team-lead's
// ANY/ALL/ARRAY finding to the rest: LEFT and RIGHT are ClickHouse string
// functions, left(s, n)/right(s, n), and a bare "LEFT"/"RIGHT" terminator
// truncates a condition at a legitimate left(...)/right(...) call in an
// operand -- INNER/FULL/CROSS/ASOF/SEMI/ANTI are lower-risk but treated
// the same way for consistency, not because a collision was found for
// each). Bare "JOIN" is still listed as the fallback for an unqualified
// join. "GROUP"/"ORDER" (not "GROUP BY"/"ORDER BY") are enough: "BY"
// always follows immediately and the boundary is the clause start, not
// the exact phrase. "UNION ALL" is still caught by the bare "UNION"
// entry (ALL alone is never searched for).
var clauseTerminators = []string{
	"WHERE", "PREWHERE", "GROUP", "ORDER", "HAVING", "LIMIT", "SETTINGS",
	"UNION", "WINDOW", "QUALIFY",
	"INNER JOIN", "LEFT JOIN", "RIGHT JOIN", "FULL JOIN", "CROSS JOIN",
	"ANY JOIN", "ALL JOIN", "ASOF JOIN", "SEMI JOIN", "ANTI JOIN", "ARRAY JOIN",
	"JOIN",
}

// clauseEnd finds where a span starting at `from` (at paren depth `depth`)
// ends: the earliest of the next word in `terminators` at that SAME
// depth, the position where depth first drops below `depth` (a closing
// paren belonging to an enclosing scope), or end of string. Used both to
// bound a found ON condition and, reused with the same terminator set, to
// find how far a JOIN's search for its own ON may look before giving up.
func clauseEnd(upper string, quoted []bool, depths []int, from, depth int, terminators []string) int {
	end := len(upper)
	// depths[i] is the depth IN EFFECT WHILE processing character i -- so
	// the ')' that closes us from `depth` down to `depth-1` is itself
	// still recorded at `depth` (the decrement lands in depths[i+1], not
	// depths[i]). The boundary is that paren's own position, EXCLUDED
	// from the condition -- end = i, not i+1, or a trailing ')' leaks
	// into the extracted text.
	for i := from; i < len(upper); i++ {
		// !quoted[i] matters even though depthProfile already ignores
		// parens inside literals when COUNTING depth: a ')' byte sitting
		// inside a literal never changes depth (it stays at whatever it
		// already was for the whole literal), so it can coincidentally
		// equal `depth` and get mistaken for the REAL closing paren of an
		// enclosing scope without this check (codex R5 review).
		if !quoted[i] && upper[i] == ')' && depths[i] == depth {
			end = i
			break
		}
	}
	for _, word := range terminators {
		if pos := firstWordAtDepth(upper, quoted, depths, from, depth, word); pos >= 0 && pos < end {
			end = pos
		}
	}
	return end
}

// depthProfile returns, for every byte offset in s (including one past
// the end), the paren nesting depth IN EFFECT at that offset -- i.e. the
// depth BEFORE that byte is processed, so depthProfile(s)[i] is the depth
// a keyword starting at i is found at.
func depthProfile(s string, quoted []bool) []int {
	depths := make([]int, len(s)+1)
	depth := 0
	for i := 0; i < len(s); i++ {
		depths[i] = depth
		if quoted[i] {
			// A '(' or ')' inside a string literal (e.g. a value like
			// "concat('(', b.k)") is not real nesting -- counting it
			// shifts every depth after it, which can make a JOIN and its
			// own ON no longer agree on depth and silently skip the
			// clause (team-lead's review, same failure class as R4's
			// quote-unaware word scans).
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
	}
	depths[len(s)] = depth
	return depths
}

// isWordChar reports whether b can appear inside a SQL identifier/keyword
// -- used to enforce word boundaries around a keyword match so "ON" does
// not match inside "CONCAT" or "action", and "AND" does not match inside
// "brand".
func isWordChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// quoteProfile returns, for every byte offset in s (including one past
// the end), whether that offset falls strictly inside a single-quoted SQL
// string literal (codex R4 review: a literal like 'ON' or a value
// containing "WHERE" must never be mistaken for query structure). A
// doubled quote (”) is treated as an escaped quote, matching
// operandParser.stringLiteral, not the closing delimiter.
func quoteProfile(s string) []bool {
	quoted := make([]bool, len(s)+1)
	inString := false
	for i := 0; i < len(s); i++ {
		quoted[i] = inString
		if s[i] != '\'' {
			continue
		}
		if inString && i+1 < len(s) && s[i+1] == '\'' {
			quoted[i+1] = true
			i++
			continue
		}
		inString = !inString
	}
	quoted[len(s)] = inString
	return quoted
}

// nextWord finds the next case-insensitive, word-bounded occurrence of
// word in upper (which must already be uppercased) at or after from,
// skipping any match that starts inside a string literal (quoted).
// Word-bounded rejects a substring match that is really part of a longer
// identifier on either side.
func nextWord(upper string, quoted []bool, from int, word string) (int, bool) {
	for i := from; i+len(word) <= len(upper); i++ {
		if quoted[i] {
			continue
		}
		if upper[i:i+len(word)] != word {
			continue
		}
		if i > 0 && isWordChar(upper[i-1]) {
			continue
		}
		if end := i + len(word); end < len(upper) && isWordChar(upper[end]) {
			continue
		}
		return i, true
	}
	return -1, false
}

// firstWordAtDepth is nextWord restricted to occurrences at exactly the
// given paren depth, re-searching past any depth mismatch (a keyword
// appearing deeper than `depth` -- inside a nested subquery expression --
// does not count as this clause's boundary).
func firstWordAtDepth(upper string, quoted []bool, depths []int, from, depth int, word string) int {
	pos := from
	for {
		idx, ok := nextWord(upper, quoted, pos, word)
		if !ok {
			return -1
		}
		if depths[idx] == depth {
			return idx
		}
		pos = idx + 1
	}
}

// splitTopLevelAnd splits condition (already whitespace-collapsed by
// JoinONViolations) on " AND " that is not nested inside parentheses, so
// a multi-argument function call's own internal " AND " -- there is none
// in the shapes this rule allows as a whole operand, but the split is
// written to stay correct regardless of what ends up inside parens -- is
// never mistaken for a conjunct boundary.
func splitTopLevelAnd(condition string) []string {
	const sep = " AND "
	var conjuncts []string
	depth := 0
	inString := false
	start := 0
	for i := 0; i < len(condition); i++ {
		switch {
		case condition[i] == '\'':
			if inString && i+1 < len(condition) && condition[i+1] == '\'' {
				i++
				continue
			}
			inString = !inString
			continue
		case inString:
			continue
		case condition[i] == '(':
			depth++
		case condition[i] == ')':
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
	parts := splitOutsideQuotes(conjunct, "=")
	if len(parts) != 2 {
		return false
	}
	return isPortableOperand(parts[0]) && isPortableOperand(parts[1])
}

// splitOutsideQuotes splits s on every occurrence of sep that is not
// inside a single-quoted SQL string literal (codex review finding): a
// permitted string-literal operand whose own content contains "=" --
// "ON a.id = 'foo=bar'" -- would otherwise split into three parts and be
// wrongly rejected, even though the grammar explicitly allows string
// literals. A doubled quote (”) is an escaped quote, not the closing
// delimiter, matching operandParser.stringLiteral.
func splitOutsideQuotes(s, sep string) []string {
	var parts []string
	inString := false
	start := 0
	for i := 0; i < len(s); {
		if s[i] == '\'' {
			if inString && i+1 < len(s) && s[i+1] == '\'' {
				i += 2
				continue
			}
			inString = !inString
			i++
			continue
		}
		if !inString && i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			parts = append(parts, s[start:i])
			i += len(sep)
			start = i
			continue
		}
		i++
	}
	return append(parts, s[start:])
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

// operand parses one base operand (literal, column, or function call), then
// any number of trailing "[operand]" array-subscript accesses -- the
// splitByString('#pr', evidence_ref)[1] shape dev-health-go's
// readers/investment_theme.go:135 already carries in a real ON clause,
// found by this guard's own R1 sweep and, like the literal/multi-arg-call
// operands above, already proven portable (it is on the current tip; the
// existing pin sites' integration tests exercise readTeamThemeMix's join
// against both 24.8 historically and 26.7.5.10 in this PR's own CI run).
func (p *operandParser) operand() bool {
	if !p.baseOperand() {
		return false
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.s) || p.s[p.pos] != '[' {
			return true
		}
		p.pos++
		if !p.operand() {
			return false
		}
		p.skipSpace()
		if p.pos >= len(p.s) || p.s[p.pos] != ']' {
			return false
		}
		p.pos++
	}
}

func (p *operandParser) baseOperand() bool {
	p.skipSpace()
	if p.pos >= len(p.s) {
		return false
	}
	switch c := p.s[p.pos]; {
	case c == '\'':
		return p.stringLiteral()
	case c == '{':
		return p.boundParameter()
	case c == '-' || (c >= '0' && c <= '9'):
		return p.numberLiteral()
	case isIdentStart(c):
		return p.identOrCall()
	default:
		return false
	}
}

// boundParameter consumes a ClickHouse named-parameter placeholder, e.g.
// "{org_id:String}" or "{as_of:Nullable(DateTime64(3, 'UTC'))}" (codex
// review finding: chaos4099_scope_expander.go's projectRepositoriesAsOf
// carries "r.org_id = {org_id:String}" in a real ON clause). This does not
// parse the type expression itself -- ClickHouse type syntax nests
// parens/commas/string literals arbitrarily deep (Nullable(...),
// Array(...), a quoted timezone) with no bound relevant to the portability
// question this guard asks -- it only finds the MATCHING closing brace by
// tracking "{"/"}" depth, which is sufficient because a placeholder's type
// expression is never itself brace-delimited.
func (p *operandParser) boundParameter() bool {
	if p.pos >= len(p.s) || p.s[p.pos] != '{' {
		return false
	}
	depth := 0
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				p.pos++
				return true
			}
		}
		p.pos++
	}
	return false // unterminated -- no matching "}"
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
