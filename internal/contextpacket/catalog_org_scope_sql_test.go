package contextpacket_test

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

// A small, scope-aware reader of the ClickHouse SELECT statements of the
// source catalog (CHAOS-7244). It is a test helper, not a SQL parser: it
// understands exactly what the org-scope sweep needs and REFUSES (returns an
// error) what it does not understand, so a statement shape it cannot read
// fails the sweep instead of passing it.
//
// What it decides, per query scope (one SELECT block; a subquery, a derived
// table, a CTE body and each side of a UNION is its own scope):
//
//   - the base tables the scope reads (FROM, comma joins and JOIN);
//   - which of them are bound to the caller's organization by a TOP-LEVEL
//     AND conjunct of that scope's WHERE/PREWHERE (or of the ON of a join
//     that keeps the rows of the bound side): `[alias.]org_id =
//     {org_id:String}`, or an equality of two org_id columns that reaches a
//     bound table. A conjunct hidden under OR, NOT, a function, a CASE, the
//     select list, a string or a comment is not a binding;
//   - aliases per scope: an inner scope's `p` never inherits the binding of
//     an outer `p`, and an inner binding never scopes an outer table.

type sqlTokKind int

const (
	sqlIdent sqlTokKind = iota
	sqlNumber
	sqlString
	sqlParam // {name:Type}
	sqlPunct
)

type sqlTok struct {
	kind   sqlTokKind
	text   string
	quoted bool // a quoted identifier is never a keyword
}

func tokenizeSQL(s string) ([]sqlTok, error) {
	var toks []sqlTok
	rs := []rune(s)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case r == '/' && i+1 < len(rs) && rs[i+1] == '*':
			j := i + 2
			for j+1 < len(rs) && !(rs[j] == '*' && rs[j+1] == '/') {
				j++
			}
			if j+1 >= len(rs) {
				return nil, fmt.Errorf("unterminated block comment")
			}
			i = j + 2
		case r == '\'':
			j := i + 1
			for {
				if j >= len(rs) {
					return nil, fmt.Errorf("unterminated string literal")
				}
				if rs[j] == '\\' {
					j += 2
					continue
				}
				if rs[j] == '\'' {
					if j+1 < len(rs) && rs[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			toks = append(toks, sqlTok{kind: sqlString, text: string(rs[i : j+1])})
			i = j + 1
		case r == '`' || r == '"':
			j := i + 1
			for j < len(rs) && rs[j] != r {
				j++
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("unterminated quoted identifier")
			}
			toks = append(toks, sqlTok{kind: sqlIdent, text: string(rs[i+1 : j]), quoted: true})
			i = j + 1
		case r == '{':
			j := i + 1
			inString := false
			for j < len(rs) && (inString || rs[j] != '}') {
				if rs[j] == '\'' {
					inString = !inString
				}
				j++
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("unterminated {parameter}")
			}
			toks = append(toks, sqlTok{kind: sqlParam, text: string(rs[i+1 : j])})
			i = j + 1
		case unicode.IsDigit(r):
			j := i
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.' || rs[j] == '_') {
				j++
			}
			if j < len(rs) && (rs[j] == 'e' || rs[j] == 'E') {
				j++
				if j < len(rs) && (rs[j] == '+' || rs[j] == '-') {
					j++
				}
				for j < len(rs) && unicode.IsDigit(rs[j]) {
					j++
				}
			}
			toks = append(toks, sqlTok{kind: sqlNumber, text: string(rs[i:j])})
			i = j
		case unicode.IsLetter(r) || r == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_' || rs[j] == '$') {
				j++
			}
			toks = append(toks, sqlTok{kind: sqlIdent, text: string(rs[i:j])})
			i = j
		default:
			if i+1 < len(rs) {
				switch two := string(rs[i : i+2]); two {
				case "<=", ">=", "!=", "<>", "==", "||", "->", "::", "=>":
					toks = append(toks, sqlTok{kind: sqlPunct, text: two})
					i += 2
					continue
				}
			}
			if strings.ContainsRune("()[],.;=<>+-*/%?:|&^~@", r) {
				toks = append(toks, sqlTok{kind: sqlPunct, text: string(r)})
				i++
				continue
			}
			return nil, fmt.Errorf("unexpected character %q", r)
		}
	}
	return toks, nil
}

