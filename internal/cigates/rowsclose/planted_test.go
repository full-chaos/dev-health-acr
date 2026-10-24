package rowsclose

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

var plantedPackages = []string{"noclose", "notdeferred", "earlyreturn", "escapes", "negative"}

// The walker is itself tested: each planted package under testdata/planted must yield exactly its sites, and the
// negative package none. A clause removed from the rule is RED here.
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
	const dir = "internal/cigates/rowsclose/testdata/planted/"
	want := map[site]int{
		{dir + "noclose/noclose.go", "Never", kindNoClose}:                                            1,
		{dir + "noclose/noclose.go", "Blank", kindNoClose}:                                            1,
		{dir + "noclose/noclose.go", "Statement", kindNoClose}:                                        1,
		{dir + "noclose/noclose.go", "Declared", kindNoClose}:                                         1,
		{dir + "noclose/noclose.go", "DeferOther", kindNoClose}:                                       1,
		{dir + "noclose/noclose.go", "WrongVariable", kindNoClose}:                                    1,
		{dir + "notdeferred/notdeferred.go", "ClosedAtTheEnd", kindCloseNotDeferred}:                  1,
		{dir + "earlyreturn/earlyreturn.go", "ReturnBetween", kindReturnBefore}:                       1,
		{dir + "earlyreturn/earlyreturn.go", "DeferOnOneBranch", kindReturnBefore}:                    1,
		{dir + "earlyreturn/earlyreturn.go", "StaleErrorCheck", kindReturnBefore}:                     1,
		{dir + "notdeferred/notdeferred.go", "ConditionalInTheClosure", kindCloseNotDeferred}:         1,
		{dir + "notdeferred/notdeferred.go", "ReturnBeforeCloseInTheClosure", kindCloseNotDeferred}:   1,
		{dir + "notdeferred/notdeferred.go", "PanicBeforeCloseInTheClosure", kindCloseNotDeferred}:    1,
		{dir + "notdeferred/notdeferred.go", "GotoBeforeCloseInTheClosure", kindCloseNotDeferred}:     1,
		{dir + "notdeferred/notdeferred.go", "GoexitBeforeCloseInTheClosure", kindCloseNotDeferred}:   1,
		{dir + "notdeferred/notdeferred.go", "InvokedLiteralPanicsBeforeClose", kindCloseNotDeferred}: 1,
		{dir + "earlyreturn/earlyreturn.go", "StatementBetween", kindReturnBefore}:                    1,
		{dir + "escapes/escapes.go", "Returned", kindEscapes}:                                         1,
		{dir + "escapes/escapes.go", "ReturnedLater", kindEscapes}:                                    1,
		{dir + "escapes/escapes.go", "Wrapped", kindEscapes}:                                          1,
		{dir + "escapes/escapes.go", "Stored", kindEscapes}:                                           1,
		{dir + "escapes/escapes.go", "Nested", kindEscapes}:                                           1,
		{dir + "escapes/escapes.go", "Goroutine", kindEscapes}:                                        1,
		{dir + "escapes/escapes.go", "Sent", kindEscapes}:                                             1,
		{dir + "escapes/escapes.go", "Aliased", kindEscapes}:                                          1,
		{dir + "escapes/escapes.go", "WrappedLocal", kindEscapes}:                                     1,
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

// The verdict names every defect of the allow rows: an unlisted site, a count that differs, a row without a reason and
// a stale row.
func TestTheVerdictNamesEveryDefect(t *testing.T) {
	found := map[site]int{
		{"a.go", "A", kindNoClose}:          1,
		{"b.go", "B", kindEscapes}:          2,
		{"c.go", "C", kindCloseNotDeferred}: 1,
	}
	rows := map[site]allowRow{
		{"b.go", "B", kindEscapes}:          {count: 1, reason: "wrapper"},
		{"c.go", "C", kindCloseNotDeferred}: {count: 1, reason: " "},
		{"d.go", "D", kindNoClose}:          {count: 1, reason: "gone"},
	}
	got := strings.Join(compare(found, rows), "\n")
	want := strings.Join([]string{
		"COUNT differs: b.go B rows-escapes: allow.tsv 1, code 2",
		"NO REASON: c.go C close-not-deferred",
		"STALE row: d.go D no-close",
		"UNLISTED site: a.go A no-close (x1)",
	}, "\n")
	if got != want {
		t.Errorf("verdict\n got:\n%s\nwant:\n%s", got, want)
	}
}

// The scope pin fails when the set of production files outside every release build changes.
func TestTheScopePinFailsWhenTheOutsideSetChanges(t *testing.T) {
	if got := scopeProblems([]string{"a.go"}, []string{"a.go"}); len(got) != 0 {
		t.Errorf("equal sets must pass, got %v", got)
	}
	if got := scopeProblems([]string{"a.go", "b.go"}, []string{"a.go"}); len(got) != 1 {
		t.Errorf("a new outside file must fail, got %v", got)
	}
}
