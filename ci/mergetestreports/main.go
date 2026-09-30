// Command mergetestreports merges the per-shard reports of CI's sharded `unit`
// job (CHAOS-3895) back into the single coverage and JUnit reports the
// published `go-coverage` and `go-junit` artifacts have always been.
//
// Each `unit` shard uploads one intermediate artifact, go-unit-shard-<i>,
// holding that shard's cover.out (Go coverage profile), junit.xml (gotestsum
// JUnit) and go-test.json (gotestsum's test2json stream). The `reports` job
// downloads all of them into one directory and runs this tool four times:
//
//	mergetestreports coverage   -dir D -expect N -out merged/cover.out
//	mergetestreports junit      -dir D -expect N -out merged/junit.xml
//	mergetestreports failures   -dir D -expect N [-summary FILE] [-report FILE]
//	mergetestreports crosscheck -dir D -expect N -junit ... -cover ... -cobertura ... -failures FILE
//
// `coverage` concatenates the profiles under ONE `mode:` header, merging any
// block that appears in more than one shard (max for `set`, sum for `count`
// and `atomic`, the same rule gocovmerge applies). Conversion to Cobertura is
// NOT done here: the Makefile's coverage-cobertura target runs the same pinned
// gocover-cobertura the shards' own `make test-coverage` uses.
//
// `junit` writes one <testsuites> root whose child <testsuite> elements are the
// shards' own, copied byte for byte (no re-encoding, so nothing a shard wrote
// -- properties, system-out, failure bodies -- can be lost or altered), and
// whose tests/failures/errors/skipped/time totals are the sum of the shards'.
//
// `failures` reads EVERY shard's go-test.json and lists every failed package
// and test across all of them, so one place shows the whole run's failures
// rather than the first failure of whichever shard the reader opened. -report
// writes what it read as JSON for `crosscheck`.
//
// `crosscheck` is the reports job's behaviour check: it recomputes testcase and
// suite counts from the raw shard files with a parser of its own and compares
// them, the merged coverage profile and the failure listing with the merged
// files the job wrote, so a merge that never ran, ran on fewer shards, or
// produced something other than the sum fails there. The failed-test set is
// re-derived from every shard's raw go-test.json AND from the merged JUnit, and
// both must equal the listing `failures` wrote; a failed test's identity is the
// (package, test) PAIR, never a joined string (see failKey). The Cobertura
// document is decoded whole (a truncated one is an error), its root counters are
// checked against its own class-level <line> elements, and it is tied to the
// merged profile by what holds exactly on real data (see checkCobertura).
//
// The tool fails closed on all malformed input, never guessing at it. A missing
// shard, an extra shard, a shard directory whose name is not the canonical
// decimal index (1, not 01), an empty file, a malformed profile, two shards
// that both claim one test suite, two profiles in different coverage modes, a
// go-test.json line that is not a test2json event or whose Action is outside
// the closed set (testActions), a JUnit root whose totals are absent,
// non-integer, or not what the document holds (see the table above
// junitRequiredCounts), or a JUnit file with more than one root, is an error and
// not a partial merge: a merged report that silently covers all but one shard, or
// states a total nobody measured, reads exactly like a correct one, which is the
// failure this whole job exists to prevent.
//
// What "not what the document holds" means was MEASURED against gotestsum
// v1.13.0 on a failing run, because a literal "root = sum of suites" refuses it:
// see the table above junitRequiredCounts, and the real failing-run fixture in
// testdata/gotestsum-failing-run.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// shardArtifactPrefix is the name prefix of each unit shard's intermediate
// artifact; the shard index follows it. .github/workflows/ci.yml's unit job
// uploads go-unit-shard-${{ matrix.shard }}, and scripts/ci/test-workflow-contract.sh
// proves the two agree.
const shardArtifactPrefix = "go-unit-shard-"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mergetestreports coverage|junit|failures|crosscheck -dir DIR -expect N [flags]")
		return 2
	}
	cmd, rest := args[0], args[1:]
	var (
		dir, out, summary, report string
		expect                    int
		cc                        crosscheckInputs
	)
	fs := flag.NewFlagSet("mergetestreports "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&dir, "dir", "", "directory holding one "+shardArtifactPrefix+"<i> subdirectory per shard")
	fs.IntVar(&expect, "expect", 0, "number of shards the unit matrix has; the merge refuses any other count")
	switch cmd {
	case "coverage", "junit":
		fs.StringVar(&out, "out", "", "merged report to write")
	case "failures":
		fs.StringVar(&summary, "summary", "", "optional file to append a markdown failure summary to (GITHUB_STEP_SUMMARY)")
		fs.StringVar(&report, "report", "", "optional JSON file recording what was read, for crosscheck")
	case "crosscheck":
		fs.StringVar(&cc.junit, "junit", "", "merged junit.xml to check")
		fs.StringVar(&cc.cover, "cover", "", "merged cover.out to check")
		fs.StringVar(&cc.cobertura, "cobertura", "", "Cobertura coverage.xml built from the merged profile")
		fs.StringVar(&cc.failures, "failures", "", "JSON failure report written by `failures -report`")
	default:
		fmt.Fprintf(stderr, "mergetestreports: unknown command %q (want coverage, junit, failures or crosscheck)\n", cmd)
		return 2
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	needOut := cmd == "coverage" || cmd == "junit"
	if dir == "" || expect < 1 || (needOut && out == "") ||
		(cmd == "crosscheck" && (cc.junit == "" || cc.cover == "" || cc.cobertura == "" || cc.failures == "")) {
		fmt.Fprintf(stderr, "mergetestreports %s: -dir and -expect (>=1) are required, and so is every output flag the command reads (-out; or -junit -cover -cobertura -failures)\n", cmd)
		return 2
	}

	var err error
	switch cmd {
	case "coverage":
		err = runCoverage(dir, expect, out, stdout)
	case "junit":
		err = runJUnit(dir, expect, out, stdout)
	case "failures":
		err = runFailures(dir, expect, summary, report, stdout)
	case "crosscheck":
		cc.dir, cc.expect = dir, expect
		err = runCrosscheck(cc, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "mergetestreports %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

// shardFiles returns, in shard order, the path of file `name` inside each of
// the expect shard directories. It fails unless the directory holds exactly the
// shards 1..expect and every one of them has a non-empty `name`.
func shardFiles(dir string, expect int, name string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read shard directory: %w", err)
	}
	seen := map[int]bool{}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), shardArtifactPrefix) {
			continue
		}
		suffix := strings.TrimPrefix(e.Name(), shardArtifactPrefix)
		idx, convErr := strconv.Atoi(suffix)
		// strconv.Itoa(idx) != suffix rejects 01 (and +1): two artifacts must
		// never map to one shard index, or one of them is silently ignored.
		if convErr != nil || idx < 1 || idx > expect || strconv.Itoa(idx) != suffix {
			return nil, fmt.Errorf("unexpected shard directory %q in %s: the unit matrix has %d shard(s), so only %s1..%s%d may exist, spelt as a plain decimal "+
				"(the matrix grew or shrank without the reports job's -expect following it, or two artifacts name one shard)",
				e.Name(), dir, expect, shardArtifactPrefix, shardArtifactPrefix, expect)
		}
		seen[idx] = true
	}
	paths := make([]string, 0, expect)
	for i := 1; i <= expect; i++ {
		if !seen[i] {
			return nil, fmt.Errorf("shard %d of %d is missing: no %s%d directory in %s -- its unit job did not upload, so any merge would silently omit that shard's share of the suite",
				i, expect, shardArtifactPrefix, i, dir)
		}
		p := filepath.Join(dir, shardArtifactPrefix+strconv.Itoa(i), name)
		info, statErr := os.Stat(p)
		if statErr != nil {
			return nil, fmt.Errorf("shard %d of %d has no %s: %w", i, expect, name, statErr)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return nil, fmt.Errorf("shard %d of %d: %s is empty or not a regular file", i, expect, p)
		}
		paths = append(paths, p)
	}
	return paths, nil
}

