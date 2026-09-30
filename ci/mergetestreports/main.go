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
// produced something other than the sum fails there.
//
// The tool fails closed on all malformed input, never guessing at it. A missing
// shard, an extra shard, a shard directory whose name is not the canonical
// decimal index (1, not 01), an empty file, a malformed profile, two shards
// that both claim one test suite, two profiles in different coverage modes, a
// go-test.json line that is not a test2json event, a JUnit root whose totals
// are absent, non-integer, or not the sum of its suites, or a JUnit file with
// more than one root, is an error and not a partial merge: a merged report that
// silently covers 3 of 4 shards, or states a total nobody measured, reads
// exactly like a correct one, which is the failure this whole job exists to
// prevent.
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
			return nil, fmt.Errorf("shard %d of %d is missing: no %s%d directory in %s -- its unit job did not upload, so any merge would silently omit a quarter of the suite",
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

// junitCountAttrs are the counters that must add up: a <testsuites> root states
// each as a total, and it has to equal the sum over the suites it contains.
// tests, failures and errors are always present on a gotestsum root; skipped
// and disabled only when the writer emits them (gotestsum puts `skipped` on the
// suites and not on the root, so a root without it is complete, not malformed).
var (
	junitRequiredCounts = []string{"tests", "failures", "errors"}
	junitOptionalCounts = []string{"skipped", "disabled"}
)

// junitSuite is one direct <testsuite> child of a <testsuites> root: its name,
// its counters, and the exact bytes it was written with.
type junitSuite struct {
	name   string
	counts map[string]int
	raw    []byte
}

// junitDoc is one parsed shard report.
type junitDoc struct {
	rootCounts map[string]int
	rootTime   float64
	suites     []junitSuite
}

// mergeJUnit merges JUnit <testsuites> documents into one. Child <testsuite>
// elements are copied verbatim from the shard's own bytes. Every shard report
// must be internally consistent -- root totals present, integers, and equal to
// the sum over its own suites -- or the merge is refused: a merged total built
// on a root nobody checked would be a number that was never measured.
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
			want, ok := doc.rootCounts[a]
			if !ok {
				continue
			}
			sum := 0
			for _, s := range doc.suites {
				sum += s.counts[a]
			}
			if want != sum {
				return nil, st, fmt.Errorf("%s: <testsuites %s=%d> but its %d suite(s) add up to %d -- the report is truncated or hand-edited, and a merged total built on it would be wrong",
					name, a, want, len(doc.suites), sum)
			}
			rootTotals[a] += want
			if a == "skipped" || a == "disabled" {
				presentOpt[a] = true
			}
		}
		timeTotal += doc.rootTime
		for _, s := range doc.suites {
			if prev, dup := suiteOwner[s.name]; dup {
				return nil, st, fmt.Errorf("test suite %q is in both %s and %s -- the shards overlap, so its tests ran twice", s.name, prev, name)
			}
			suiteOwner[s.name] = name
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
// a counter that is not an integer, a suite without a name.
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
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						kidName = a.Value
					}
				}
				if kidName == "" {
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

// testEvent is the subset of a test2json event this tool reads.
type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

// failureReport is what the `failures` command found, and what `crosscheck`
// reads back from -report to prove the listing really covered every shard.
type failureReport struct {
	Shards   int           `json:"shards"`
	PerShard []shardEvents `json:"per_shard"`
	// Failed maps "package" or "package/Test" to the shard indices that saw it fail.
	Failed map[string][]int `json:"failed"`
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
	keys := make([]string, 0, len(rep.Failed))
	for k := range rep.Failed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, s := range rep.PerShard {
		fmt.Fprintf(stdout, "shard %d: %d test events, %d failed test(s)/package(s)\n", s.Shard, s.Events, s.Failed)
	}
	for _, k := range keys {
		fmt.Fprintf(stdout, "FAIL %s (shard %s)\n", k, joinInts(rep.Failed[k]))
	}
	fmt.Fprintf(stdout, "%d failed test(s)/package(s) across %d shard(s)\n", len(keys), rep.Shards)
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
		fmt.Fprintf(&md, "### unit: %d failed test(s)/package(s) across %d shards\n\n", len(keys), rep.Shards)
		for _, k := range keys {
			fmt.Fprintf(&md, "- `%s` (shard %s)\n", k, joinInts(rep.Failed[k]))
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
// that is not a test2json event -- the one line that would have said which test
// failed could be exactly the one that is mangled -- is an error, not a note.
func collectFailures(streams [][]byte) (failureReport, error) {
	rep := failureReport{Failed: map[string][]int{}}
	for i, raw := range streams {
		shard := i + 1
		se := shardEvents{Shard: shard}
		seen := map[string]bool{}
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
				se.Events++
				if ev.Action == "fail" {
					key := ev.Package
					if ev.Test != "" {
						key += "/" + ev.Test
					}
					if key != "" && !seen[key] {
						seen[key] = true
						rep.Failed[key] = append(rep.Failed[key], shard)
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

// coberturaFacts reads the two counters off a Cobertura <coverage> root.
func coberturaFacts(data []byte) (valid, covered int, err error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, tErr := dec.Token()
		if tErr == io.EOF {
			return 0, 0, errors.New("no <coverage> root element")
		}
		if tErr != nil {
			return 0, 0, tErr
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "coverage" {
			return 0, 0, fmt.Errorf("root element is <%s>, want <coverage>", se.Name.Local)
		}
		var vOK, cOK bool
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "lines-valid":
				valid, err = strconv.Atoi(a.Value)
				vOK = err == nil
			case "lines-covered":
				covered, err = strconv.Atoi(a.Value)
				cOK = err == nil
			}
		}
		if !vOK || !cOK {
			return 0, 0, errors.New("<coverage> has no integer lines-valid / lines-covered")
		}
		return valid, covered, nil
	}
}

// runCrosscheck is the reports job's BEHAVIOUR check. Everything else in the
// job is a step that could have been disabled, skipped by a condition, or
// replaced with `true` while the job still reads green; this recomputes the
// answer from the raw shard inputs and compares it with the merged files the
// job actually wrote, so a merge that did not run, ran on fewer shards, or
// produced something other than the sum fails here.
func runCrosscheck(in crosscheckInputs, stdout io.Writer) error {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	// JUnit: testcase and suite element counts, counted independently of the
	// merge, must be the same in the merged file as across the shards, and the
	// merged root must state them.
	jPaths, err := shardFiles(in.dir, in.expect, "junit.xml")
	if err != nil {
		return err
	}
	jRaws, err := readAll(jPaths)
	if err != nil {
		return err
	}
	var wantCases, wantSuites, wantFailures int
	for i, raw := range jRaws {
		c, cErr := countElements(raw, "testcase")
		s, sErr := countElements(raw, "testsuite")
		doc, pErr := parseJUnit(raw)
		if err := errors.Join(cErr, sErr, pErr); err != nil {
			return fmt.Errorf("shard %d junit.xml: %w", i+1, err)
		}
		wantCases += c
		wantSuites += s
		wantFailures += doc.rootCounts["failures"]
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
	if mDoc.rootCounts["tests"] != wantCases {
		bad("merged junit root says tests=%d but the shards hold %d testcase(s)", mDoc.rootCounts["tests"], wantCases)
	}
	if mDoc.rootCounts["failures"] != wantFailures {
		bad("merged junit root says failures=%d but the shards report %d", mDoc.rootCounts["failures"], wantFailures)
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
	valid, covered, cbErr := coberturaFacts(cob)
	switch {
	case cbErr != nil:
		bad("cobertura report %s: %v", in.cobertura, cbErr)
	case valid < 1 || covered < 0 || covered > valid:
		bad("cobertura report %s states lines-covered=%d of lines-valid=%d", in.cobertura, covered, valid)
	}

	// Failure listing: it must exist and must have read every shard.
	fb, err := os.ReadFile(in.failures)
	if err != nil {
		return fmt.Errorf("failure report %s was not produced: %w", in.failures, err)
	}
	var rep failureReport
	if err := json.Unmarshal(fb, &rep); err != nil {
		return fmt.Errorf("failure report %s: %w", in.failures, err)
	}
	if rep.Shards != in.expect || len(rep.PerShard) != in.expect {
		bad("the failure listing read %d shard(s) (%d entries), want %d", rep.Shards, len(rep.PerShard), in.expect)
	}
	for _, s := range rep.PerShard {
		if s.Events < 1 {
			bad("the failure listing read no events from shard %d", s.Shard)
		}
	}
	if wantFailures > 0 && len(rep.Failed) == 0 {
		bad("the shards report %d failure(s) in JUnit but the failure listing names none", wantFailures)
	}

	if len(problems) > 0 {
		return fmt.Errorf("the merged reports do not match the shards:\n  - %s", strings.Join(problems, "\n  - "))
	}
	fmt.Fprintf(stdout, "crosscheck OK: %d shards, %d testcases in %d suites, %d failure(s), coverage %d/%d lines, failure listing read %d shards\n",
		in.expect, wantCases, wantSuites, wantFailures, covered, valid, rep.Shards)
	return nil
}
