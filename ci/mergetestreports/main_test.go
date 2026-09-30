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
		"<testsuite tests=\"1\" skipped=\"1\" name=\"a\"></testsuite>\n</testsuites>"
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
		`{"Action":"fail","Package":"p/c","Test":"TestZ"}` + "\n" // duplicate event, counted once
	rep, err := collectFailures([][]byte{[]byte(s1), []byte(s2), []byte(s3)})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Failed["p/a/TestX"]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("p/a/TestX: %v", rep.Failed)
	}
	if _, ok := rep.Failed["p/a"]; !ok {
		t.Fatalf("a package-level failure was dropped: %v", rep.Failed)
	}
	if got := rep.Failed["p/c/TestZ"]; len(got) != 1 || got[0] != 3 {
		t.Fatalf("a failure in the LAST shard must be reported, not only the first shard's: %v", rep.Failed)
	}
	if _, ok := rep.Failed["p/b/TestY"]; ok {
		t.Fatalf("a passing test was reported as failed: %v", rep.Failed)
	}
	if rep.Shards != 3 || len(rep.PerShard) != 3 {
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

// --- fail closed (CHAOS-3895 r1) ---------------------------------------------
//
// The class: the merger accepted malformed input silently. Each guard below has
// a plant that the previous behaviour let through.

func TestShardFilesRefusesTwoArtifactsForOneShardIndex(t *testing.T) {
	// strconv.Atoi maps "01" to 1; without the canonical-spelling rule the
	// second artifact for shard 1 was ignored, not refused.
	dir := writeShards(t, "cover.out", "x", "x")
	extra := filepath.Join(dir, shardArtifactPrefix+"01")
	if err := os.MkdirAll(extra, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "cover.out"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := shardFiles(dir, 2, "cover.out")
	mustFail(t, err, "plain decimal")
}

func TestShardFilesRefusesOtherNonCanonicalShardNames(t *testing.T) {
	for _, name := range []string{"+1", "1.zip", "1 ", "x", "", "0", "3"} {
		dir := writeShards(t, "cover.out", "x", "x")
		if err := os.MkdirAll(filepath.Join(dir, shardArtifactPrefix+name), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := shardFiles(dir, 2, "cover.out"); err == nil {
			t.Fatalf("shard directory suffix %q was accepted", name)
		}
	}
}

func TestCollectFailuresRefusesAnyLineThatIsNotAnEvent(t *testing.T) {
	// A valid failure, then a mangled line: the mangled one could have been the
	// failure. Also an event with no Action, and JSON that is not an object.
	valid := `{"Action":"fail","Package":"p/a","Test":"TestX"}` + "\n"
	for name, tail := range map[string]string{
		"not json":         "not-json\n",
		"truncated json":   `{"Action":"fail","Package":"p/b","Te` + "\n",
		"no action":        `{"Package":"p/b"}` + "\n",
		"json array":       `["fail"]` + "\n",
		"json string":      `"fail"` + "\n",
		"trailing garbage": `{"Action":"pass"} junk` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := collectFailures([][]byte{[]byte(valid + tail)})
			mustFail(t, err, "line 2 is not a test2json event")
		})
	}
}

func TestMergeJUnitRefusesRootTotalsThatAreMissingOrNotIntegers(t *testing.T) {
	suite := "<testsuite tests=\"2\" failures=\"1\" name=\"a\"></testsuite>"
	root := func(attrs string) string { return "<testsuites " + attrs + ">" + suite + "</testsuites>" }
	cases := map[string]string{
		"tests missing":      root(`failures="1" errors="0" time="1"`),
		"failures missing":   root(`tests="2" errors="0" time="1"`),
		"errors missing":     root(`tests="2" failures="1" time="1"`),
		"time missing":       root(`tests="2" failures="1" errors="0"`),
		"tests fractional":   root(`tests="1.5" failures="1" errors="0" time="1"`),
		"tests negative":     root(`tests="-2" failures="1" errors="0" time="1"`),
		"tests padded":       root(`tests="02" failures="1" errors="0" time="1"`),
		"tests empty":        root(`tests="" failures="1" errors="0" time="1"`),
		"time not a number":  root(`tests="2" failures="1" errors="0" time="soon"`),
		"skipped fractional": root(`tests="2" failures="1" errors="0" skipped="0.5" time="1"`),
		"tests below suites": root(`tests="1" failures="1" errors="0" time="1"`),
		"tests above suites": root(`tests="3" failures="1" errors="0" time="1"`),
		"failures mismatch":  root(`tests="2" failures="0" errors="0" time="1"`),
		"errors mismatch":    root(`tests="2" failures="1" errors="4" time="1"`),
		"skipped mismatch":   root(`tests="2" failures="1" errors="0" skipped="3" time="1"`),
		// Consistent but impossible: root and suite agree on a negative count, so
		// only the non-negative rule (not the sum rule) can refuse it.
		"negative but summing": "<testsuites tests=\"-1\" failures=\"0\" errors=\"0\" time=\"1\"><testsuite tests=\"-1\" failures=\"0\" name=\"a\"></testsuite></testsuites>",
		"suite tests missing":  "<testsuites tests=\"0\" failures=\"0\" errors=\"0\" time=\"1\"><testsuite name=\"a\"></testsuite></testsuites>",
		"suite tests bad":      "<testsuites tests=\"0\" failures=\"0\" errors=\"0\" time=\"1\"><testsuite tests=\"one\" name=\"a\"></testsuite></testsuites>",
		"suite failures bad":   "<testsuites tests=\"1\" failures=\"0\" errors=\"0\" time=\"1\"><testsuite tests=\"1\" failures=\"-1\" name=\"a\"></testsuite></testsuites>",
	}
	// The unmutated root is valid: the plants above are the only difference.
	if _, _, err := mergeJUnit([]string{"ok"}, [][]byte{[]byte(root(`tests="2" failures="1" errors="0" time="1"`))}); err != nil {
		t.Fatalf("control document was refused: %v", err)
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := mergeJUnit([]string{"a"}, [][]byte{[]byte(body)}); err == nil {
				t.Fatalf("accepted %q", body)
			}
		})
	}
}