func readAll(paths []string) ([][]byte, error) {
	out := make([][]byte, len(paths))
	for i, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// --- coverage ---------------------------------------------------------------

// coverBlock is one record of a Go coverage profile:
// name.go:startLine.startCol,endLine.endCol numStmt count.
type coverBlock struct {
	file                string
	startLine, startCol int
	endLine, endCol     int
	numStmt, count      int
}

var coverRecordRE = regexp.MustCompile(`^(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$`)

type coverageStats struct {
	shards, blocks, overlapping int
	mode                        string
}

func runCoverage(dir string, expect int, out string, stdout io.Writer) error {
	paths, err := shardFiles(dir, expect, "cover.out")
	if err != nil {
		return err
	}
	raws, err := readAll(paths)
	if err != nil {
		return err
	}
	merged, st, err := mergeCoverage(paths, raws)
	if err != nil {
		return err
	}
	if err := writeFile(out, merged); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "merged %d coverage profiles (mode %s): %d blocks, %d present in more than one shard -> %s\n",
		st.shards, st.mode, st.blocks, st.overlapping, out)
	return nil
}

// mergeCoverage merges Go coverage profiles into one. names labels each profile
// in error messages.
func mergeCoverage(names []string, profiles [][]byte) ([]byte, coverageStats, error) {
	var st coverageStats
	type blockKey struct {
		file                   string
		startLine, startCol    int
		endLine, endCol, nStmt int
	}
	blocks := map[blockKey]*coverBlock{}
	var order []blockKey
	for pi, raw := range profiles {
		name := names[pi]
		sc := bufio.NewScanner(bytes.NewReader(raw))
		sc.Buffer(make([]byte, 0, 1<<20), 1<<26)
		lineNo := 0
		sawMode := false
		for sc.Scan() {
			lineNo++
			line := strings.TrimRight(sc.Text(), "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			if !sawMode {
				mode, ok := strings.CutPrefix(line, "mode: ")
				if !ok {
					return nil, st, fmt.Errorf("%s:%d: first line must be a `mode:` header, got %q", name, lineNo, truncate(line))
				}
				switch mode {
				case "set", "count", "atomic":
				default:
					return nil, st, fmt.Errorf("%s:%d: unknown coverage mode %q", name, lineNo, mode)
				}
				if st.mode == "" {
					st.mode = mode
				} else if st.mode != mode {
					return nil, st, fmt.Errorf("%s:%d: coverage mode %q differs from the other shards' %q -- profiles in different modes cannot be summed", name, lineNo, mode, st.mode)
				}
				sawMode = true
				continue
			}
			m := coverRecordRE.FindStringSubmatch(line)
			if m == nil {
				return nil, st, fmt.Errorf("%s:%d: not a coverage record: %q", name, lineNo, truncate(line))
			}
			nums := make([]int, 6)
			for i := range nums {
				n, convErr := strconv.Atoi(m[i+2])
				if convErr != nil {
					return nil, st, fmt.Errorf("%s:%d: bad number in %q: %w", name, lineNo, truncate(line), convErr)
				}
				nums[i] = n
			}
			key := blockKey{m[1], nums[0], nums[1], nums[2], nums[3], nums[4]}
			count := nums[5]
			if existing, dup := blocks[key]; dup {
				st.overlapping++
				if st.mode == "set" {
					existing.count = max(existing.count, count)
				} else {
					existing.count += count
				}
				continue
			}
			blocks[key] = &coverBlock{file: key.file, startLine: key.startLine, startCol: key.startCol,
				endLine: key.endLine, endCol: key.endCol, numStmt: key.nStmt, count: count}
			order = append(order, key)
		}
		if err := sc.Err(); err != nil {
			return nil, st, fmt.Errorf("%s: %w", name, err)
		}
		if !sawMode {
			return nil, st, fmt.Errorf("%s: no `mode:` header -- the file is not a coverage profile", name)
		}
		st.shards++
	}
	if len(order) == 0 {
		return nil, st, errors.New("no coverage records in any shard -- a merged profile with nothing in it would read as 0% covered, not as a missing measurement")
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		switch {
		case a.file != b.file:
			return a.file < b.file
		case a.startLine != b.startLine:
			return a.startLine < b.startLine
		case a.startCol != b.startCol:
			return a.startCol < b.startCol
		case a.endLine != b.endLine:
			return a.endLine < b.endLine
		case a.endCol != b.endCol:
			return a.endCol < b.endCol
		}
		return a.nStmt < b.nStmt
	})
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "mode: %s\n", st.mode)
	for _, k := range order {
		b := blocks[k]
		fmt.Fprintf(&buf, "%s:%d.%d,%d.%d %d %d\n", b.file, b.startLine, b.startCol, b.endLine, b.endCol, b.numStmt, b.count)
	}
	st.blocks = len(order)
	return buf.Bytes(), st, nil
}

func truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// --- junit ------------------------------------------------------------------

type junitStats struct {
	shards, suites                   int
	tests, failures, errors, skipped int
	time                             float64
}

func runJUnit(dir string, expect int, out string, stdout io.Writer) error {
	paths, err := shardFiles(dir, expect, "junit.xml")
	if err != nil {
		return err
	}
	raws, err := readAll(paths)
	if err != nil {
		return err
	}
	merged, st, err := mergeJUnit(paths, raws)
	if err != nil {
		return err
	}
	if err := writeFile(out, merged); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "merged %d junit reports: %d suites, %d tests, %d failures, %d errors, %d skipped -> %s\n",
		st.shards, st.suites, st.tests, st.failures, st.errors, st.skipped, out)
	return nil
}

// What a <testsuites> root's totals must be, MEASURED against gotestsum v1.13.0
// on a run with passing, failing, panicking, skipped, build-failed and
// TestMain-failing packages (not assumed):
//
//	tests     the sum of the suites' tests attributes (also the number of
//	          real testcases; a synthetic per-package failure testcase is not
//	          counted, so tests can be LESS than the testcase elements)
//	failures  the number of <failure> elements in the whole document. NOT the
//	          sum of the suites' failures attributes: a package-level failure
//	          (a build failure, a TestMain exit) is a synthetic testcase under
//	          a suite whose own failures attribute says 0, while the root counts
//	          it. A literal "root = sum of suites" rule would refuse exactly the
//	          failing runs whose merged report matters most.
//	errors    an integer; it is 1 for a build failure with no <error> element
//	          anywhere, so it is required and summed but not checked against
//	          the suites.
//	skipped / disabled  optional (gotestsum writes skipped only on suites);
//	          when a root carries one it equals the sum of the suites'.
//	time      a number.
var (
	junitRequiredCounts = []string{"tests", "failures", "errors"}
	junitOptionalCounts = []string{"skipped", "disabled"}
)

// junitSuite is one direct <testsuite> child of a <testsuites> root: its name
// (empty for the nameless suite gotestsum writes for a build failure), its
// counters, and the exact bytes it was written with.
type junitSuite struct {
	name   string
	counts map[string]int
	raw    []byte
}

// junitDoc is one parsed shard report.
type junitDoc struct {
	rootCounts   map[string]int
	rootTime     float64
	suites       []junitSuite
	failureElems int // <failure> elements anywhere in the document
}

// mergeJUnit merges JUnit <testsuites> documents into one. Child <testsuite>
// elements are copied verbatim from the shard's own bytes. Every shard report
// must be internally consistent (see the table above) or the merge is refused:
// a merged total built on a root nobody checked would be a number that was
// never measured.
func mergeJUnit(names []string, docs [][]byte) ([]byte, junitStats, error) {
	var st junitStats
	rootTotals := map[string]int{}
	presentOpt := map[string]bool{}
	var timeTotal float64
	suiteOwner := map[string]string{}
	var children [][]byte
	for di, data := range docs {
		name := names[di]
		doc, err := parseJUnit(data)
		if err != nil {
			return nil, st, fmt.Errorf("%s: %w", name, err)
		}
		for _, a := range append(append([]string{}, junitRequiredCounts...), junitOptionalCounts...) {
			got, ok := doc.rootCounts[a]
			if !ok {
				continue
			}
			var want int
			switch a {
			case "errors":
				rootTotals[a] += got
				continue
			case "failures":
				want = doc.failureElems
			default:
				for _, s := range doc.suites {
					want += s.counts[a]
				}
			}
			if got != want {
				what := fmt.Sprintf("its %d suite(s) add up to %d", len(doc.suites), want)
				if a == "failures" {
					what = fmt.Sprintf("the document holds %d <failure> element(s)", want)
				}
				return nil, st, fmt.Errorf("%s: <testsuites %s=%d> but %s -- the report is truncated or hand-edited, and a merged total built on it would be wrong",
					name, a, got, what)
			}
			rootTotals[a] += got
			if a == "skipped" || a == "disabled" {
				presentOpt[a] = true
			}
		}
		timeTotal += doc.rootTime
		for _, s := range doc.suites {
			if s.name != "" {
				if prev, dup := suiteOwner[s.name]; dup {
					return nil, st, fmt.Errorf("test suite %q is in both %s and %s -- the shards overlap, so its tests ran twice", s.name, prev, name)
				}
				suiteOwner[s.name] = name
			}
			st.skipped += s.counts["skipped"]
			children = append(children, s.raw)
		}
		st.shards++
	}
	if len(children) == 0 {
		return nil, st, errors.New("no <testsuite> in any shard -- a merged report with no tests would read as a clean run, not as a missing measurement")
	}
	var buf bytes.Buffer
	buf.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<testsuites")
	for _, a := range junitRequiredCounts {
		fmt.Fprintf(&buf, " %s=\"%d\"", a, rootTotals[a])
	}
	for _, a := range junitOptionalCounts {
		if presentOpt[a] {
			fmt.Fprintf(&buf, " %s=\"%d\"", a, rootTotals[a])
		}
	}
	fmt.Fprintf(&buf, " time=\"%f\">\n", timeTotal)
	for _, c := range children {
		buf.WriteString("\t")
		buf.Write(c)
		buf.WriteString("\n")
	}
	buf.WriteString("</testsuites>\n")
	st.suites = len(children)
	st.tests, st.failures, st.errors = rootTotals["tests"], rootTotals["failures"], rootTotals["errors"]
	st.time = timeTotal
	return buf.Bytes(), st, nil
}