// sqlNode is a token or a bracket group ( ) / [ ].
type sqlNode struct {
	tok      sqlTok
	group    bool
	children []sqlNode
}

func groupSQL(toks []sqlTok) ([]sqlNode, error) {
	stack := [][]sqlNode{nil}
	opens := []string{""}
	for _, t := range toks {
		if t.kind == sqlPunct {
			switch t.text {
			case "(", "[":
				stack = append(stack, nil)
				opens = append(opens, t.text)
				continue
			case ")", "]":
				if len(stack) == 1 || (t.text == ")") != (opens[len(opens)-1] == "(") {
					return nil, fmt.Errorf("unbalanced %q", t.text)
				}
				inner := stack[len(stack)-1]
				stack, opens = stack[:len(stack)-1], opens[:len(opens)-1]
				stack[len(stack)-1] = append(stack[len(stack)-1], sqlNode{group: true, children: inner})
				continue
			}
		}
		stack[len(stack)-1] = append(stack[len(stack)-1], sqlNode{tok: t})
	}
	if len(stack) != 1 {
		return nil, fmt.Errorf("unclosed %q", opens[len(opens)-1])
	}
	return stack[0], nil
}

func (n sqlNode) isKw(words ...string) bool {
	if n.group || n.tok.kind != sqlIdent || n.tok.quoted {
		return false
	}
	for _, w := range words {
		if strings.EqualFold(n.tok.text, w) {
			return true
		}
	}
	return false
}

func (n sqlNode) isPunct(text string) bool {
	return !n.group && n.tok.kind == sqlPunct && n.tok.text == text
}

func (n sqlNode) isIdent() bool { return !n.group && n.tok.kind == sqlIdent }

// isSubquery: a group that holds a query: SELECT ..., ((SELECT ...)) or
// ((SELECT ...) UNION ALL (SELECT ...)). A group such as ((SELECT 1) + 1) is
// an expression; the subquery inside it is found when the sweep descends.
func (n sqlNode) isSubquery() bool {
	if !n.group || len(n.children) == 0 {
		return false
	}
	if n.children[0].isKw("SELECT", "WITH") {
		return true
	}
	if !n.children[0].isSubquery() {
		return false
	}
	return len(n.children) == 1 || slices.ContainsFunc(n.children, func(c sqlNode) bool { return c.isKw(sqlSetOperators...) })
}

var (
	sqlSetOperators = []string{"UNION", "EXCEPT", "INTERSECT"}
	sqlTerminators  = []string{"WHERE", "PREWHERE", "GROUP", "ORDER", "HAVING", "LIMIT", "OFFSET", "SETTINGS", "FORMAT", "WINDOW", "QUALIFY", "UNION", "EXCEPT", "INTERSECT"}
	sqlJoinWords    = []string{"INNER", "LEFT", "RIGHT", "FULL", "CROSS", "OUTER", "ANY", "ALL", "ASOF", "SEMI", "ANTI", "GLOBAL", "LOCAL", "PASTE", "ARRAY", "NATURAL", "JOIN"}
	sqlAfterTable   = append(append([]string{"FINAL", "SAMPLE", "ON", "USING", "FROM"}, sqlTerminators...), sqlJoinWords...)
)

func isTerminator(n sqlNode) bool { return n.isKw(sqlTerminators...) }

// isJoinStart: modifiers followed by JOIN, or a plain JOIN. A function call
// such as left(x, 3) is not one: its name is followed by a group.
func isJoinStart(items []sqlNode, i int) bool {
	j := i
	for j < len(items) && items[j].isKw(sqlJoinWords...) && !items[j].isKw("JOIN") {
		j++
	}
	return j < len(items) && items[j].isKw("JOIN")
}

type sqlJoinKind int

const (
	joinInner sqlJoinKind = iota // ON rows are filtered on both sides
	joinLeft                     // the joined (right) table is filtered by ON
	joinOther                    // RIGHT / FULL: ON filters nothing that is kept
)

