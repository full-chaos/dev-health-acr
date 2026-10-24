package emitscan_test

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
	"github.com/full-chaos/dev-health-acr/internal/otelexport"
)

// scanBudget is the wall-time budget of one scan of the repository. The scan bounds its own work per package; this is the
// second, outer bound: a scan that outlasts it fails the test instead of hanging the run.
const scanBudget = 30 * time.Second

func scanWithin(t *testing.T) ([]emitscan.Finding, []emitscan.Site) {
	t.Helper()
	type result struct {
		findings []emitscan.Finding
		sites    []emitscan.Site
		err      error
	}
	done := make(chan result, 1)
	root := repoRoot(t)
	go func() {
		findings, sites, err := emitscan.ScanWithCensus(root, []string{"internal", "cmd"})
		done <- result{findings, sites, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.findings, r.sites
	case <-time.After(scanBudget):
		t.Fatalf("the emit-path scan outlasted its %s budget", scanBudget)
		return nil, nil
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// pinKey identifies a pinned finding by site, not by line: rule, file, enclosing function and detail.
type pinKey struct{ rule, file, fn, detail string }

type pin struct {
	key   pinKey
	count int
}

func readPins(t *testing.T) []pin {
	t.Helper()
	handle, err := os.Open("pins.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	var pins []pin
	scanner := bufio.NewScanner(handle)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 6 || strings.TrimSpace(fields[5]) == "" {
			t.Fatalf("pins.tsv: a row needs rule, file, func, detail, count and a reason: %q", scanner.Text())
		}
		count, err := strconv.Atoi(fields[4])
		if err != nil || count < 1 {
			t.Fatalf("pins.tsv: bad count in the row for %s", fields[1])
		}
		pins = append(pins, pin{key: pinKey{fields[0], fields[1], fields[2], fields[3]}, count: count})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return pins
}

// TestEmitPathsMatchTheCensus is the gate: every finding of the scan over the
// emit paths is pinned exactly (rule, file, count, reason), a pin without a
// finding is stale, and a file the scan cannot read is a failure.
func TestEmitPathsMatchTheCensus(t *testing.T) {
	findings, _ := scanWithin(t)
	pinned := map[pinKey]int{}
	for _, p := range readPins(t) {
		if _, dup := pinned[p.key]; dup {
			t.Fatalf("pins.tsv: %s %s %s is pinned twice", p.key.rule, p.key.file, p.key.fn)
		}
		pinned[p.key] = p.count
	}
	found := map[pinKey][]emitscan.Finding{}
	for _, finding := range findings {
		detail := finding.Detail
		if detail == "" {
			detail = "-"
		}
		key := pinKey{finding.Rule, finding.File, finding.Func, detail}
		found[key] = append(found[key], finding)
	}
	var keys []pinKey
	for key := range found {
		keys = append(keys, key)
	}
	for key := range pinned {
		if _, ok := found[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].file != keys[j].file {
			return keys[i].file < keys[j].file
		}
		if keys[i].fn != keys[j].fn {
			return keys[i].fn < keys[j].fn
		}
		return keys[i].rule < keys[j].rule
	})
	seen := map[pinKey]bool{}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		have := found[key]
		want := pinned[key]
		if len(have) == want {
			continue
		}
		if len(have) == 0 {
			t.Errorf("%s %s %s %s: stale pin (no finding), remove the row", key.rule, key.file, key.fn, key.detail)
			continue
		}
		var where []string
		for _, finding := range have {
			where = append(where, fmt.Sprintf("%d", finding.Line))
		}
		t.Errorf("%s %s %s %s: %d finding(s) at line %s, %d pinned", key.rule, key.file, key.fn, key.detail, len(have), strings.Join(where, ","), want)
	}
}

// siteKey names a census row: file and enclosing function.
type siteKey struct{ file, fn string }

func readSiteTable(t *testing.T, name string) map[siteKey]string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] != "file\tfunc\tkeys\treason" {
		t.Fatalf("%s: the first line must be the header file<TAB>func<TAB>keys<TAB>reason", name)
	}
	table := map[siteKey]string{}
	for _, line := range lines[1:] {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || fields[0] == "" || fields[1] == "" || fields[2] == "" || strings.TrimSpace(fields[3]) == "" {
			t.Fatalf("%s: a row needs file, func, keys and a reason: %q", name, line)
		}
		key := siteKey{fields[0], fields[1]}
		if _, dup := table[key]; dup {
			t.Fatalf("%s: %s %s is listed twice", name, key.file, key.fn)
		}
		keys := strings.Split(fields[2], ",")
		if !sort.StringsAreSorted(keys) {
			t.Fatalf("%s: the keys of %s %s are not sorted", name, key.file, key.fn)
		}
		table[key] = fields[2]
	}
	return table
}