// countAttr reads a non-negative integer attribute. An absent attribute is
// (0, false, nil); a present one that is not a canonical integer is an error.
func countAttr(attrs []xml.Attr, name string) (n int, present bool, err error) {
	for _, a := range attrs {
		if a.Name.Local != name {
			continue
		}
		n, convErr := strconv.Atoi(a.Value)
		if convErr != nil || n < 0 || strconv.Itoa(n) != a.Value {
			return 0, true, fmt.Errorf("%s=%q is not a non-negative integer", name, a.Value)
		}
		return n, true, nil
	}
	return 0, false, nil
}

// parseJUnit parses one JUnit document whose single root is <testsuites>. It
// fails closed on everything a well-formed gotestsum report never contains: a
// second root or trailing content, a root without tests/failures/errors/time,
// a counter that is not an integer, a suite with no name attribute (an EMPTY
// name is legal: gotestsum writes one for a build failure).
func parseJUnit(data []byte) (junitDoc, error) {
	doc := junitDoc{rootCounts: map[string]int{}}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var (
		depth     int
		rootSeen  bool
		rootDone  bool
		sawTime   bool
		kidStart  int64
		kidName   string
		kidCounts map[string]int
	)
	for {
		before := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return doc, fmt.Errorf("malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if rootDone {
				return doc, fmt.Errorf("more than one root element: a second <%s> follows the closed <testsuites> -- two documents were concatenated, and their totals would silently overwrite each other", t.Name.Local)
			}
			depth++
			if t.Name.Local == "failure" && depth >= 4 {
				doc.failureElems++
			}
			switch depth {
			case 1:
				if t.Name.Local != "testsuites" {
					return doc, fmt.Errorf("root element is <%s>, want <testsuites>", t.Name.Local)
				}
				rootSeen = true
				for _, a := range append(append([]string{}, junitRequiredCounts...), junitOptionalCounts...) {
					n, ok, cerr := countAttr(t.Attr, a)
					if cerr != nil {
						return doc, fmt.Errorf("<testsuites %w", cerr)
					}
					if ok {
						doc.rootCounts[a] = n
					}
				}
				for _, a := range junitRequiredCounts {
					if _, ok := doc.rootCounts[a]; !ok {
						return doc, fmt.Errorf("<testsuites> has no %s attribute -- its total cannot be checked against its suites", a)
					}
				}
				for _, a := range t.Attr {
					if a.Name.Local == "time" {
						f, ferr := strconv.ParseFloat(a.Value, 64)
						if ferr != nil || f < 0 {
							return doc, fmt.Errorf("<testsuites time=%q> is not a non-negative number", a.Value)
						}
						doc.rootTime, sawTime = f, true
					}
				}
				if !sawTime {
					return doc, errors.New("<testsuites> has no time attribute")
				}
			case 2:
				if t.Name.Local != "testsuite" {
					return doc, fmt.Errorf("unexpected <%s> directly under <testsuites>", t.Name.Local)
				}
				kidStart, kidName, kidCounts = before, "", map[string]int{}
				hasName := false
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						kidName, hasName = a.Value, true
					}
				}
				if !hasName {
					return doc, errors.New("a <testsuite> has no name attribute")
				}
				for _, a := range append(append([]string{}, junitRequiredCounts...), junitOptionalCounts...) {
					n, ok, cerr := countAttr(t.Attr, a)
					if cerr != nil {
						return doc, fmt.Errorf("<testsuite name=%q> %w", kidName, cerr)
					}
					if ok {
						kidCounts[a] = n
					}
				}
				if _, ok := kidCounts["tests"]; !ok {
					return doc, fmt.Errorf("<testsuite name=%q> has no tests attribute", kidName)
				}
			}
		case xml.EndElement:
			if depth == 2 {
				doc.suites = append(doc.suites, junitSuite{name: kidName, counts: kidCounts, raw: bytes.Clone(data[kidStart:dec.InputOffset()])})
			}
			depth--
			if depth == 0 {
				rootDone = true
			}
		case xml.CharData:
			if rootDone && len(bytes.TrimSpace(t)) > 0 {
				return doc, errors.New("content after the closing </testsuites>")
			}
		}
	}
	if !rootSeen {
		return doc, errors.New("no <testsuites> root element")
	}
	return doc, nil
}

// --- failures ---------------------------------------------------------------

