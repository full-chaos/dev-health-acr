package grantclass

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/releasebuild"
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
	result := scanPlanted(t)
	for _, problem := range result.problems {
		t.Error(problem)
	}
	found := result.counts
	const kinds = "internal/cigates/grantclass/testdata/planted/kinds/kinds.go"
	want := map[site]int{
		{kinds, "Compare", kindCompare}:               1,
		{kinds, "SwitchPartial", kindSwitch}:          1,
		{kinds, "SwitchPartial", kindSwitchPartial}:   1,
		{kinds, "SwitchDefault", kindSwitch}:          1,
		{kinds, "SwitchDefault", kindSwitchDefault}:   1,
		{kinds, "SwitchExhaustive", kindSwitch}:       1,
		{kinds, "TransportVar", kindTransport}:        1,
		{kinds, "TransportMapKey", kindTransport}:     1,
		{kinds, "DerivedField", kindCompare}:          1,
		{kinds, "DerivedField", kindDerivedEscape}:    1,
		{kinds, "DerivedLocal", kindCompare}:          1,
		{kinds, "DerivedLocal", kindDerivedEscape}:    1,
		{kinds, "Lookup", kindLookup}:                 1,
		{kinds, "MethodValue", kindMethodValue}:       1,
		{kinds, "TransportString", kindTransport}:     1,
		{kinds, "TransportArgument", kindTransport}:   1,
		{kinds, "TransportMap", kindTransport}:        1,
		{kinds, "Classified", kindClassify}:           1,
		{kinds, "RawGranted", kindRawGrant}:           1,
		{kinds, "TwoDecisions", kindCompare}:          2,
		{kinds, "CollectionSlice", kindCollection}:    1,
		{kinds, "CollectionMapKey", kindCollection}:   1,
		{kinds, "CollectionMapValue", kindCollection}: 1,
		{kinds, "CollectionArgument", kindCollection}: 1,
		{kinds, "TransportReturn", kindTransport}:     1,
		{kinds, "TransportNamed", kindTransport}:      1,
		{kinds, "TransportSend", kindTransport}:       1,
		{kinds, "TransportField", kindTransport}:      1,
		{kinds, "ClosureDecision", kindCompare}:       1,
	}
	if got, expected := render(found), render(want); got != expected {
		t.Errorf("planted sites differ\n got:\n%s\nwant:\n%s", got, expected)
	}
}

// scanPlanted scans the planted packages.
func scanPlanted(t *testing.T) scanResult {
	t.Helper()
	var patterns []string
	for _, name := range plantedPackages {
		patterns = append(patterns, fixturesBase+"/"+name)
	}
	return scan(t, repopath.Root(t), patterns)
}

// A class whose constant set differs from its pin fails, and the failure
// lists every row of that class to be decided again; a matching pin passes.
// The planted class has a, b and c: a pin of a and b is the class having
// gained c without the pin moving.
func TestAChangedClassVocabularyFailsWithEveryRowToReDecide(t *testing.T) {
	result := scanPlanted(t)
	const class = fixturesBase + "/classify.Class"
	if got := strings.Join(result.vocabulary[class], ","); got != "a,b,c" {
		t.Fatalf("planted vocabulary = %q, want a,b,c", got)
	}
	if problems := vocabularyProblems(result.vocabulary, map[string][]string{class: {"a", "b", "c"}}, result.classes); len(problems) != 0 {
		t.Fatalf("a pin that matches must pass, got %v", problems)
	}
	problems := vocabularyProblems(result.vocabulary, map[string][]string{class: {"a", "b"}}, result.classes)
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "class vocabulary changed: "+class+" pinned a,b, live a,b,c: re-decide these rows, then update the pin: ") {
		t.Fatalf("a class that gained a constant must fail once with the re-decide list, got %v", problems)
	}
	const kinds = "internal/cigates/grantclass/testdata/planted/kinds/kinds.go"
	for key := range result.counts {
		if key.file != kinds {
			continue
		}
		if row := fmt.Sprintf("%s %s %s", key.file, key.symbol, key.kind); !strings.Contains(problems[0], row) {
			t.Errorf("the re-decide list is missing %s", row)
		}
	}
	missing := vocabularyProblems(result.vocabulary, map[string][]string{}, result.classes)
	if len(missing) != 1 || !strings.HasPrefix(missing[0], "NO VOCABULARY PIN: "+class) {
		t.Fatalf("a class with no pin must fail, got %v", missing)
	}
	stale := vocabularyProblems(map[string][]string{}, map[string][]string{class: {"a"}}, result.classes)
	if len(stale) != 1 || !strings.HasPrefix(stale[0], "STALE vocabulary pin: "+class) {
		t.Fatalf("a pin for no loaded class must fail, got %v", stale)
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

// The scan covers every distinct (GOOS, GOARCH) pair of the release matrix,
// so a file built only for one architecture is read too.
func TestTheScanCoversEveryReleaseTarget(t *testing.T) {
	want := map[[2]string]bool{}
	for _, target := range releasebuild.Matrix() {
		want[[2]string{target.GOOS, target.GOARCH}] = true
	}
	got := map[[2]string]bool{}
	for _, pair := range releaseTargets() {
		if got[pair] {
			t.Errorf("release target %v scanned twice", pair)
		}
		got[pair] = true
	}
	if len(got) != len(want) {
		t.Fatalf("scanned %d release targets %v, want the %d distinct pairs %v", len(got), got, len(want), want)
	}
	for pair := range want {
		if !got[pair] {
			t.Errorf("release target %v is not scanned", pair)
		}
	}
}