func censusOf(t *testing.T, kind string) map[siteKey]string {
	t.Helper()
	_, sites := scanWithin(t)
	out := map[siteKey]string{}
	for _, site := range sites {
		if site.Kind == kind {
			out[siteKey{site.File, site.Func}] = strings.Join(site.Keys, ",")
		}
	}
	return out
}

func diffTables(have, want map[siteKey]string) []string {
	var problems []string
	for key, keys := range have {
		switch listed, ok := want[key]; {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s %s emits %s and is not in the table: add a row with a reason or remove the emit", key.file, key.fn, keys))
		case listed != keys:
			problems = append(problems, fmt.Sprintf("%s %s emits %s, the table says %s", key.file, key.fn, keys, listed))
		}
	}
	for key := range want {
		if _, ok := have[key]; !ok {
			problems = append(problems, fmt.Sprintf("%s %s is in the table but emits nothing: stale row, remove it", key.file, key.fn))
		}
	}
	sort.Strings(problems)
	return problems
}

// TestMetricLabelSitesMatchLabelsTSV pins the label keys of every metric.WithAttributes call site: a new site, a changed
// key set or a stale row fails until the table says so. Unbounded words (ids, paths, free text) are refused by rule R2.
func TestMetricLabelSitesMatchLabelsTSV(t *testing.T) {
	for _, problem := range diffTables(censusOf(t, "metric"), readSiteTable(t, "labels.tsv")) {
		t.Error(problem)
	}
}

// TestSpanAttributeKeysMatchSpansTSVAndAreSafe pins the span attribute keys the code sets, and every one of them must be
// a key the span scrub lets leave the process.
func TestSpanAttributeKeysMatchSpansTSVAndAreSafe(t *testing.T) {
	have := censusOf(t, "attribute")
	for _, problem := range diffTables(have, readSiteTable(t, "spans.tsv")) {
		t.Error(problem)
	}
	for key, keys := range have {
		for _, attribute := range strings.Split(keys, ",") {
			if !otelexport.SafeSpanKey(attribute) {
				t.Errorf("%s %s sets the span attribute %q, which otelexport.SafeSpanKey drops before export", key.file, key.fn, attribute)
			}
		}
	}
}

func TestSiteTableDiffNamesEveryKindOfDrift(t *testing.T) {
	have := map[siteKey]string{{"a.go", "f"}: "x", {"b.go", "g"}: "y,z", {"c.go", "h"}: "w"}
	want := map[siteKey]string{{"a.go", "f"}: "x", {"b.go", "g"}: "y", {"d.go", "i"}: "v"}
	got := strings.Join(diffTables(have, want), "\n")
	for _, fragment := range []string{"c.go h emits w and is not in the table", "b.go g emits y,z, the table says y", "d.go i is in the table but emits nothing"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("drift report misses %q:\n%s", fragment, got)
		}
	}
	if len(diffTables(have, have)) != 0 {
		t.Error("identical tables reported drift")
	}
}
