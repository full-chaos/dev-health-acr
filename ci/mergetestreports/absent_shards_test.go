package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewShardSetRefusesAnAbsentListItCannotTrust(t *testing.T) {
	for _, tc := range []struct{ absent, want string }{
		{`{"shard":2}`, "is not a JSON list"},
		{`[2]`, "is not a JSON list"},
		{`[{"leg":2}]`, `exactly {"shard":N}`},
		{`[{"shard":2,"extra":1}]`, `exactly {"shard":N}`},
		{`[{"shard":"2"}]`, `exactly {"shard":N}`},
		{`[{"shard":0}]`, "outside the unit matrix 1..3"},
		{`[{"shard":4}]`, "outside the unit matrix 1..3"},
		{`[{"shard":2},{"shard":2}]`, "shard 2 twice"},
		{`[{"shard":1},{"shard":2},{"shard":3}]`, "names every shard"},
	} {
		_, err := newShardSet(3, tc.absent)
		mustFail(t, err, tc.want)
	}
	for _, none := range []string{"", " ", "[]"} {
		set, err := newShardSet(3, none)
		if err != nil || len(set.present()) != 3 {
			t.Fatalf("-absent %q: present=%v err=%v, want all 3 shards", none, set.present(), err)
		}
	}
}

func TestRunMergesExactlyThePresentShards(t *testing.T) {
	dir := writeShards(t, "cover.out", profA, profB, profA)
	if err := os.RemoveAll(filepath.Join(dir, shardArtifactPrefix+"2")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "cover.out")
	var so, se bytes.Buffer
	if code := run([]string{"coverage", "-dir", dir, "-expect", "3", "-out", out, "-absent", `[{"shard":2}]`}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}

	so.Reset()
	se.Reset()
	if code := run([]string{"coverage", "-dir", dir, "-expect", "3", "-out", out}, &so, &se); code == 0 {
		t.Fatal("without -absent a merge missing shard 2 must not succeed")
	}
	if !strings.Contains(se.String(), "shard 2 of 3 is missing") {
		t.Fatalf("stderr = %q", se.String())
	}

	so.Reset()
	se.Reset()
	if code := run([]string{"coverage", "-dir", dir, "-expect", "3", "-out", out, "-absent", `[{"shard":3}]`}, &so, &se); code == 0 {
		t.Fatal("a merge that names the wrong absent shard must not succeed")
	}
}

func TestRunRefusesReportsFromAShardThePlanExcluded(t *testing.T) {
	dir := writeShards(t, "cover.out", profA, profB, profA)
	out := filepath.Join(t.TempDir(), "cover.out")
	var so, se bytes.Buffer
	if code := run([]string{"coverage", "-dir", dir, "-expect", "3", "-out", out, "-absent", `[{"shard":2}]`}, &so, &se); code == 0 {
		t.Fatal("an excluded leg that uploaded reports must fail the merge")
	}
	if !strings.Contains(se.String(), "belongs to a shard the plan job excluded") {
		t.Fatalf("stderr = %q", se.String())
	}
}

func TestRunFailuresNamesTheRealShardOfAMatrixWithExcludedLegs(t *testing.T) {
	dir := writeShards(t, "go-test.json",
		`{"Action":"pass","Package":"p/a"}`+"\n",
		`{"Action":"pass","Package":"p/b"}`+"\n",
		`{"Action":"fail","Package":"p/c","Test":"TestX"}`+"\n")
	if err := os.RemoveAll(filepath.Join(dir, shardArtifactPrefix+"2")); err != nil {
		t.Fatal(err)
	}
	var so, se bytes.Buffer
	if code := run([]string{"failures", "-dir", dir, "-expect", "3", "-absent", `[{"shard":2}]`}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	for _, want := range []string{"shard 1: 1 test events", "shard 3: 1 test events", "FAIL p/c/TestX (shard 3)", "across 2 shard(s)"} {
		if !strings.Contains(so.String(), want) {
			t.Fatalf("stdout lacks %q:\n%s", want, so.String())
		}
	}
}

func TestCrosscheckPassesOnAMatrixWithAnExcludedLeg(t *testing.T) {
	f := newCrosscheckFixture(t)
	if err := os.Rename(filepath.Join(f.dir, shardArtifactPrefix+"2"), filepath.Join(f.dir, shardArtifactPrefix+"3")); err != nil {
		t.Fatal(err)
	}
	absent := `[{"shard":2}]`
	var so, se bytes.Buffer
	for _, args := range [][]string{
		{"junit", "-dir", f.dir, "-expect", "3", "-out", filepath.Join(f.out, "junit.xml"), "-absent", absent},
		{"coverage", "-dir", f.dir, "-expect", "3", "-out", filepath.Join(f.out, "cover.out"), "-absent", absent},
		{"failures", "-dir", f.dir, "-expect", "3", "-report", filepath.Join(f.out, "failures.json"), "-absent", absent},
	} {
		if code := run(args, &so, &se); code != 0 {
			t.Fatalf("%v exited %d: %s", args, code, se.String())
		}
	}
	cross := []string{
		"crosscheck", "-dir", f.dir, "-expect", "3",
		"-junit", filepath.Join(f.out, "junit.xml"), "-cover", filepath.Join(f.out, "cover.out"),
		"-cobertura", filepath.Join(f.out, "coverage.xml"), "-failures", filepath.Join(f.out, "failures.json"),
	}
	so.Reset()
	se.Reset()
	if code := run(append(append([]string{}, cross...), "-absent", absent), &so, &se); code != 0 ||
		!strings.Contains(so.String(), "crosscheck OK: 2 shards, 5 testcases in 2 suites") {
		t.Fatalf("exit %d: %s%s", code, so.String(), se.String())
	}

	so.Reset()
	se.Reset()
	if code := run(cross, &so, &se); code == 0 {
		t.Fatal("crosscheck without -absent must refuse a matrix with shard 2 missing")
	}

	f.editFailures(t, func(r *failureReport) { r.PerShard[1].Shard = 2 })
	so.Reset()
	se.Reset()
	if code := run(append(append([]string{}, cross...), "-absent", absent), &so, &se); code == 0 ||
		!strings.Contains(so.String()+se.String(), "is shard 2, want shard 3") {
		t.Fatalf("a listing that names the excluded shard must fail the crosscheck: exit %d: %s%s", code, so.String(), se.String())
	}
}
