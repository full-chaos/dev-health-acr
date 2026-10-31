package grantclass

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

var plantedPackages = []string{"classify", "kinds", "negative"}

// The walker is itself tested: the planted packages must yield exactly their
// sites, and the negative package none. A kind removed from the walker is RED
// here. The four mutations the design names are among them: a derived bool
// (DerivedField, and DerivedLocal through a local variable), a method value (MethodValue), interface transport
// (TransportArgument, TransportMap) and a second decision inside one function
// (TwoDecisions, counted twice).
func TestTheWalkerFindsEveryPlantedKind(t *testing.T) {
	root := repopath.Root(t)
	var patterns []string
	for _, name := range plantedPackages {
		patterns = append(patterns, fixturesBase+"/"+name)
	}
	found, problems, _ := scan(t, root, patterns)
	for _, problem := range problems {
		t.Error(problem)
	}
	const kinds = "internal/cigates/grantclass/testdata/planted/kinds/kinds.go"
	want := map[site]int{
		{kinds, "Compare", kindCompare}:             1,
		{kinds, "SwitchPartial", kindSwitchPartial}: 1,
		{kinds, "SwitchDefault", kindSwitchDefault}: 1,
		{kinds, "DerivedField", kindCompare}:        1,
		{kinds, "DerivedField", kindDerivedEscape}:  1,
		{kinds, "DerivedLocal", kindCompare}:        1,
		{kinds, "DerivedLocal", kindDerivedEscape}:  1,
		{kinds, "Lookup", kindLookup}:               1,
		{kinds, "MethodValue", kindMethodValue}:     1,
		{kinds, "TransportString", kindTransport}:   1,
		{kinds, "TransportArgument", kindTransport}: 1,
		{kinds, "TransportMap", kindTransport}:      1,
		{kinds, "Classified", kindClassify}:         1,
		{kinds, "RawGranted", kindRawGrant}:         1,
		{kinds, "TwoDecisions", kindCompare}:        2,
	}
	if got, expected := render(found), render(want); got != expected {
		t.Errorf("planted sites differ\n got:\n%s\nwant:\n%s", got, expected)
	}
}

func render(sites map[site]int) string {
	var lines []string
	for key, count := range sites {
		lines = append(lines, fmt.Sprintf("%s %s %s x%d", key.file, key.symbol, key.kind, count))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// The verdict names every defect of the rows: an unlisted site, a count that
// differs (a second decision added to a listed function), an undecided row,
// a row without a note and a stale row.
func TestTheVerdictNamesEveryDefect(t *testing.T) {
	found := map[site]int{
		{"a.go", "A", kindCompare}:   1,
		{"b.go", "B", kindCompare}:   2,
		{"c.go", "C", kindTransport}: 1,
		{"e.go", "E", kindClassify}:  1,
	}
	rows := map[site]decisionRow{
		{"b.go", "B", kindCompare}:   {count: 1, decision: "r-sub", note: "restricted path"},
		{"c.go", "C", kindTransport}: {count: 1, decision: "vocab", note: " "},
		{"e.go", "E", kindClassify}:  {count: 1, decision: "open", note: "not decided"},
		{"d.go", "D", kindCompare}:   {count: 1, decision: "deny", note: "gone"},
	}
	got := strings.Join(compare(found, rows), "\n")
	want := strings.Join([]string{
		"COUNT differs: b.go B compare: decisions.tsv 1, code 2",
		"NO NOTE: c.go C transport",
		"STALE row: d.go D compare",
		`UNDECIDED site: e.go E classify: decision "open" (want r-sub, proj, deny, leave, consent or vocab)`,
		"UNLISTED site: a.go A compare (x1)",
	}, "\n")
	if got != want {
		t.Errorf("verdict\n got:\n%s\nwant:\n%s", got, want)
	}
}

// The scope pin fails when the set of production files outside every release
// build changes.
func TestTheScopePinFailsWhenTheOutsideSetChanges(t *testing.T) {
	if got := scopeProblems([]string{"a.go"}, []string{"a.go"}); len(got) != 0 {
		t.Errorf("equal sets must pass, got %v", got)
	}
	if got := scopeProblems([]string{"a.go", "b.go"}, []string{"a.go"}); len(got) != 1 {
		t.Errorf("a new outside file must fail, got %v", got)
	}
}