// testActions is the CLOSED set of Action values a go-test.json line may carry
// (a value outside it is an error, never silently dropped: a failure event
// carrying an Action this tool does not know is exactly the one that vanishes
// from the listing). Sources, both read from the Go toolchain this repo builds
// with (go1.27.0), and observed on a real gotestsum v1.13.0 run:
//   - test2json's own vocabulary, $GOROOT/src/cmd/internal/test2json/test2json.go
//     (the Event.Action values it writes): start, run, pause, cont, pass, bench,
//     fail, output, skip, plus `attr` and `artifacts` (Go 1.25+, written for
//     t.Attr and t.ArtifactDir; see the `case "artifacts"` / `case "attr"` in
//     that file's line handler);
//   - `go test -json`'s build events, documented in `go help buildjson`
//     ($GOROOT/src/cmd/go/internal/help/helpdoc.go, "build-output" and
//     "build-fail"; emitted by $GOROOT/src/cmd/go/internal/load/printer.go).
//     A package that fails to build writes these AND a package `fail` event, so
//     they carry no failure of their own; gotestsum writes them to
//     go-test.json unchanged (see testdata/gotestsum-failing-run/go-test.json).
var testActions = map[string]bool{
	"start": true, "run": true, "pause": true, "cont": true, "pass": true, "bench": true,
	"fail": true, "output": true, "skip": true, "attr": true, "artifacts": true,
	"build-output": true, "build-fail": true,
}

// testEvent is the subset of a test2json event this tool reads.
type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

// failureEntry is one failed test (Package + Test) or one failed package with
// no test named (Test == ""), with the shards that saw it fail.
type failureEntry struct {
	Package string `json:"package"`
	Test    string `json:"test,omitempty"`
	Shards  []int  `json:"shards"`
}

// failKey is a failed test's IDENTITY: the (package, test) pair itself. It is
// a struct on purpose. A `package + "/" + test` string is not unique -- the pair
// (example.org/m, TestX/TestY) and the pair (example.org/m/TestX, TestY) are two
// different failures with one joined name -- and any map keyed by it collapses
// them (CHAOS-3895 r3 P1, executed). The joined form exists only for display.
type failKey struct{ Package, Test string }

func (k failKey) String() string {
	if k.Test == "" {
		return k.Package
	}
	return k.Package + "/" + k.Test
}

func (e failureEntry) id() failKey { return failKey{e.Package, e.Test} }

// key is the display name of the entry (see failKey: never an identity).
func (e failureEntry) key() string { return e.id().String() }

// failureReport is what the `failures` command found, and what `crosscheck`
// reads back from -report.
type failureReport struct {
	Shards   int            `json:"shards"`
	PerShard []shardEvents  `json:"per_shard"`
	Failed   []failureEntry `json:"failed"`
}

type shardEvents struct {
	Shard  int `json:"shard"`
	Events int `json:"events"`
	Failed int `json:"failed"`
}

func runFailures(dir string, expect int, summary, report string, stdout io.Writer) error {
	paths, err := shardFiles(dir, expect, "go-test.json")
	if err != nil {
		return err
	}
	raws, err := readAll(paths)
	if err != nil {
		return err
	}
	rep, err := collectFailures(raws)
	if err != nil {
		return err
	}
	for _, s := range rep.PerShard {
		fmt.Fprintf(stdout, "shard %d: %d test events, %d failed test(s)/package(s)\n", s.Shard, s.Events, s.Failed)
	}
	for _, e := range rep.Failed {
		fmt.Fprintf(stdout, "FAIL %s (shard %s)\n", e.key(), joinInts(e.Shards))
	}
	fmt.Fprintf(stdout, "%d failed test(s)/package(s) across %d shard(s)\n", len(rep.Failed), rep.Shards)
	if report != "" {
		b, mErr := json.MarshalIndent(rep, "", "  ")
		if mErr != nil {
			return mErr
		}
		if err := writeFile(report, append(b, '\n')); err != nil {
			return err
		}
	}
	if summary != "" {
		var md strings.Builder
		fmt.Fprintf(&md, "### unit: %d failed test(s)/package(s) across %d shards\n\n", len(rep.Failed), rep.Shards)
		for _, e := range rep.Failed {
			fmt.Fprintf(&md, "- `%s` (shard %s)\n", e.key(), joinInts(e.Shards))
		}
		f, err := os.OpenFile(summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, werr := f.WriteString(md.String())
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	return nil
}

// collectFailures reads every shard's test2json stream. It fails closed: a line
// that is not an event, an event with no Action, or an Action outside
// testActions is an error, not a note -- the one line that would have said which
// test failed could be exactly the one that is mangled or new.
func collectFailures(streams [][]byte) (failureReport, error) {
	rep := failureReport{}
	byKey := map[failKey]*failureEntry{}
	for i, raw := range streams {
		shard := i + 1
		se := shardEvents{Shard: shard}
		seen := map[failKey]bool{}
		r := bufio.NewReaderSize(bytes.NewReader(raw), 1<<20)
		lineNo := 0
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				lineNo++
			}
			if len(bytes.TrimSpace(line)) > 0 {
				var ev testEvent
				if jerr := json.Unmarshal(line, &ev); jerr != nil || ev.Action == "" {
					return rep, fmt.Errorf("shard %d: go-test.json line %d is not a test2json event (%q) -- a failure event could be the mangled line, so the listing would silently omit it",
						shard, lineNo, truncate(strings.TrimSpace(string(line))))
				}
				if !testActions[ev.Action] {
					return rep, fmt.Errorf("shard %d: go-test.json line %d has Action %q, which is not a known test2json/go-test action -- an unknown action could be carrying a failure, so the listing would silently omit it",
						shard, lineNo, ev.Action)
				}
				se.Events++
				if ev.Action == "fail" && ev.Package != "" {
					k := failKey{ev.Package, ev.Test}
					if !seen[k] {
						seen[k] = true
						if byKey[k] == nil {
							byKey[k] = &failureEntry{Package: ev.Package, Test: ev.Test}
						}
						byKey[k].Shards = append(byKey[k].Shards, shard)
						se.Failed++
					}
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return rep, err
			}
		}
		if se.Events == 0 {
			return rep, fmt.Errorf("shard %d: go-test.json holds no test2json events -- the failure analysis for this shard did not happen", shard)
		}
		rep.PerShard = append(rep.PerShard, se)
	}
	rep.Shards = len(rep.PerShard)
	keys := make([]failKey, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Package != keys[j].Package {
			return keys[i].Package < keys[j].Package
		}
		return keys[i].Test < keys[j].Test
	})
	for _, k := range keys {
		rep.Failed = append(rep.Failed, *byKey[k])
	}
	return rep, nil
}