func TestMergeJUnitRefusesMoreThanOneRoot(t *testing.T) {
	a := gotestsumDoc(1, 0, "1.000000", gotestsumSuite("example.com/m/a", 1, 0))
	b := gotestsumDoc(5, 0, "9.000000", gotestsumSuite("example.com/m/b", 5, 0))
	// Second root whose totals are exactly the combined sum: the totals-versus-
	// suites rule alone would accept this, so only the one-root rule refuses it.
	summing := gotestsumDoc(1, 0, "1.000000", gotestsumSuite("example.com/m/a", 1, 0)) +
		gotestsumDoc(6, 0, "9.000000", gotestsumSuite("example.com/m/b", 5, 0))
	for name, body := range map[string]string{
		"two documents": a + b,
		"two documents whose second root sums both": summing,
		"stray element": a + "<extra/>",
		"trailing text": a + "trailing text",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := mergeJUnit([]string{"a"}, [][]byte{[]byte(body)}); err == nil {
				t.Fatalf("accepted %s", name)
			}
		})
	}
	// Whitespace and a trailing comment after the root are not a second root.
	if _, _, err := mergeJUnit([]string{"a"}, [][]byte{[]byte(a + "\n\n<!-- end -->\n")}); err != nil {
		t.Fatalf("trailing whitespace/comment refused: %v", err)
	}
}

func TestMergeJUnitCountsSkippedFromSuitesWhenTheRootCarriesNone(t *testing.T) {
	// gotestsum puts `skipped` on suites, never on the root: the real shards of
	// main run 36716154379 hold 40 skipped tests and a root with no skipped
	// attribute. The summary must say 40, not the root's absence.
	suite := "<testsuite tests=\"3\" failures=\"0\" skipped=\"2\" name=\"a\"></testsuite>"
	doc := "<testsuites tests=\"3\" failures=\"0\" errors=\"0\" time=\"1\">" + suite + "</testsuites>"
	out, st, err := mergeJUnit([]string{"a"}, [][]byte{[]byte(doc)})
	if err != nil {
		t.Fatal(err)
	}
	if st.skipped != 2 {
		t.Fatalf("skipped = %d, want 2 (from the suite)", st.skipped)
	}
	if strings.Contains(string(out), "skipped=\"2\" time") {
		t.Fatalf("root grew a skipped attribute the source root never had: %s", out)
	}
}

