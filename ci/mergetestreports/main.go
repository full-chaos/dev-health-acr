// Command mergetestreports merges the per-shard reports of CI's sharded `unit`
// job (CHAOS-3895) back into the single coverage and JUnit reports the
// published `go-coverage` and `go-junit` artifacts have always been.
//
// Each `unit` shard uploads one intermediate artifact, go-unit-shard-<i>,
// holding that shard's cover.out (Go coverage profile), junit.xml (gotestsum
// JUnit) and go-test.json (gotestsum's test2json stream). The `reports` job
// downloads all of them into one directory and runs this tool three times:
//
//	mergetestreports coverage -dir D -expect N -out merged/cover.out
//	mergetestreports junit    -dir D -expect N -out merged/junit.xml
//	mergetestreports failures -dir D -expect N [-summary FILE]
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
// rather than the first failure of whichever shard the reader opened.
//
// The tool never guesses at absent input. A missing shard, an extra shard, an
// empty file, a malformed profile, two shards that both claim one test suite,
// or two profiles in different coverage modes is an error, not a partial
// merge: a merged report that silently covers 3 of 4 shards reads exactly like
// a complete one, which is the failure this whole job exists to prevent.
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
		fmt.Fprintln(stderr, "usage: mergetestreports coverage|junit|failures -dir DIR -expect N [-out FILE] [-summary FILE]")
		return 2
	}
	cmd, rest := args[0], args[1:]
	var (
		dir, out, summary string
		expect            int
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
	default:
		fmt.Fprintf(stderr, "mergetestreports: unknown command %q (want coverage, junit or failures)\n", cmd)
		return 2
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if dir == "" || expect < 1 || (cmd != "failures" && out == "") {
		fmt.Fprintf(stderr, "mergetestreports %s: -dir and -expect (>=1) are required", cmd)
		if cmd != "failures" {
			fmt.Fprint(stderr, ", and so is -out")
		}
		fmt.Fprintln(stderr)
		return 2
	}

	var err error
	switch cmd {
	case "coverage":
		err = runCoverage(dir, expect, out, stdout)
	case "junit":
		err = runJUnit(dir, expect, out, stdout)
	case "failures":
		err = runFailures(dir, expect, summary, stdout)
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
		idx, convErr := strconv.Atoi(strings.TrimPrefix(e.Name(), shardArtifactPrefix))
		if convErr != nil || idx < 1 || idx > expect {
			return nil, fmt.Errorf("unexpected shard directory %q in %s: the unit matrix has %d shard(s), so only %s1..%s%d may exist "+
				"(the matrix grew or shrank without the reports job's -expect following it)",
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

// junitRootAttrs are the <testsuites> attributes that are totals across
// children and therefore add up across shards.
var junitRootAttrs = []string{"tests", "failures", "errors", "skipped", "disabled", "time"}

// mergeJUnit merges JUnit <testsuites> documents into one. Child <testsuite>
// elements are copied verbatim from the shard's own bytes.
func mergeJUnit(names []string, docs [][]byte) ([]byte, junitStats, error) {
	var st junitStats
	totals := map[string]float64{}
	present := map[string]bool{}
	suiteOwner := map[string]string{}
	var children [][]byte
	for di, data := range docs {
		name := names[di]
		rootAttrs, kids, suiteNames, err := splitTestsuites(data)
		if err != nil {
			return nil, st, fmt.Errorf("%s: %w", name, err)
		}
		for _, a := range junitRootAttrs {
			if v, ok := rootAttrs[a]; ok {
				f, convErr := strconv.ParseFloat(v, 64)
				if convErr != nil {
					return nil, st, fmt.Errorf("%s: <testsuites %s=%q> is not a number", name, a, v)
				}
				totals[a] += f
				present[a] = true
			}
		}
		for _, sn := range suiteNames {
			if prev, dup := suiteOwner[sn]; dup {
				return nil, st, fmt.Errorf("test suite %q is in both %s and %s -- the shards overlap, so its tests ran twice", sn, prev, name)
			}
			suiteOwner[sn] = name
		}
		children = append(children, kids...)
		st.shards++
	}
	if len(children) == 0 {
		return nil, st, errors.New("no <testsuite> in any shard -- a merged report with no tests would read as a clean run, not as a missing measurement")
	}
	var buf bytes.Buffer
	buf.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<testsuites")
	for _, a := range junitRootAttrs {
		if !present[a] {
			continue
		}
		if a == "time" {
			fmt.Fprintf(&buf, " time=\"%f\"", totals[a])
		} else {
			fmt.Fprintf(&buf, " %s=\"%d\"", a, int(totals[a]))
		}
	}
	buf.WriteString(">\n")
	for _, c := range children {
		buf.WriteString("\t")
		buf.Write(c)
		buf.WriteString("\n")
	}
	buf.WriteString("</testsuites>\n")
	st.suites = len(children)
	st.tests, st.failures = int(totals["tests"]), int(totals["failures"])
	st.errors, st.skipped, st.time = int(totals["errors"]), int(totals["skipped"]), totals["time"]
	return buf.Bytes(), st, nil
}

// splitTestsuites parses one JUnit document whose root is <testsuites> and
// returns the root's attributes, the raw bytes of each direct <testsuite>
// child, and each child's name.
func splitTestsuites(data []byte) (map[string]string, [][]byte, []string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var (
		rootAttrs = map[string]string{}
		kids      [][]byte
		names     []string
		depth     int
		sawRoot   bool
		kidStart  int64
	)
	for {
		before := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 1:
				if t.Name.Local != "testsuites" {
					return nil, nil, nil, fmt.Errorf("root element is <%s>, want <testsuites>", t.Name.Local)
				}
				sawRoot = true
				for _, a := range t.Attr {
					rootAttrs[a.Name.Local] = a.Value
				}
			case depth == 2:
				if t.Name.Local != "testsuite" {
					return nil, nil, nil, fmt.Errorf("unexpected <%s> directly under <testsuites>", t.Name.Local)
				}
				kidStart = before
				sn := ""
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						sn = a.Value
					}
				}
				if sn == "" {
					return nil, nil, nil, errors.New("a <testsuite> has no name attribute")
				}
				names = append(names, sn)
			}
		case xml.EndElement:
			if depth == 2 {
				kids = append(kids, bytes.Clone(data[kidStart:dec.InputOffset()]))
			}
			depth--
		}
	}
	if !sawRoot {
		return nil, nil, nil, errors.New("no <testsuites> root element")
	}
	return rootAttrs, kids, names, nil
}

