// Package cells is the one table of repository keys every key validator is
// held to: the reposlug parser, the published schema patterns and the contract
// validators. It is test support and imports nothing.
package cells

import "strings"

// Cell is one repository key and whether it is valid once ASCII case is folded.
type Cell struct {
	Name  string
	Slug  string
	Valid bool
}

func Table() []Cell {
	segment := func(n int) string { return strings.Repeat("a", n) }
	cells := []Cell{
		{"two segments", "acme/web", true},
		{"mixed case", "Acme/Grp/Web", true},
		{"three segments", "acme/grp/web", true},
		{"twenty segments", strings.Repeat("g/", 19) + "x", true},
		{"255 bytes", segment(100) + "/" + segment(100) + "/" + segment(53), true},
		{"dot-leading last segment", "acme/grp/.github", true},
		{"dots and dashes inside", "acme/g.r_p-1/we.b_2-x", true},
		{"21 segments", strings.Repeat("g/", 20) + "x", false},
		{"256 bytes", segment(100) + "/" + segment(100) + "/" + segment(54), false},
		{"256 bytes mixed case", strings.ToUpper(segment(100)) + "/" + segment(100) + "/" + segment(54), false},
		{"dot segment", "acme/./web", false},
		{"dot-dot segment", "acme/../web", false},
		{"dot-dot last", "acme/grp/..", false},
		{"dot last", "acme/grp/.", false},
		{"leading dot group", "acme/.grp/web", false},
		{"leading dot owner", ".acme/web", false},
		{"101-character group", "acme/" + segment(101) + "/web", false},
		{"101-character last", "acme/" + segment(101), false},
		{"one segment", "acme", false},
		{"empty", "", false},
		{"empty segment", "acme//web", false},
		{"trailing slash", "acme/grp/", false},
		{"leading slash", "/acme/web", false},
		{"wildcard", "acme/*", false},
		{"wildcard group", "acme/*/web", false},
		{"space", "acme/g rp/web", false},
		{"newline", "acme/web\n", false},
		{"non-ASCII letter", "acme/wéb", false},
		{"kelvin sign", "acme/Keep", false},
	}
	return cells
}