// --- crosscheck --------------------------------------------------------------

// crosscheckFixture lays out N shards, runs the three merge commands through
// run() exactly as the workflow does, writes a Cobertura file, and returns the
// crosscheck arguments. Each negative test then damages one merged file.
type crosscheckFixture struct {
	dir, out string
	args     []string
}

func newCrosscheckFixture(t *testing.T) crosscheckFixture {
	t.Helper()
	sa := gotestsumSuite("example.com/m/a", 3, 1)
	sb := gotestsumSuite("example.com/m/b", 2, 0)
	dir := t.TempDir()
	shards := map[string]map[string]string{
		"1": {
			"cover.out":    profA,
			"junit.xml":    gotestsumDoc(3, 1, "1.500000", sa),
			"go-test.json": `{"Action":"fail","Package":"example.com/m/a","Test":"TestBad0"}` + "\n",
		},
		"2": {
			"cover.out":    profB,
			"junit.xml":    gotestsumDoc(2, 0, "1.500000", sb),
			"go-test.json": `{"Action":"pass","Package":"example.com/m/b"}` + "\n",
		},
	}
	for idx, files := range shards {
		for name, body := range files {
			p := filepath.Join(dir, shardArtifactPrefix+idx, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	out := t.TempDir()
	var so, se bytes.Buffer
	for _, args := range [][]string{
		{"junit", "-dir", dir, "-expect", "2", "-out", filepath.Join(out, "junit.xml")},
		{"coverage", "-dir", dir, "-expect", "2", "-out", filepath.Join(out, "cover.out")},
		{"failures", "-dir", dir, "-expect", "2", "-report", filepath.Join(out, "failures.json")},
	} {
		if code := run(args, &so, &se); code != 0 {
			t.Fatalf("%v exited %d: %s", args, code, se.String())
		}
	}
	if err := os.WriteFile(filepath.Join(out, "coverage.xml"),
		[]byte(`<?xml version="1.0"?><coverage line-rate="0.5" lines-covered="2" lines-valid="4"></coverage>`), 0o644); err != nil {
		t.Fatal(err)
	}
	return crosscheckFixture{dir: dir, out: out, args: []string{
		"crosscheck", "-dir", dir, "-expect", "2",
		"-junit", filepath.Join(out, "junit.xml"), "-cover", filepath.Join(out, "cover.out"),
		"-cobertura", filepath.Join(out, "coverage.xml"), "-failures", filepath.Join(out, "failures.json"),
	}}
}

func (f crosscheckFixture) run(t *testing.T) (int, string) {
	t.Helper()
	var so, se bytes.Buffer
	code := run(f.args, &so, &se)
	return code, so.String() + se.String()
}

func (f crosscheckFixture) rewrite(t *testing.T, name string, fn func(string) string) {
	t.Helper()
	p := filepath.Join(f.out, name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	mutated := fn(string(b))
	if mutated == string(b) {
		t.Fatalf("mutation of %s changed nothing", name)
	}
	if err := os.WriteFile(p, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCrosscheckPassesOnACorrectMerge(t *testing.T) {
	f := newCrosscheckFixture(t)
	code, out := f.run(t)
	if code != 0 || !strings.Contains(out, "crosscheck OK: 2 shards, 5 testcases in 2 suites, 1 failure(s)") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestCrosscheckFailsWhenTheMergedReportsDoNotMatchTheShards(t *testing.T) {
	cases := []struct {
		name string
		want string
		do   func(t *testing.T, f crosscheckFixture)
	}{
		{"a suite dropped from the merged junit", "merged junit has", func(t *testing.T, f crosscheckFixture) {
			f.rewrite(t, "junit.xml", func(s string) string {
				i := strings.Index(s, "\t<testsuite tests=\"2\"")
				j := i + strings.Index(s[i:], "</testsuite>\n") + len("</testsuite>\n")
				return s[:i] + s[j:]
			})
		}},
		{"a testcase dropped from a merged suite with every stated total intact", "merged junit has", func(t *testing.T, f crosscheckFixture) {
			f.rewrite(t, "junit.xml", func(s string) string {
				i := strings.Index(s, "\t\t<testcase classname=\"example.com/m/b\" name=\"TestOK1\"")
				j := i + strings.Index(s[i:], "</testcase>\n") + len("</testcase>\n")
				return s[:i] + s[j:]
			})
		}},
		{"the merged junit root total is wrong", "merged junit root says tests=", func(t *testing.T, f crosscheckFixture) {
			// Suite content intact, only the stated total edited.
			f.rewrite(t, "junit.xml", func(s string) string { return strings.Replace(s, `<testsuites tests="5"`, `<testsuites tests="4"`, 1) })
		}},
		{"the merged junit root failures are wrong", "merged junit root says failures=", func(t *testing.T, f crosscheckFixture) {
			f.rewrite(t, "junit.xml", func(s string) string { return strings.Replace(s, `failures="1" errors`, `failures="0" errors`, 1) })
		}},
		{"the merged coverage profile is not the merge", "is not the merge", func(t *testing.T, f crosscheckFixture) {
			f.rewrite(t, "cover.out", func(s string) string { return strings.Replace(s, "b/b.go:5.1,6.2 1 1", "b/b.go:5.1,6.2 1 0", 1) })
		}},
		{"the cobertura report covers nothing", "lines-covered=", func(t *testing.T, f crosscheckFixture) {
			f.rewrite(t, "coverage.xml", func(s string) string { return strings.Replace(s, `lines-valid="4"`, `lines-valid="0"`, 1) })
		}},
		{"the cobertura report is not cobertura", "cobertura report", func(t *testing.T, f crosscheckFixture) {
			f.rewrite(t, "coverage.xml", func(s string) string { return strings.Replace(s, "<coverage", "<report", 1) })
		}},
		{"the failure listing read fewer shards", "failure listing read", func(t *testing.T, f crosscheckFixture) {
			f.rewrite(t, "failures.json", func(s string) string { return strings.Replace(s, `"shards": 2`, `"shards": 1`, 1) })
		}},
		{"the failure listing names no failure although junit has one", "names none", func(t *testing.T, f crosscheckFixture) {
			f.rewrite(t, "failures.json", func(s string) string {
				return strings.Replace(s, `"example.com/m/a/TestBad0": [
      1
    ]`, ``, 1)
			})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newCrosscheckFixture(t)
			c.do(t, f)
			code, out := f.run(t)
			if code == 0 {
				t.Fatalf("crosscheck passed: %s", out)
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("output %q does not contain %q", out, c.want)
			}
		})
	}
}

func TestCrosscheckFailsWhenAMergeStepNeverRan(t *testing.T) {
	// The disabled-step case the grep guard cannot see: the step's text is in
	// the workflow, but its output does not exist.
	for _, name := range []string{"junit.xml", "cover.out", "coverage.xml", "failures.json"} {
		t.Run(name, func(t *testing.T) {
			f := newCrosscheckFixture(t)
			if err := os.Remove(filepath.Join(f.out, name)); err != nil {
				t.Fatal(err)
			}
			code, out := f.run(t)
			if code == 0 || !strings.Contains(out, "was not produced") {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

func TestCrosscheckFailsWhenAShardIsMissing(t *testing.T) {
	f := newCrosscheckFixture(t)
	if err := os.RemoveAll(filepath.Join(f.dir, shardArtifactPrefix+"2")); err != nil {
		t.Fatal(err)
	}
	if code, out := f.run(t); code == 0 || !strings.Contains(out, "shard 2 of 2 is missing") {
		t.Fatalf("exit %d: %s", code, out)
	}
}