type sqlRef struct {
	name string // base table name (last path part); "" for a derived table
	qual string // the name the statement qualifies its columns with
	base bool
}

type sqlScope struct {
	parent      *sqlScope
	refs        []*sqlRef
	ctes        map[string]bool
	constrained map[*sqlRef]bool
}

func (sc *sqlScope) find(qual string) (*sqlRef, *sqlScope) {
	for s := sc; s != nil; s = s.parent {
		for _, r := range s.refs {
			if r.qual == qual {
				return r, s
			}
		}
	}
	return nil, nil
}

func (sc *sqlScope) hasCTE(name string) bool {
	for s := sc; s != nil; s = s.parent {
		if s.ctes[name] {
			return true
		}
	}
	return false
}

// orgScopeReport is what one statement reads and where it leaks.
type orgScopeReport struct {
	// Tables lists every base table read, in reading order, by name.
	Tables []string
	// Violations lists every base table not scoped to the organization, as
	// `table <name> (alias "<alias>")`.
	Violations []string
}

// analyzeOrgScope reads one statement. It errors on a shape it cannot read
// and when the statement reads no table at all.
func analyzeOrgScope(statement string) (orgScopeReport, error) {
	toks, err := tokenizeSQL(statement)
	if err != nil {
		return orgScopeReport{}, err
	}
	items, err := groupSQL(toks)
	if err != nil {
		return orgScopeReport{}, err
	}
	var report orgScopeReport
	if err := analyzeQuery(items, nil, &report); err != nil {
		return orgScopeReport{}, err
	}
	if len(report.Tables) == 0 {
		return orgScopeReport{}, fmt.Errorf("no table found: the sweep matched nothing")
	}
	return report, nil
}

// analyzeQuery splits a query at its set operators; each side is a scope.
func analyzeQuery(items []sqlNode, parent *sqlScope, report *orgScopeReport) error {
	start := 0
	for i := 0; i < len(items); i++ {
		if !items[i].isKw(sqlSetOperators...) {
			continue
		}
		if err := analyzeBlock(items[start:i], parent, report); err != nil {
			return err
		}
		i++ // UNION [ALL | DISTINCT]
		for i < len(items) && items[i].isKw("ALL", "DISTINCT") {
			i++
		}
		start = i
		i-- // the loop increments
	}
	return analyzeBlock(items[start:], parent, report)
}

// sqlJoin is one JOIN of a block with its ON expression.
type sqlJoin struct {
	kind sqlJoinKind
	ref  *sqlRef
	on   []sqlNode
}

// sqlEquality is a.org_id = b.org_id: from -> to, and back when both.
type sqlEquality struct {
	from, to *sqlRef
	both     bool
}

