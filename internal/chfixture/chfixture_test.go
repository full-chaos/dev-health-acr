package chfixture_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
)

func TestAtLeastVersionFloor(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    bool
	}{
		{"exact floor", "26.7.5.10", true},
		{"higher patch", "26.7.9.1", true},
		{"higher minor", "26.10.1.1", true},
		{"higher major", "27.1.1.1", true},
		{"lower minor lexically-higher digit", "26.10.1.1", true}, // guards the "10" < "7" string trap
		{"below floor minor", "26.6.9.9", false},
		{"below floor major", "24.8.14.1", false},
		{"malformed", "not-a-version", false},
		{"single component", "26", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chfixture.AtLeastVersionFloor(tc.version); got != tc.want {
				t.Errorf("AtLeastVersionFloor(%q) = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}

// TestJoinONViolations is CHAOS-4549's own guard proving itself, not just
// the producers it checks. team-lead's 2026-08-29 scope correction relaxed
// the operand grammar (literals and any-arity/nested function calls are
// portable -- an earlier, stricter draft rejected several already-shipped,
// already-CI-proven ON clauses on main and would have forced unrelated
// production rewrites into this PR); the four RED cases below are exactly
// what still must fail: OR, a bare boolean-function conjunct with no "=",
// "<>", and "IN". The GREEN cases prove the grammar is not narrower than
// that: a plain equality, an AND-conjunction, a literal conjunct, a
// single-argument cast, a multi-argument/nested function call, and the
// SQL-comment false-positive this guard hit once already (a `--` comment
// whose prose contained the literal words " ON " and " AND ").
func TestJoinONViolations(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		wantBad   []string
		// wantZeroClauses marks a case that legitimately finds NO JOIN ON
		// clause at all (no JOIN in the statement, or a JOIN with no ON of
		// its own) -- the default "clauses == 0 is a guard bug" check
		// below is skipped for these.
		wantZeroClauses bool
	}{
		{
			name:      "plain equality is portable",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id",
			wantBad:   nil,
		},
		{
			name:      "AND-conjunction of plain equalities is portable",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND a.org_id = b.org_id",
			wantBad:   nil,
		},
		{
			name:      "single-argument cast of a column is portable",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.org_id = toString(b.org_id)",
			wantBad:   nil,
		},
		{
			name:      "a literal conjunct is portable (m.is_active = 1, tables.go)",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND b.is_active = 1",
			wantBad:   nil,
		},
		{
			name:      "a multi-argument function conjunct is portable (ifNull(a.team_id, ''), teams_projects_edges.go)",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = ifNull(b.team_id, '')",
			wantBad:   nil,
		},
		{
			name:      "a nested function call is portable",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = toString(ifNull(b.team_id, 0))",
			wantBad:   nil,
		},
		{
			name:      "an array-subscript operand is portable (splitByString(...)[1], dev-health-go readers/investment_theme.go:135)",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.repo_uuid = splitByString('#pr', b.evidence_ref)[1]",
			wantBad:   nil,
		},
		{
			name:      "codex review: a string literal containing = is portable, not a 3-way split",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = 'foo=bar'",
			wantBad:   nil,
		},
		{
			name:      "codex review: a string literal containing AND is portable, not a phantom conjunct boundary",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND a.tag = 'x AND y'",
			wantBad:   nil,
		},
		{
			// team-lead's own non-blocking review note: a bare "ANY"/"ALL"
			// terminator would truncate this condition right before
			// any(...), leaving "a.x = " dangling with no right operand.
			name:      "team-lead review: any(...)/all(...) as an operand's function call is portable, not a clause terminator",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.x = any(b.arr) AND a.y = all(b.arr2)",
			wantBad:   nil,
		},
		{
			// codex R3: chaos4099_scope_expander.go's projectRepositoriesAsOf
			// carries this exact shape in a real ON clause.
			name:      "codex R3: a bound parameter {name:Type} operand is portable",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND r.org_id = {org_id:String}",
			wantBad:   nil,
		},
		{
			name:      "codex R3: a bound parameter with a nested/parameterized type is portable",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND iv.observed_at = {as_of:Nullable(DateTime64(3, 'UTC'))}",
			wantBad:   nil,
		},
		{
			// codex R4: a bare "LEFT" terminator (pre-fix) truncates the
			// condition right at left(...), producing an empty/partial
			// condition that silently never sees the OR after it. The
			// reported violation text is UPPERCASE (codex R5: everything
			// downstream reads the single normalized -- uppercased outside
			// literals -- text; violation strings are no exception).
			name:      "codex R4: left(...)/right(...) as an operand's function call is portable, and an OR after it is still detected",
			statement: "SELECT 1 FROM a INNER JOIN b ON left(a.key, 3) = b.key OR a.org_id = b.org_id",
			wantBad:   []string{"LEFT(A.KEY, 3) = B.KEY OR A.ORG_ID = B.ORG_ID"},
		},
		{
			// codex R4: a literal 'ON' must never be mistaken for the JOIN
			// keyword -- this statement has no JOIN at all.
			name:            "codex R4: a string literal spelling ON is not a phantom clause",
			statement:       "SELECT if(flag, 'ON', 'OFF') FROM a",
			wantBad:         nil,
			wantZeroClauses: true,
		},
		{
			// codex R4: a JOIN using USING(...) has no ON condition of its
			// own; the guard must not walk past it to grab an unrelated ON
			// belonging to a different clause deeper in the statement.
			name:            "codex R4: a JOIN ... USING(...) with no ON contributes no clause",
			statement:       "SELECT 1 FROM a JOIN b USING (id) WHERE a.flag = if(a.on_call, 'ON', 'OFF')",
			wantBad:         nil,
			wantZeroClauses: true,
		},
		// codex R5: four scans (comment strip, ')' boundary, AND split,
		// qualifier-phrase match) each had their own case/quote/whitespace
		// blind spot, found and patched one at a time -- the fix is the
		// single normalize() pass every one of these four cases exercises
		// a different corner of. Confirmed against acca848b (pre-fix) via
		// a throwaway probe: all four produced wrong output (case 0:
		// clauses=1 not 2, truncated mid-literal; case 1: a garbled
		// violation merging both joins' text; case 2: clauses=1/conjuncts=1,
		// the whole thing reported as one false-positive violation; case 3:
		// "LEFT" leaking into a garbled cross-join violation).
		{
			name:      "codex R5: a -- inside a string literal is not a comment, and a later JOIN's OR is still detected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.tag = '--not a comment' LEFT JOIN c ON c.id = a.id OR c.id = b.id",
			wantBad:   []string{"C.ID = A.ID OR C.ID = B.ID"},
		},
		{
			name:      "codex R5: a ) inside a string literal is not the structural close paren, and a later JOIN's OR is still detected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.tag = 'x)' LEFT JOIN c ON c.id = a.id OR c.id = b.id",
			wantBad:   []string{"C.ID = A.ID OR C.ID = B.ID"},
		},
		{
			name:      "codex R5: lowercase 'and' splits a conjunction just like AND, not a false violation",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id and a.org_id = b.org_id",
			wantBad:   nil,
		},
		{
			name:      "codex R5: a qualifier phrase split across a newline (LEFT\\nJOIN) still terminates the previous condition and is still found as its own join",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id\nLEFT\nJOIN c ON c.id = a.id OR c.id = b.id",
			wantBad:   []string{"C.ID = A.ID OR C.ID = B.ID"},
		},
		{
			// team-lead's re-review of acca848b: a '(' inside a string
			// literal used to count as real nesting (depthProfile was not
			// quote-aware), which desyncs depth for the REST of the
			// statement and can swallow a later, unrelated JOIN's own ON
			// clause into this one -- silently skipping it rather than
			// checking it separately. A statement with only one JOIN
			// can't exhibit this (nothing after it to wrongly absorb);
			// this needs a SECOND join whose own real violation would
			// otherwise vanish into the first join's garbled, absorbed
			// "condition". Confirmed against the pre-fix code: it
			// reported clauses=1 (not 2) and a single mangled violation
			// spanning both joins, instead of cleanly attributing the OR
			// to the second join alone. The literal '(' itself stays
			// lowercase in the reported text (codex R5: literal CONTENTS
			// are copied through normalize() unchanged, only the
			// surrounding structure is uppercased).
			name:      "team-lead review: a literal containing a paren character does not desynchronize JOIN/ON depth tracking for a LATER join",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.k = concat('(', b.k) LEFT JOIN c ON c.id = a.id OR c.id = b.id",
			wantBad:   []string{"C.ID = A.ID OR C.ID = B.ID"},
		},
		{
			name:      "OR-arm is rejected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id OR a.org_id = b.org_id",
			wantBad:   []string{"A.ID = B.ID OR A.ORG_ID = B.ORG_ID"},
		},
		{
			name:      "a bare boolean-function conjunct with no = is rejected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND has(b.arr, a.x)",
			wantBad:   []string{"HAS(B.ARR, A.X)"},
		},
		{
			name:      "<> is rejected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND a.kind <> b.kind",
			wantBad:   []string{"A.KIND <> B.KIND"},
		},
		{
			// the quoted literals 'x'/'y' stay lowercase -- only structure
			// outside literals is uppercased.
			name:      "IN is rejected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND a.kind IN ('x', 'y')",
			wantBad:   []string{"A.KIND IN ('x', 'y')"},
		},
		{
			name: "a -- comment whose prose contains the words ON and AND never contributes a phantom clause",
			statement: `SELECT 1 FROM a
-- so the caller's JOIN ON below never carries a literal comparison -- the
-- pre-26 analyzer's ON accepts only a plain column-equality conjunction.
INNER JOIN b ON a.id = b.id`,
			wantBad: nil,
		},
		// R1 (team-lead, 2026-08-29): the ON condition used to end at the
		// first newline, so an OR/predicate on a LATER line of a
		// multi-line ON was never seen. These three cases are red against
		// the pre-fix code (proven in the PR body) and green here.
		{
			name: "a1: multi-line ON with OR on the second line is rejected",
			statement: `SELECT 1 FROM a INNER JOIN b ON a.id = b.id
	OR a.org_id = b.org_id`,
			wantBad: []string{"A.ID = B.ID OR A.ORG_ID = B.ORG_ID"},
		},
		{
			name: "a2: a conjunct split across multiple source lines by AND is still portable",
			statement: `SELECT 1 FROM a INNER JOIN b ON a.id = b.id
	AND a.org_id = b.org_id`,
			wantBad: nil,
		},
		{
			name:      "b1: ON at the start of a line (no leading space) still finds the OR on it",
			statement: "SELECT 1 FROM a INNER JOIN b\nON a.id = b.id OR a.org_id = b.org_id",
			wantBad:   []string{"A.ID = B.ID OR A.ORG_ID = B.ORG_ID"},
		},
		{
			name: "a JOIN ON condition correctly stops at a following clause, never absorbing WHERE/GROUP BY/a second JOIN",
			statement: `SELECT 1 FROM a
INNER JOIN b ON a.id = b.id
LEFT JOIN c ON c.id = b.id
WHERE a.org_id = {org_id:String}
GROUP BY a.id`,
			wantBad: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			violations, clauses, conjuncts := chfixture.JoinONViolations(tc.statement)
			if tc.wantZeroClauses {
				if clauses != 0 || conjuncts != 0 {
					t.Fatalf("clauses/conjuncts = %d/%d, want 0/0 -- this statement has no ON clause to check", clauses, conjuncts)
				}
			} else if clauses == 0 || conjuncts == 0 {
				t.Fatalf("checked 0 clauses/conjuncts (%d/%d) -- the guard examined nothing", clauses, conjuncts)
			}
			if len(violations) != len(tc.wantBad) {
				t.Fatalf("violations = %v, want %v", violations, tc.wantBad)
			}
			for i, want := range tc.wantBad {
				if violations[i] != want {
					t.Errorf("violation[%d] = %q, want %q", i, violations[i], want)
				}
			}
		})
	}
}
