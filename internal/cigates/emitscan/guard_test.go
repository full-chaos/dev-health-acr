package emitscan_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
	"github.com/full-chaos/dev-health-acr/internal/otelexport"
)

// scanDeadlineBudget is how long one scan of the repository may run before the guard cancels it. It follows the test binary's
// own deadline (go test -timeout) and leaves a margin to report a clean failure before the binary is killed, so a slow runner
// can no longer fail a scan that is making progress; the cost itself is pinned by the work budget and the CPU-time rows. A
// binary with no deadline gets a ten minute cap.
func scanDeadlineBudget(now, deadline time.Time, hasDeadline bool) time.Duration {
	const (
		budgetCap = 10 * time.Minute
		margin    = 5 * time.Second
		floor     = time.Second
	)
	if !hasDeadline {
		return budgetCap
	}
	if budget := deadline.Sub(now) - margin; budget > floor {
		return budget
	}
	return floor
}

// runScan scans dirs under root and gives up, cancelling the work itself, after budget.
func runScan(budget time.Duration, root string, dirs []string) ([]emitscan.Finding, []emitscan.Site, error) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return emitscan.ScanContext(ctx, root, dirs)
}

// scanBudget is the budget of a scan started now by this test.
func scanBudget(t *testing.T) time.Duration {
	t.Helper()
	deadline, ok := t.Deadline()
	return scanDeadlineBudget(time.Now(), deadline, ok)
}

func scanWithin(t *testing.T) ([]emitscan.Finding, []emitscan.Site) {
	t.Helper()
	budget := scanBudget(t)
	findings, sites, err := runScan(budget, repoRoot(t), []string{"internal", "cmd"})
	if err != nil {
		t.Fatalf("the emit-path scan did not finish within %s (the test deadline less a margin): %v", budget, err)
	}
	return findings, sites
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

// goFilesUnder lists the Go files the scan reads. The guard names its root by go.mod and enumerates files itself, so the shard
// planner (scripts/ci/reached-shards.sh, rule 4: a test that names a root outside its directory and enumerates files walks
// the tree) reaches this package from any change.
func goFilesUnder(t *testing.T, root string, dirs []string) []string {
	t.Helper()
	var files []string
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestTheScanHasSourceToRead(t *testing.T) {
	if files := goFilesUnder(t, repoRoot(t), []string{"internal", "cmd"}); len(files) < 100 {
		t.Fatalf("only %d Go files under internal and cmd: the census would pass over an empty tree", len(files))
	}
}

// The census gate must run on every pull request that touches Go: the planner reaches this package from a change in a
// package that shares no import with it, and runs every shard for a change under scripts/ci/.
func TestThePlannerAlwaysReachesTheCensusGate(t *testing.T) {
	root := repoRoot(t)
	plan := func(changed string) (reached string, mode string) {
		t.Helper()
		dir := t.TempDir()
		changedFile := filepath.Join(dir, "changed.txt")
		reachedFile := filepath.Join(dir, "reached.txt")
		if err := os.WriteFile(changedFile, []byte(changed+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", filepath.Join(root, "scripts", "ci", "reached-shards.sh"), "--event", "pull_request", "--changed", changedFile, "--reached-out", reachedFile)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("reached-shards.sh: %v\n%s", err, out)
		}
		raw, _ := os.ReadFile(reachedFile)
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "reach_mode=") {
				mode = strings.TrimPrefix(line, "reach_mode=")
			}
		}
		return string(raw), mode
	}
	const gate = "internal/cigates/emitscan"
	// the path is built at run time: a test that names the changed file in its source is reached by that very rule
	outside := "internal/contract" + "check/ru" + "n.go"
	reached, mode := plan(outside)
	if mode != "full" && !strings.Contains(reached, gate) {
		t.Fatalf("a change to %s (mode %q) does not reach %s: the census gate can be skipped", outside, mode, gate)
	}
	// control: the planner does prune, so the check above is not vacuous
	if mode == "reached" && strings.Count(reached, "\n") < 2 {
		t.Fatalf("control: the planner reached only %q", reached)
	}
	if _, mode := plan("scripts/ci/reached-shards.sh"); mode != "full" {
		t.Fatalf("a change under scripts/ci/ runs mode %q, want full", mode)
	}
}

func TestScanBudgetFollowsTheTestDeadline(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		deadline    time.Time
		hasDeadline bool
		want        time.Duration
	}{
		{"no deadline: the ten minute cap", time.Time{}, false, 10 * time.Minute},
		{"a fifteen minute deadline leaves the margin", now.Add(15 * time.Minute), true, 15*time.Minute - 5*time.Second},
		{"a ten minute deadline (the go test default)", now.Add(10 * time.Minute), true, 10*time.Minute - 5*time.Second},
		{"three seconds left: the floor", now.Add(3 * time.Second), true, time.Second},
		{"the deadline has passed: the floor", now.Add(-time.Minute), true, time.Second},
	}
	for _, tc := range cases {
		if got := scanDeadlineBudget(now, tc.deadline, tc.hasDeadline); got != tc.want {
			t.Errorf("%s: budget = %s, want %s", tc.name, got, tc.want)
		}
	}
	// the wiring: the budget a test's scan really gets is the one derived from this test's own deadline
	deadline, ok := t.Deadline()
	want := scanDeadlineBudget(time.Now(), deadline, ok)
	if got := scanBudget(t); got > want || want-got > time.Second {
		t.Errorf("scanBudget = %s, want about %s (deadline %v, set %v)", got, want, deadline, ok)
	}
	if ok && scanBudget(t) == 30*time.Second {
		t.Error("the scan budget is the old fixed 30 s")
	}
}

// The guard still ends a scan that never returns: with the work budget raised so only the context can stop it, a short
// budget cancels it and the guard's own path reports the context error.
func TestTheGuardFailsOnAHangingScan(t *testing.T) {
	root := ringRoot(t, 2000)
	defer emitscan.SetWorkBudget(1<<50, 1<<30)()
	start := time.Now()
	_, _, err := runScan(200*time.Millisecond, root, []string{"internal"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the hanging scan outlasted its budget by %s", elapsed)
	}
}