func analyzeBlock(items []sqlNode, parent *sqlScope, report *orgScopeReport) error {
	if len(items) == 0 {
		return fmt.Errorf("empty query")
	}
	// A parenthesised query side: (SELECT ...) UNION ALL (SELECT ...).
	if len(items) == 1 && items[0].group {
		return analyzeQuery(items[0].children, parent, report)
	}
	sc := &sqlScope{parent: parent, ctes: map[string]bool{}, constrained: map[*sqlRef]bool{}}
	i := 0
	if items[0].isKw("WITH") {
		j := 1
		for j < len(items) && !items[j].isKw("SELECT") {
			if items[j].isIdent() && j+2 < len(items) && items[j+1].isKw("AS") && items[j+2].isSubquery() {
				sc.ctes[items[j].tok.text] = true
			}
			j++
		}
		if j == len(items) {
			return fmt.Errorf("WITH without SELECT")
		}
		i = j
	}
	if !items[i].isKw("SELECT") {
		return fmt.Errorf("a query block does not start with SELECT")
	}
	i++
	for i < len(items) && !items[i].isKw("FROM") && !isTerminator(items[i]) {
		i++
	}
	var joins []sqlJoin
	if i < len(items) && items[i].isKw("FROM") {
		var err error
		if joins, i, err = parseFrom(items, i+1, sc); err != nil {
			return err
		}
	}
	var wheres [][]sqlNode
	for i < len(items) {
		if items[i].isKw("WHERE", "PREWHERE") {
			end := i + 1
			for end < len(items) && !isTerminator(items[end]) {
				end++
			}
			wheres = append(wheres, items[i+1:end])
			i = end
			continue
		}
		i++
	}

	var equalities []sqlEquality
	// resolve maps a qualifier to a ref of THIS scope or of an enclosing one;
	// an unqualified org_id is only unambiguous in a scope with one table.
	resolve := func(qual string) (*sqlRef, *sqlScope) {
		if qual == "" {
			if len(sc.refs) == 1 {
				return sc.refs[0], sc
			}
			return nil, nil
		}
		return sc.find(qual)
	}
	// collect reads the conjuncts of one WHERE or ON. allowed says which of
	// this scope's tables the clause may bind; link builds the equality edge
	// (or refuses it) for two of this scope's tables.
	collect := func(expr []sqlNode, allowed func(*sqlRef) bool, link func(a, b *sqlRef) (sqlEquality, bool)) {
		for _, conj := range conjuncts(expr) {
			if qual, ok := matchOrgBinding(conj); ok {
				if r, owner := resolve(qual); r != nil && owner == sc && allowed(r) {
					sc.constrained[r] = true
				}
				continue
			}
			qa, qb, ok := matchOrgEquality(conj)
			if !ok {
				continue
			}
			ra, oa := resolve(qa)
			rb, ob := resolve(qb)
			switch {
			case ra == nil || rb == nil:
			case oa == sc && ob == sc:
				if e, ok := link(ra, rb); ok {
					equalities = append(equalities, e)
				}
			// A correlated equality with an enclosing table that is bound
			// there binds this scope's table.
			case oa == sc && ob.constrained[rb] && allowed(ra):
				sc.constrained[ra] = true
			case ob == sc && oa.constrained[ra] && allowed(rb):
				sc.constrained[rb] = true
			}
		}
	}
	everything := func(*sqlRef) bool { return true }
	bothWays := func(a, b *sqlRef) (sqlEquality, bool) { return sqlEquality{from: a, to: b, both: true}, true }
	for _, w := range wheres {
		collect(w, everything, bothWays)
	}
	for _, j := range joins {
		switch j.kind {
		case joinInner:
			collect(j.on, everything, bothWays)
		case joinLeft:
			// Left rows survive the ON: it filters the joined table only.
			collect(j.on, func(r *sqlRef) bool { return r == j.ref }, func(a, b *sqlRef) (sqlEquality, bool) {
				switch {
				case b == j.ref && a != j.ref:
					return sqlEquality{from: a, to: b}, true
				case a == j.ref && b != j.ref:
					return sqlEquality{from: b, to: a}, true
				}
				return sqlEquality{}, false
			})
		}
		// joinOther (RIGHT, FULL): the ON filters no row that is kept.
	}
	for changed := true; changed; {
		changed = false
		for _, e := range equalities {
			if sc.constrained[e.from] && !sc.constrained[e.to] {
				sc.constrained[e.to], changed = true, true
			}
			if e.both && sc.constrained[e.to] && !sc.constrained[e.from] {
				sc.constrained[e.from], changed = true, true
			}
		}
	}

	for _, r := range sc.refs {
		if !r.base {
			continue
		}
		report.Tables = append(report.Tables, r.name)
		if !tableNeedsOrgScope(r.name) || sc.constrained[r] {
			continue
		}
		alias := ""
		if r.qual != r.name {
			alias = r.qual
		}
		report.Violations = append(report.Violations, "table "+r.name+" (alias \""+alias+"\")")
	}
	return findSubqueries(items, sc, report)
}

