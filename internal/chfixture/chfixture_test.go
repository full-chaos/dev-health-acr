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
			name:      "OR-arm is rejected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id OR a.org_id = b.org_id",
			wantBad:   []string{"a.id = b.id OR a.org_id = b.org_id"},
		},
		{
			name:      "a bare boolean-function conjunct with no = is rejected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND has(b.arr, a.x)",
			wantBad:   []string{"has(b.arr, a.x)"},
		},
		{
			name:      "<> is rejected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND a.kind <> b.kind",
			wantBad:   []string{"a.kind <> b.kind"},
		},
		{
			name:      "IN is rejected",
			statement: "SELECT 1 FROM a INNER JOIN b ON a.id = b.id AND a.kind IN ('x', 'y')",
			wantBad:   []string{"a.kind IN ('x', 'y')"},
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
			wantBad: []string{"a.id = b.id OR a.org_id = b.org_id"},
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
			wantBad:   []string{"a.id = b.id OR a.org_id = b.org_id"},
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
			if clauses == 0 || conjuncts == 0 {
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