// --- failures ---------------------------------------------------------------

// testEvent is the subset of a test2json event this tool reads.
type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

type failureReport struct {
	// failed maps "package" or "package/Test" to the shard indices that saw it fail.
	failed    map[string][]int
	perShard  []shardEvents
	malformed int
}

type shardEvents struct {
	shard, events, failed int
}

func runFailures(dir string, expect int, summary string, stdout io.Writer) error {
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
	keys := make([]string, 0, len(rep.failed))
	for k := range rep.failed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, s := range rep.perShard {
		fmt.Fprintf(stdout, "shard %d: %d test events, %d failed test(s)/package(s)\n", s.shard, s.events, s.failed)
	}
	if rep.malformed > 0 {
		fmt.Fprintf(stdout, "note: %d line(s) across the shards' go-test.json were not test2json events and were skipped\n", rep.malformed)
	}
	for _, k := range keys {
		fmt.Fprintf(stdout, "FAIL %s (shard %s)\n", k, joinInts(rep.failed[k]))
	}
	fmt.Fprintf(stdout, "%d failed test(s)/package(s) across %d shard(s)\n", len(keys), len(rep.perShard))
	if summary != "" {
		var md strings.Builder
		fmt.Fprintf(&md, "### unit: %d failed test(s)/package(s) across %d shards\n\n", len(keys), len(rep.perShard))
		for _, k := range keys {
			fmt.Fprintf(&md, "- `%s` (shard %s)\n", k, joinInts(rep.failed[k]))
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

func collectFailures(streams [][]byte) (failureReport, error) {
	rep := failureReport{failed: map[string][]int{}}
	for i, raw := range streams {
		shard := i + 1
		se := shardEvents{shard: shard}
		seen := map[string]bool{}
		r := bufio.NewReaderSize(bytes.NewReader(raw), 1<<20)
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var ev testEvent
				if jerr := json.Unmarshal(line, &ev); jerr != nil || ev.Action == "" {
					rep.malformed++
				} else {
					se.events++
					if ev.Action == "fail" {
						key := ev.Package
						if ev.Test != "" {
							key += "/" + ev.Test
						}
						if key != "" && !seen[key] {
							seen[key] = true
							rep.failed[key] = append(rep.failed[key], shard)
							se.failed++
						}
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
		if se.events == 0 {
			return rep, fmt.Errorf("shard %d: go-test.json holds no test2json events -- the failure analysis for this shard did not happen", shard)
		}
		rep.perShard = append(rep.perShard, se)
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