// parseFrom reads the table expressions of a FROM clause, its comma joins and
// its JOINs, from i. It returns the joins and the index after the clause.
func parseFrom(items []sqlNode, i int, sc *sqlScope) ([]sqlJoin, int, error) {
	var joins []sqlJoin
	read := func(at int) (*sqlRef, int, error) {
		ref, next, err := parseTableRef(items, at, sc)
		if err != nil {
			return nil, at, err
		}
		return ref, next, sc.add(ref)
	}
	_, i, err := read(i)
	if err != nil {
		return nil, i, err
	}
	for i < len(items) {
		switch {
		case items[i].isPunct(","):
			if _, i, err = read(i + 1); err != nil {
				return nil, i, err
			}
		case isJoinStart(items, i):
			kind := joinInner
			for ; !items[i].isKw("JOIN"); i++ {
				switch {
				case items[i].isKw("LEFT"):
					kind = joinLeft
				case items[i].isKw("RIGHT", "FULL"):
					kind = joinOther
				case items[i].isKw("ARRAY"):
					return nil, i, fmt.Errorf("ARRAY JOIN is not readable by the org-scope sweep")
				}
			}
			ref, next, err := read(i + 1)
			if err != nil {
				return nil, i, err
			}
			i = next
			join := sqlJoin{kind: kind, ref: ref}
			switch {
			case i < len(items) && items[i].isKw("ON"):
				end := i + 1
				for end < len(items) && !isJoinStart(items, end) && !isTerminator(items[end]) && !items[end].isPunct(",") {
					end++
				}
				join.on = items[i+1 : end]
				i = end
			case i < len(items) && items[i].isKw("USING"):
				return nil, i, fmt.Errorf("JOIN ... USING is not readable by the org-scope sweep: write the org_id equality in ON")
			}
			joins = append(joins, join)
		default:
			return joins, i, nil
		}
	}
	return joins, i, nil
}

func (sc *sqlScope) add(r *sqlRef) error {
	for _, other := range sc.refs {
		if other.qual == r.qual {
			return fmt.Errorf("the name %q is used twice in one scope", r.qual)
		}
	}
	sc.refs = append(sc.refs, r)
	return nil
}

// findSubqueries analyses every subquery group below items, at any depth, as
// its own scope whose parent is sc.
func findSubqueries(items []sqlNode, sc *sqlScope, report *orgScopeReport) error {
	for _, n := range items {
		if !n.group {
			continue
		}
		if n.isSubquery() {
			if err := analyzeQuery(n.children, sc, report); err != nil {
				return err
			}
			continue
		}
		if err := findSubqueries(n.children, sc, report); err != nil {
			return err
		}
	}
	return nil
}

// parseTableRef reads one table expression of FROM or JOIN starting at i.
func parseTableRef(items []sqlNode, i int, sc *sqlScope) (*sqlRef, int, error) {
	if i >= len(items) {
		return nil, i, fmt.Errorf("a table expression is missing")
	}
	ref := &sqlRef{}
	switch {
	case items[i].group:
		if !items[i].isSubquery() {
			return nil, i, fmt.Errorf("a parenthesised table expression that is not a subquery")
		}
		i++
	case items[i].isIdent():
		path := []string{items[i].tok.text}
		i++
		for i+1 < len(items) && items[i].isPunct(".") && items[i+1].isIdent() {
			path = append(path, items[i+1].tok.text)
			i += 2
		}
		if i < len(items) && items[i].group {
			return nil, i, fmt.Errorf("table function %s(...) is not readable by the org-scope sweep", strings.Join(path, "."))
		}
		ref.name = path[len(path)-1]
		ref.qual = ref.name
		ref.base = !(len(path) == 1 && sc.hasCTE(path[0]))
	default:
		return nil, i, fmt.Errorf("unreadable table expression")
	}
	// [AS] alias, then FINAL and SAMPLE in any order.
	if i < len(items) && items[i].isKw("AS") {
		if i+1 >= len(items) || !items[i+1].isIdent() {
			return nil, i, fmt.Errorf("AS without an alias")
		}
		ref.qual = items[i+1].tok.text
		i += 2
	} else if i < len(items) && items[i].isIdent() && !items[i].isKw(sqlAfterTable...) {
		ref.qual = items[i].tok.text
		i++
	}
	if ref.qual == "" { // a derived table may go without an alias
		ref.qual = fmt.Sprintf("(derived table %d)", len(sc.refs))
	}
	for i < len(items) {
		switch {
		case items[i].isKw("FINAL"):
			i++
		case items[i].isKw("SAMPLE"):
			i++
			for i < len(items) && !items[i].group && (items[i].tok.kind == sqlNumber || items[i].isPunct("/") || items[i].isKw("OFFSET")) {
				i++
			}
		default:
			return ref, i, nil
		}
	}
	return ref, i, nil
}