func joinInts(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// --- crosscheck -------------------------------------------------------------

// crosscheckInputs names the merged outputs the reports job produced.
type crosscheckInputs struct {
	dir                               string
	expect                            int
	junit, cover, cobertura, failures string
}

// countElements counts start elements with the given local name, with a
// decoder of its own: it shares no code with parseJUnit, so a defect in the
// merge's parser cannot make both sides of the comparison wrong the same way.
func countElements(data []byte, local string) (int, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	n := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == local {
			n++
		}
	}
}

// failedSet is the normalised set of failures, as structured (package, test)
// identities (see failKey; a package-level failure has an empty Test). A
// package-level failure is dropped when a test in that package failed
// too (the package fails BECAUSE its test did, and JUnit records only the
// test); what remains at package level is a package that failed with no failed
// test -- a build failure, a TestMain exit -- which JUnit records as a
// synthetic testcase with no classname under that package's suite.
type failedSet map[failKey]bool

func normalizeFailed(pairs [][2]string) failedSet {
	withTest := map[string]bool{}
	for _, p := range pairs {
		if p[1] != "" {
			withTest[p[0]] = true
		}
	}
	out := failedSet{}
	for _, p := range pairs {
		switch {
		case p[1] != "":
			out[failKey{p[0], p[1]}] = true
		case !withTest[p[0]]:
			out[failKey{p[0], ""}] = true
		}
	}
	return out
}

// rawFailedPairs re-derives the failed (package, test) pairs from one raw
// go-test.json with a decoder of its own (a generic map, not testEvent) and its
// own check of the closed Action set.
func rawFailedPairs(raw []byte, shard int) ([][2]string, error) {
	var out [][2]string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<26)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("shard %d go-test.json line %d: %w", shard, n, err)
		}
		action, _ := ev["Action"].(string)
		if !testActions[action] {
			return nil, fmt.Errorf("shard %d go-test.json line %d: Action %q is not a known action", shard, n, action)
		}
		if action != "fail" {
			continue
		}
		pkg, _ := ev["Package"].(string)
		test, _ := ev["Test"].(string)
		if pkg != "" {
			out = append(out, [2]string{pkg, test})
		}
	}
	return out, sc.Err()
}

// junitFailedPairs re-derives the failed (package, test) pairs from a JUnit
// document with a token loop of its own: every testcase with a <failure>
// child (the element gotestsum writes; it writes no <error>). A testcase with no classname is gotestsum's synthetic
// per-package failure and stands for the enclosing suite's package.
func junitFailedPairs(data []byte) ([][2]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var (
		out       [][2]string
		suite     string
		classname string
		name      string
		inCase    bool
		failed    bool
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "testsuite":
				suite = ""
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						suite = a.Value
					}
				}
			case "testcase":
				inCase, failed, classname, name = true, false, "", ""
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "classname":
						classname = a.Value
					case "name":
						name = a.Value
					}
				}
			case "failure":
				if inCase {
					failed = true
				}
			}
		case xml.EndElement:
			if t.Name.Local == "testcase" && inCase {
				if failed {
					if classname == "" {
						out = append(out, [2]string{suite, ""})
					} else {
						out = append(out, [2]string{classname, name})
					}
				}
				inCase = false
			}
		}
	}
}

