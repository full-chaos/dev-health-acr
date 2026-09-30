package main

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeShards lays out dir/go-unit-shard-<i>/<name> for each content, the same
// shape actions/download-artifact produces with `pattern: go-unit-shard-*` and
// merge-multiple left false.
func writeShards(t *testing.T, name string, contents ...string) string {
	t.Helper()
	dir := t.TempDir()
	for i, c := range contents {
		p := filepath.Join(dir, shardArtifactPrefix+strconv.Itoa(i+1), name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func mustFail(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got none", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

// --- shard layout -----------------------------------------------------------

func TestShardFilesRefusesAMissingShard(t *testing.T) {
	dir := writeShards(t, "cover.out", "mode: set\n", "mode: set\n", "mode: set\n")
	_, err := shardFiles(dir, 4, "cover.out")
	mustFail(t, err, "shard 4 of 4 is missing")
}

func TestShardFilesRefusesAnExtraShard(t *testing.T) {
	// The matrix grew to 5 but the reports job still says 4: a merge of 4
	// would silently drop shard 5's suite.
	dir := writeShards(t, "cover.out", "x", "x", "x", "x", "x")
	_, err := shardFiles(dir, 4, "cover.out")
	mustFail(t, err, "unexpected shard directory")
}

func TestShardFilesRefusesAnEmptyFile(t *testing.T) {
	dir := writeShards(t, "cover.out", "mode: set\n", "")
	_, err := shardFiles(dir, 2, "cover.out")
	mustFail(t, err, "empty or not a regular file")
}

func TestShardFilesRefusesAShardWithoutTheFile(t *testing.T) {
	dir := writeShards(t, "junit.xml", "x", "x")
	_, err := shardFiles(dir, 2, "cover.out")
	mustFail(t, err, "has no cover.out")
}

func TestShardFilesIgnoresUnrelatedEntries(t *testing.T) {
	dir := writeShards(t, "cover.out", "x", "x")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := shardFiles(dir, 2, "cover.out")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.Contains(paths[0], "go-unit-shard-1") || !strings.Contains(paths[1], "go-unit-shard-2") {
		t.Fatalf("shards must come back in index order, got %v", paths)
	}
}

// --- coverage ---------------------------------------------------------------

const (
	profA = "mode: set\n" +
		"example.com/m/a/a.go:10.2,12.3 2 1\n" +
		"example.com/m/a/a.go:14.2,15.3 1 0\n"
	profB = "mode: set\n" +
		"example.com/m/b/b.go:5.1,6.2 1 1\n"
)

func TestMergeCoverageConcatenatesUnderOneHeader(t *testing.T) {
	out, st, err := mergeCoverage([]string{"a", "b"}, [][]byte{[]byte(profA), []byte(profB)})
	if err != nil {
		t.Fatal(err)
	}
	want := "mode: set\n" +
		"example.com/m/a/a.go:10.2,12.3 2 1\n" +
		"example.com/m/a/a.go:14.2,15.3 1 0\n" +
		"example.com/m/b/b.go:5.1,6.2 1 1\n"
	if string(out) != want {
		t.Fatalf("merged profile:\n%s\nwant:\n%s", out, want)
	}
	if got := strings.Count(string(out), "mode:"); got != 1 {
		t.Fatalf("want exactly one mode header, got %d", got)
	}
	if st.blocks != 3 || st.overlapping != 0 || st.shards != 2 || st.mode != "set" {
		t.Fatalf("stats = %+v", st)
	}
}

func TestMergeCoverageIsOrderIndependent(t *testing.T) {
	ab, _, err := mergeCoverage([]string{"a", "b"}, [][]byte{[]byte(profA), []byte(profB)})
	if err != nil {
		t.Fatal(err)
	}
	ba, _, err := mergeCoverage([]string{"b", "a"}, [][]byte{[]byte(profB), []byte(profA)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ab, ba) {
		t.Fatalf("shard order changed the merged profile:\n%s\nvs\n%s", ab, ba)
	}
}

// The property the whole job rests on: splitting a profile across shards and
// merging it back is the identity (up to record order). Checked against a
// profile with blocks spread over several files.
func TestMergeCoverageOfASplitProfileEqualsTheWhole(t *testing.T) {
	whole := "mode: atomic\n" +
		"example.com/m/a/a.go:1.1,2.2 1 3\n" +
		"example.com/m/a/a.go:3.1,4.2 2 0\n" +
		"example.com/m/b/b.go:1.1,2.2 1 7\n" +
		"example.com/m/c/c.go:9.9,10.10 4 1\n"
	lines := strings.Split(strings.TrimSpace(whole), "\n")
	header, recs := lines[0], lines[1:]
	shards := [][]byte{{}, {}, {}}
	for i := range shards {
		shards[i] = []byte(header + "\n")
	}
	for i, r := range recs {
		s := i % len(shards)
		shards[s] = append(shards[s], []byte(r+"\n")...)
	}
	out, st, err := mergeCoverage([]string{"1", "2", "3"}, shards)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != whole {
		t.Fatalf("split-then-merge is not the identity:\n%s\nwant:\n%s", out, whole)
	}
	if st.overlapping != 0 {
		t.Fatalf("disjoint shards reported %d overlapping blocks", st.overlapping)
	}
}

func TestMergeCoverageSetModeOverlapTakesTheMax(t *testing.T) {
	// The same block hit in one shard and missed in another is covered.
	a := "mode: set\nexample.com/m/a/a.go:1.1,2.2 1 0\n"
	b := "mode: set\nexample.com/m/a/a.go:1.1,2.2 1 1\n"
	out, st, err := mergeCoverage([]string{"a", "b"}, [][]byte{[]byte(a), []byte(b)})
	if err != nil {
		t.Fatal(err)
	}
	if want := "mode: set\nexample.com/m/a/a.go:1.1,2.2 1 1\n"; string(out) != want {
		t.Fatalf("got %q want %q", out, want)
	}
	if st.overlapping != 1 || st.blocks != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestMergeCoverageCountModeOverlapSums(t *testing.T) {
	for _, mode := range []string{"count", "atomic"} {
		a := "mode: " + mode + "\nexample.com/m/a/a.go:1.1,2.2 1 3\n"
		b := "mode: " + mode + "\nexample.com/m/a/a.go:1.1,2.2 1 4\n"
		out, _, err := mergeCoverage([]string{"a", "b"}, [][]byte{[]byte(a), []byte(b)})
		if err != nil {
			t.Fatal(err)
		}
		if want := "mode: " + mode + "\nexample.com/m/a/a.go:1.1,2.2 1 7\n"; string(out) != want {
			t.Fatalf("%s: got %q want %q", mode, out, want)
		}
	}
}

func TestMergeCoverageRefusesMixedModes(t *testing.T) {
	_, _, err := mergeCoverage([]string{"a", "b"}, [][]byte{[]byte(profA), []byte("mode: count\nexample.com/m/b/b.go:1.1,2.2 1 1\n")})
	mustFail(t, err, "differs from the other shards")
}

func TestMergeCoverageRefusesMalformedInput(t *testing.T) {
	cases := map[string]string{
		"no header":         "example.com/m/a/a.go:1.1,2.2 1 1\n",
		"unknown mode":      "mode: bogus\n",
		"garbage record":    "mode: set\nnot a record\n",
		"missing count":     "mode: set\nexample.com/m/a/a.go:1.1,2.2 1\n",
		"non-numeric count": "mode: set\nexample.com/m/a/a.go:1.1,2.2 1 x\n",
		"whitespace only":   "\n\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := mergeCoverage([]string{"a"}, [][]byte{[]byte(body)}); err == nil {
				t.Fatalf("accepted %q", body)
			}
		})
	}
}

func TestMergeCoverageRefusesAMergeWithNoRecords(t *testing.T) {
	_, _, err := mergeCoverage([]string{"a", "b"}, [][]byte{[]byte("mode: set\n"), []byte("mode: set\n")})
	mustFail(t, err, "no coverage records")
}

func TestMergeCoverageAllowsAHeaderOnlyShardBesideRealOnes(t *testing.T) {
	// A shard whose packages have no statements still writes its header.
	out, _, err := mergeCoverage([]string{"a", "b"}, [][]byte{[]byte(profA), []byte("mode: set\n")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "mode: set\n") || !strings.Contains(string(out), "a.go:10.2,12.3") {
		t.Fatalf("got %q", out)
	}
}

// --- junit ------------------------------------------------------------------

// gotestsumSuite returns a <testsuite> in the exact shape gotestsum writes,
// including a <properties> block and (when failing) an XML-escaped body.
func gotestsumSuite(pkg string, tests, failures int) string {
	var b strings.Builder
	b.WriteString("\t<testsuite tests=\"" + strconv.Itoa(tests) + "\" failures=\"" + strconv.Itoa(failures) +
		"\" time=\"1.500000\" name=\"" + pkg + "\" timestamp=\"2026-09-30T12:40:33Z\">\n")
	b.WriteString("\t\t<properties>\n\t\t\t<property name=\"go.version\" value=\"go1.27.0 linux/amd64\"></property>\n\t\t</properties>\n")
	for i := 0; i < tests-failures; i++ {
		b.WriteString("\t\t<testcase classname=\"" + pkg + "\" name=\"TestOK" + strconv.Itoa(i) + "\" time=\"0.000000\"></testcase>\n")
	}
	for i := 0; i < failures; i++ {
		b.WriteString("\t\t<testcase classname=\"" + pkg + "\" name=\"TestBad" + strconv.Itoa(i) + "\" time=\"0.010000\">\n" +
			"\t\t\t<failure message=\"Failed\" type=\"\">got &lt;nil&gt; &amp; wanted &#34;x&#34;</failure>\n\t\t</testcase>\n")
	}
	b.WriteString("\t</testsuite>\n")
	return b.String()
}

func gotestsumDoc(tests, failures int, time string, suites ...string) string {
	return "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<testsuites tests=\"" + strconv.Itoa(tests) +
		"\" failures=\"" + strconv.Itoa(failures) + "\" errors=\"0\" time=\"" + time + "\">\n" +
		strings.Join(suites, "") + "</testsuites>\n"
}

func TestMergeJUnitSumsTotalsAndKeepsSuitesByteForByte(t *testing.T) {
	sa := gotestsumSuite("example.com/m/a", 3, 1)
	sb := gotestsumSuite("example.com/m/b", 2, 0)
	sc := gotestsumSuite("example.com/m/c", 4, 0)
	docs := [][]byte{
		[]byte(gotestsumDoc(3, 1, "1.500000", sa)),
		[]byte(gotestsumDoc(6, 0, "3.000000", sb, sc)),
	}
	out, st, err := mergeJUnit([]string{"a", "b"}, docs)
	if err != nil {
		t.Fatal(err)
	}
	want := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<testsuites tests=\"9\" failures=\"1\" errors=\"0\" time=\"4.500000\">\n" +
		sa + sb + sc + "</testsuites>\n"
	if string(out) != want {
		t.Fatalf("merged junit:\n%s\nwant:\n%s", out, want)
	}
	if st.suites != 3 || st.tests != 9 || st.failures != 1 || st.shards != 2 {
		t.Fatalf("stats = %+v", st)
	}
	// It must also be a well-formed document with one root.
	var root struct {
		XMLName xml.Name `xml:"testsuites"`
		Suites  []struct {
			Name string `xml:"name,attr"`
		} `xml:"testsuite"`
	}
	if err := xml.Unmarshal(out, &root); err != nil {
		t.Fatalf("merged junit is not well-formed: %v", err)
	}
	if len(root.Suites) != 3 {
		t.Fatalf("want 3 suites, got %d", len(root.Suites))
	}
}

func TestMergeJUnitKeepsFailureBodiesAndSkippedElements(t *testing.T) {
	suite := "\t<testsuite tests=\"2\" failures=\"1\" skipped=\"1\" time=\"0.1\" name=\"example.com/m/x\">\n" +
		"\t\t<testcase classname=\"example.com/m/x\" name=\"TestS\" time=\"0\"><skipped message=\"needs docker\"></skipped></testcase>\n" +
		"\t\t<testcase classname=\"example.com/m/x\" name=\"TestF\" time=\"0\"><failure message=\"a &amp; b\">line1\nline2 &lt;x&gt;</failure><system-out>out &amp; more</system-out></testcase>\n" +
		"\t</testsuite>\n"
	out, _, err := mergeJUnit([]string{"a"}, [][]byte{[]byte(gotestsumDoc(2, 1, "0.1", suite))})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), strings.TrimPrefix(suite, "\t")) {
		t.Fatalf("suite was altered:\n%s", out)
	}
}

func TestMergeJUnitCarriesSkippedTotalOnlyWhenAShardHasIt(t *testing.T) {
	a := "<testsuites tests=\"1\" failures=\"0\" errors=\"0\" skipped=\"1\" time=\"1\">\n" +
		"<testsuite tests=\"1\" name=\"a\"></testsuite>\n</testsuites>"
	b := "<testsuites tests=\"1\" failures=\"0\" errors=\"0\" time=\"1\">\n" +
		"<testsuite tests=\"1\" name=\"b\"></testsuite>\n</testsuites>"
	out, st, err := mergeJUnit([]string{"a", "b"}, [][]byte{[]byte(a), []byte(b)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `skipped="1"`) || st.skipped != 1 {
		t.Fatalf("skipped total lost: %s", out)
	}
}

func TestMergeJUnitRefusesTheSameSuiteInTwoShards(t *testing.T) {
	s := gotestsumSuite("example.com/m/a", 1, 0)
	_, _, err := mergeJUnit([]string{"a", "b"}, [][]byte{[]byte(gotestsumDoc(1, 0, "1", s)), []byte(gotestsumDoc(1, 0, "1", s))})
	mustFail(t, err, "the shards overlap")
}

func TestMergeJUnitRefusesMalformedInput(t *testing.T) {
	good := gotestsumSuite("example.com/m/a", 1, 0)
	cases := map[string]string{
		"truncated":       gotestsumDoc(1, 0, "1", good)[:len(gotestsumDoc(1, 0, "1", good))-20],
		"wrong root":      "<testsuite name=\"x\"></testsuite>",
		"no root":         "",
		"stray child":     "<testsuites tests=\"0\"><oops/></testsuites>",
		"nameless suite":  "<testsuites tests=\"1\"><testsuite tests=\"1\"></testsuite></testsuites>",
		"non-numeric sum": "<testsuites tests=\"many\"><testsuite name=\"x\"></testsuite></testsuites>",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := mergeJUnit([]string{"a"}, [][]byte{[]byte(body)}); err == nil {
				t.Fatalf("accepted %q", body)
			}
		})
	}
}

func TestMergeJUnitRefusesAMergeWithNoSuites(t *testing.T) {
	_, _, err := mergeJUnit([]string{"a"}, [][]byte{[]byte("<testsuites tests=\"0\" failures=\"0\" errors=\"0\" time=\"0\"></testsuites>")})
	mustFail(t, err, "no <testsuite>")
}

// --- failures ---------------------------------------------------------------

func TestCollectFailuresReadsEveryShard(t *testing.T) {
	s1 := `{"Action":"run","Package":"p/a","Test":"TestX"}` + "\n" +
		`{"Action":"fail","Package":"p/a","Test":"TestX"}` + "\n" +
		`{"Action":"fail","Package":"p/a"}` + "\n"
	s2 := `{"Action":"pass","Package":"p/b","Test":"TestY"}` + "\n" +
		`{"Action":"pass","Package":"p/b"}` + "\n"
	s3 := `{"Action":"fail","Package":"p/c","Test":"TestZ"}` + "\n" +
		`{"Action":"fail","Package":"p/c","Test":"TestZ"}` + "\n" + // duplicate event, counted once
		"not json at all\n"
	rep, err := collectFailures([][]byte{[]byte(s1), []byte(s2), []byte(s3)})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.failed["p/a/TestX"]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("p/a/TestX: %v", rep.failed)
	}
	if _, ok := rep.failed["p/a"]; !ok {
		t.Fatalf("a package-level failure was dropped: %v", rep.failed)
	}
	if got := rep.failed["p/c/TestZ"]; len(got) != 1 || got[0] != 3 {
		t.Fatalf("a failure in the LAST shard must be reported, not only the first shard's: %v", rep.failed)
	}
	if _, ok := rep.failed["p/b/TestY"]; ok {
		t.Fatalf("a passing test was reported as failed: %v", rep.failed)
	}
	if rep.malformed != 1 || len(rep.perShard) != 3 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestCollectFailuresRefusesAShardWithNoEvents(t *testing.T) {
	_, err := collectFailures([][]byte{[]byte(`{"Action":"pass","Package":"p/a"}` + "\n"), []byte("garbage only\n")})
	mustFail(t, err, "shard 2")
}

// --- end to end through run() ------------------------------------------------

func TestRunWritesMergedReports(t *testing.T) {
	dir := writeShards(t, "cover.out", profA, profB)
	out := filepath.Join(t.TempDir(), "nested", "cover.out")
	var so, se bytes.Buffer
	if code := run([]string{"coverage", "-dir", dir, "-expect", "2", "-out", out}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "mode: set\n") || strings.Count(string(got), "\n") != 4 {
		t.Fatalf("merged profile = %q", got)
	}

	jdir := writeShards(t, "junit.xml",
		gotestsumDoc(1, 0, "1.000000", gotestsumSuite("example.com/m/a", 1, 0)),
		gotestsumDoc(1, 0, "1.000000", gotestsumSuite("example.com/m/b", 1, 0)))
	jout := filepath.Join(t.TempDir(), "junit.xml")
	so.Reset()
	se.Reset()
	if code := run([]string{"junit", "-dir", jdir, "-expect", "2", "-out", jout}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	if b, _ := os.ReadFile(jout); !strings.Contains(string(b), `tests="2"`) {
		t.Fatalf("merged junit = %s", b)
	}
}

func TestRunFailsLoudlyWhenAShardIsMissing(t *testing.T) {
	dir := writeShards(t, "cover.out", profA)
	out := filepath.Join(t.TempDir(), "cover.out")
	var so, se bytes.Buffer
	if code := run([]string{"coverage", "-dir", dir, "-expect", "4", "-out", out}, &so, &se); code == 0 {
		t.Fatal("a 1-of-4 merge must not succeed")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a refused merge must not leave an output file that reads as a complete report")
	}
	if !strings.Contains(se.String(), "shard 2 of 4 is missing") {
		t.Fatalf("stderr = %q", se.String())
	}
}

func TestRunFailuresWritesSummaryAndExitsZero(t *testing.T) {
	dir := writeShards(t, "go-test.json",
		`{"Action":"fail","Package":"p/a","Test":"TestX"}`+"\n",
		`{"Action":"pass","Package":"p/b"}`+"\n")
	summary := filepath.Join(t.TempDir(), "summary.md")
	var so, se bytes.Buffer
	if code := run([]string{"failures", "-dir", dir, "-expect", "2", "-summary", summary}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	if !strings.Contains(so.String(), "FAIL p/a/TestX (shard 1)") {
		t.Fatalf("stdout = %q", so.String())
	}
	if b, _ := os.ReadFile(summary); !strings.Contains(string(b), "`p/a/TestX` (shard 1)") {
		t.Fatalf("summary = %q", b)
	}
}

func TestRunRejectsBadUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"coverage"},
		{"coverage", "-dir", "x", "-expect", "2"},              // no -out
		{"coverage", "-dir", "x", "-expect", "0", "-out", "y"}, // expect < 1
		{"junit", "-dir", "", "-expect", "2", "-out", "y"},
	} {
		var so, se bytes.Buffer
		if code := run(args, &so, &se); code == 0 {
			t.Fatalf("args %v exited 0", args)
		}
	}
}