// conjuncts returns the top-level AND conjuncts of expr. An expression that
// has an OR at its top level is a disjunction: no conjunct of it is
// guaranteed, so it has none. AND inside BETWEEN or CASE is not a separator.
func conjuncts(expr []sqlNode) [][]sqlNode {
	var out [][]sqlNode
	start, between, cases := 0, false, 0
	for i := 0; i <= len(expr); i++ {
		if i < len(expr) {
			n := expr[i]
			switch {
			case n.isKw("CASE"):
				cases++
			case n.isKw("END") && cases > 0:
				cases--
			case cases > 0:
			case n.isKw("OR"):
				return nil
			case n.isKw("BETWEEN"):
				between = true
			case n.isKw("AND") && between:
				between = false
			case n.isKw("AND"):
				out = append(out, expr[start:i])
				start = i + 1
			}
			continue
		}
		out = append(out, expr[start:])
	}
	var flat [][]sqlNode
	for _, c := range out {
		if len(c) == 1 && c[0].group && !c[0].isSubquery() {
			inner := conjuncts(c[0].children)
			flat = append(flat, inner...)
			continue
		}
		flat = append(flat, c)
	}
	return flat
}

// orgColumn reads `[qual.]org_id` or toString([qual.]org_id).
func orgColumn(items []sqlNode) (string, bool) {
	if len(items) == 2 && items[0].isKw("toString") && items[1].group {
		return orgColumn(items[1].children)
	}
	switch {
	case len(items) == 1 && items[0].isIdent() && items[0].tok.text == "org_id":
		return "", true
	case len(items) == 3 && items[0].isIdent() && items[1].isPunct(".") && items[2].isIdent() && items[2].tok.text == "org_id":
		return items[0].tok.text, true
	}
	return "", false
}

func isOrgParam(items []sqlNode) bool {
	if len(items) != 1 || items[0].group || items[0].tok.kind != sqlParam {
		return false
	}
	name, typ, _ := strings.Cut(items[0].tok.text, ":")
	return strings.TrimSpace(name) == "org_id" && strings.TrimSpace(typ) == "String"
}

// splitEquals splits a conjunct at its single top-level `=`.
func splitEquals(conj []sqlNode) (left, right []sqlNode, ok bool) {
	at := -1
	for i, n := range conj {
		if n.isPunct("=") || n.isPunct("==") {
			if at >= 0 {
				return nil, nil, false
			}
			at = i
		}
	}
	if at <= 0 || at == len(conj)-1 {
		return nil, nil, false
	}
	return conj[:at], conj[at+1:], true
}

// matchOrgBinding: the whole conjunct is `[qual.]org_id = {org_id:String}`.
func matchOrgBinding(conj []sqlNode) (string, bool) {
	left, right, ok := splitEquals(conj)
	if !ok {
		return "", false
	}
	if qual, ok := orgColumn(left); ok && isOrgParam(right) {
		return qual, true
	}
	if qual, ok := orgColumn(right); ok && isOrgParam(left) {
		return qual, true
	}
	return "", false
}

// matchOrgEquality: the whole conjunct is `a.org_id = b.org_id`, both qualified.
func matchOrgEquality(conj []sqlNode) (string, string, bool) {
	left, right, ok := splitEquals(conj)
	if !ok {
		return "", "", false
	}
	qa, okA := orgColumn(left)
	qb, okB := orgColumn(right)
	if !okA || !okB || qa == "" || qb == "" {
		return "", "", false
	}
	return qa, qb, true
}

// tableNeedsOrgScope: a table devhealthschema does not declare is held to the
// rule too (every ops table the catalog reads carries org_id); only a declared
// table without an org_id column is exempt.
func tableNeedsOrgScope(table string) bool {
	columns, declared := devhealthschema.ProductionColumns[table]
	if !declared {
		return true
	}
	for _, column := range columns {
		if column.Name == "org_id" {
			return true
		}
	}
	return false
}