// diffSets lists, for display, the failures only in a and only in b (the
// comparison itself is on the structured keys).
func diffSets(a, b failedSet) (onlyA, onlyB []string) {
	for k := range a {
		if !b[k] {
			onlyA = append(onlyA, k.String())
		}
	}
	for k := range b {
		if !a[k] {
			onlyB = append(onlyB, k.String())
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return
}

// coberturaDoc is what a WHOLE Cobertura document states and holds.
type coberturaDoc struct {
	rootValid, rootCovered int             // the <coverage> root's counters
	valid, covered         int             // recomputed from the class-level <line> elements
	files                  map[string]bool // every class's filename
	fileHit                map[string]bool // filename -> some class-level line of it has hits > 0
}

// parseCobertura decodes a Cobertura document to its END: a truncated or
// malformed document is an error, never a document whose opening tag was
// enough. It recomputes the line counts itself from the class-level
// <line number hits> elements -- lines under <class><lines>, NOT the method
// lines, which repeat them (measured on the real report of run 36729729427:
// 212314 <line> elements, 106157 of them class-level, == the root's
// lines-valid) -- so the root counters are a claim to check, not a source.
func parseCobertura(data []byte) (coberturaDoc, error) {
	doc := coberturaDoc{files: map[string]bool{}, fileHit: map[string]bool{}}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var (
		path      []string
		rootSeen  bool
		rootDone  bool
		curFile   string
		rootValid = -1
		rootCov   = -1
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return doc, fmt.Errorf("malformed or truncated XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if rootDone {
				return doc, fmt.Errorf("more than one root element: a second <%s> follows the closed <coverage>", t.Name.Local)
			}
			path = append(path, t.Name.Local)
			switch {
			case len(path) == 1:
				if t.Name.Local != "coverage" {
					return doc, fmt.Errorf("root element is <%s>, want <coverage>", t.Name.Local)
				}
				rootSeen = true
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "lines-valid":
						if n, cerr := strconv.Atoi(a.Value); cerr == nil && n >= 0 {
							rootValid = n
						}
					case "lines-covered":
						if n, cerr := strconv.Atoi(a.Value); cerr == nil && n >= 0 {
							rootCov = n
						}
					}
				}
				if rootValid < 0 || rootCov < 0 {
					return doc, errors.New("<coverage> has no non-negative integer lines-valid / lines-covered")
				}
				doc.rootValid, doc.rootCovered = rootValid, rootCov
			case t.Name.Local == "class":
				curFile = ""
				for _, a := range t.Attr {
					if a.Name.Local == "filename" {
						curFile = a.Value
					}
				}
				if curFile == "" {
					return doc, errors.New("a <class> has no filename attribute")
				}
				doc.files[curFile] = true
			case t.Name.Local == "line" && len(path) >= 3 && path[len(path)-2] == "lines" && path[len(path)-3] == "class":
				hits := -1
				hasNumber := false
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "number":
						hasNumber = true
					case "hits":
						if n, cerr := strconv.Atoi(a.Value); cerr == nil && n >= 0 {
							hits = n
						}
					}
				}
				if !hasNumber || hits < 0 {
					return doc, fmt.Errorf("a class-level <line> of %s has no number or no non-negative integer hits", curFile)
				}
				doc.valid++
				if hits > 0 {
					doc.covered++
					doc.fileHit[curFile] = true
				}
			}
		case xml.EndElement:
			path = path[:len(path)-1]
			if len(path) == 0 {
				rootDone = true
			}
		case xml.CharData:
			if rootDone && len(bytes.TrimSpace(t)) > 0 {
				return doc, errors.New("content after the closing </coverage>")
			}
		}
	}
	if !rootSeen {
		return doc, errors.New("no <coverage> root element")
	}
	return doc, nil
}

// profileFileHits reads which source files a coverage profile holds and whether
// any of a file's blocks was hit. A profile is what `mergeCoverage` wrote, so a
// line that is not a record is an error.
func profileFileHits(profile []byte) (map[string]bool, error) {
	out := map[string]bool{}
	for n, line := range strings.Split(string(profile), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "mode: ") {
			continue
		}
		m := coverRecordRE.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("profile line %d is not a coverage record", n+1)
		}
		count, err := strconv.Atoi(m[7])
		if err != nil {
			return nil, err
		}
		out[m[1]] = out[m[1]] || count > 0
	}
	return out, nil
}

// checkCobertura is the Cobertura half of crosscheck: the document must decode
// whole; its root counters must equal what its own <line> elements say; and it
// must agree with the MERGED PROFILE it was built from on what can be compared
// exactly. Exact equality of the line counts with counts derived from the
// profile is NOT derivable (gocover-cobertura maps whole function ranges by AST
// and drops class-less files: on run 36729729427 the profile has 106275 distinct
// lines, 106208 of them in files that have a class, against the report's
// 106157), so the profile link is what holds exactly on real data: every class
// is a file of the profile, and per file "some line was hit" is the same in both
// (0 disagreements over the real run's 824 classed files).
func checkCobertura(doc coberturaDoc, profile map[string]bool, path string, bad func(string, ...any)) {
	if doc.rootValid != doc.valid {
		bad("cobertura report %s states lines-valid=%d but its class-level <line> elements number %d", path, doc.rootValid, doc.valid)
	}
	if doc.rootCovered != doc.covered {
		bad("cobertura report %s states lines-covered=%d but %d of its class-level <line> elements have hits", path, doc.rootCovered, doc.covered)
	}
	if doc.valid < 1 {
		bad("cobertura report %s holds no class-level lines", path)
	}
	var orphan, disagree []string
	for cf := range doc.files {
		var match string
		for pf := range profile {
			if pf == cf || strings.HasSuffix(pf, "/"+cf) {
				match = pf
				break
			}
		}
		switch {
		case match == "":
			orphan = append(orphan, cf)
		case profile[match] != doc.fileHit[cf]:
			disagree = append(disagree, cf)
		}
	}
	sort.Strings(orphan)
	sort.Strings(disagree)
	if len(orphan) > 0 {
		bad("cobertura report %s has class file(s) that are not in the merged profile: %v", path, head(orphan))
	}
	if len(disagree) > 0 {
		bad("cobertura report %s disagrees with the merged profile on whether file(s) were hit: %v", path, head(disagree))
	}
}

// runCrosscheck is the reports job's BEHAVIOUR check. Everything else in the
// job is a step that could have been disabled, skipped by a condition, or
// replaced with `true` while the job still reads green; this recomputes the
// answer from the raw shard inputs and compares it with the merged files the
// job actually wrote, so a merge that did not run, ran on fewer shards, or
// produced something other than the sum fails here.
//
// The failure listing is compared THREE ways: the failed set re-derived from
// every shard's raw go-test.json, the failed set re-derived from the merged
// JUnit, and the listing the `failures` command wrote must all be equal.
func runCrosscheck(in crosscheckInputs, stdout io.Writer) error {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	// JUnit: element counts, counted independently of the merge, must be the
	// same in the merged file as across the shards, and the merged root must
	// state the shards' totals.
	jPaths, err := shardFiles(in.dir, in.expect, "junit.xml")
	if err != nil {
		return err
	}
	jRaws, err := readAll(jPaths)
	if err != nil {
		return err
	}
	var wantCases, wantSuites, wantFailElems, wantTests int
	for i, raw := range jRaws {
		c, cErr := countElements(raw, "testcase")
		s, sErr := countElements(raw, "testsuite")
		doc, pErr := parseJUnit(raw)
		if err := errors.Join(cErr, sErr, pErr); err != nil {
			return fmt.Errorf("shard %d junit.xml: %w", i+1, err)
		}
		wantCases += c
		wantSuites += s
		wantFailElems += doc.failureElems
		wantTests += doc.rootCounts["tests"]
	}
	mergedJ, err := os.ReadFile(in.junit)
	if err != nil {
		return fmt.Errorf("merged junit %s was not produced: %w", in.junit, err)
	}
	gotCases, cErr := countElements(mergedJ, "testcase")
	gotSuites, sErr := countElements(mergedJ, "testsuite")
	mDoc, pErr := parseJUnit(mergedJ)
	if err := errors.Join(cErr, sErr, pErr); err != nil {
		return fmt.Errorf("merged junit %s: %w", in.junit, err)
	}
	if gotCases != wantCases {
		bad("merged junit has %d testcase(s), the %d shards have %d", gotCases, in.expect, wantCases)
	}
	if gotSuites != wantSuites {
		bad("merged junit has %d suite(s), the %d shards have %d", gotSuites, in.expect, wantSuites)
	}
	if mDoc.rootCounts["tests"] != wantTests {
		bad("merged junit root says tests=%d but the shards state %d", mDoc.rootCounts["tests"], wantTests)
	}
	if mDoc.rootCounts["failures"] != wantFailElems {
		bad("merged junit root says failures=%d but the shards hold %d <failure> element(s)", mDoc.rootCounts["failures"], wantFailElems)
	}

	// Failures: raw go-test.json of every shard == merged JUnit == the listing.
	tPaths, err := shardFiles(in.dir, in.expect, "go-test.json")
	if err != nil {
		return err
	}
	tRaws, err := readAll(tPaths)
	if err != nil {
		return err
	}
	var rawPairs [][2]string
	for i, raw := range tRaws {
		p, rErr := rawFailedPairs(raw, i+1)
		if rErr != nil {
			return rErr
		}
		rawPairs = append(rawPairs, p...)
	}
	fromRaw := normalizeFailed(rawPairs)
	jPairs, err := junitFailedPairs(mergedJ)
	if err != nil {
		return fmt.Errorf("merged junit %s: %w", in.junit, err)
	}
	fromJUnit := normalizeFailed(jPairs)
	fb, err := os.ReadFile(in.failures)
	if err != nil {
		return fmt.Errorf("failure report %s was not produced: %w", in.failures, err)
	}
	var rep failureReport
	if err := json.Unmarshal(fb, &rep); err != nil {
		return fmt.Errorf("failure report %s: %w", in.failures, err)
	}
	var listed [][2]string
	for _, e := range rep.Failed {
		listed = append(listed, [2]string{e.Package, e.Test})
	}
	fromListing := normalizeFailed(listed)
	if a, b := diffSets(fromRaw, fromListing); len(a)+len(b) > 0 {
		bad("the failure listing differs from the raw go-test.json of the shards: only in the raw events %v, only in the listing %v", head(a), head(b))
	}
	if a, b := diffSets(fromRaw, fromJUnit); len(a)+len(b) > 0 {
		bad("the merged JUnit differs from the raw go-test.json of the shards: failed only in go-test.json %v, only in the JUnit %v", head(a), head(b))
	}
	if rep.Shards != in.expect || len(rep.PerShard) != in.expect {
		bad("the failure listing read %d shard(s) (%d entries), want %d", rep.Shards, len(rep.PerShard), in.expect)
	}
	for _, s := range rep.PerShard {
		if s.Events < 1 {
			bad("the failure listing read no events from shard %d", s.Shard)
		}
	}

	// Coverage: the merged profile must be exactly what merging the shards now
	// gives, and the Cobertura built from it must state some covered code.
	cPaths, err := shardFiles(in.dir, in.expect, "cover.out")
	if err != nil {
		return err
	}
	cRaws, err := readAll(cPaths)
	if err != nil {
		return err
	}
	fresh, _, err := mergeCoverage(cPaths, cRaws)
	if err != nil {
		return err
	}
	mergedC, err := os.ReadFile(in.cover)
	if err != nil {
		return fmt.Errorf("merged coverage profile %s was not produced: %w", in.cover, err)
	}
	if !bytes.Equal(mergedC, fresh) {
		bad("merged coverage profile %s is not the merge of the %d shards' profiles", in.cover, in.expect)
	}
	cob, err := os.ReadFile(in.cobertura)
	if err != nil {
		return fmt.Errorf("cobertura report %s was not produced: %w", in.cobertura, err)
	}
	cDoc, cbErr := parseCobertura(cob)
	if cbErr != nil {
		bad("cobertura report %s: %v", in.cobertura, cbErr)
	} else {
		pHits, pErr := profileFileHits(mergedC)
		if pErr != nil {
			return fmt.Errorf("merged coverage profile %s: %w", in.cover, pErr)
		}
		checkCobertura(cDoc, pHits, in.cobertura, bad)
	}

	if len(problems) > 0 {
		return fmt.Errorf("the merged reports do not match the shards:\n  - %s", strings.Join(problems, "\n  - "))
	}
	fmt.Fprintf(stdout, "crosscheck OK: %d shards, %d testcases in %d suites, %d failure element(s), %d failed test(s)/package(s) agreed by go-test.json, JUnit and the listing, coverage %d/%d lines (its own <line> elements and the merged profile agree), failure listing read %d shards\n",
		in.expect, wantCases, wantSuites, wantFailElems, len(fromRaw), cDoc.covered, cDoc.valid, rep.Shards)
	return nil
}

// head trims a list to its first few entries for a readable message.
func head(v []string) []string {
	if len(v) > 5 {
		return append(append([]string{}, v[:5]...), fmt.Sprintf("... (%d more)", len(v)-5))
	}
	return v
}
