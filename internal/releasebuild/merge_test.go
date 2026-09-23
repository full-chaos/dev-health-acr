package releasebuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildLeg(t *testing.T, source, platform string) string {
	t.Helper()
	only, err := ParseTargets(platform)
	if err != nil {
		t.Fatalf("parse %s: %v", platform, err)
	}
	dir := t.TempDir()
	if _, err := NewBuilder(CompilerFunc(writeTestBinary)).Build(context.Background(), Request{SourceDir: source, OutputDir: dir, Identity: testIdentity(), Only: only}); err != nil {
		t.Fatalf("build leg %s: %v", platform, err)
	}
	return dir
}

func allLegs(t *testing.T, source string) []string {
	return []string{buildLeg(t, source, "linux/amd64"), buildLeg(t, source, "linux/arm64"), buildLeg(t, source, "darwin/amd64,darwin/arm64"), buildLeg(t, source, "windows/amd64")}
}

// The determinism proof: per-platform legs merged must equal one full build
// byte for byte (archives, release-manifest.json, SHA256SUMS).
func TestMerge_equals_single_host_full_build(t *testing.T) {
	source := writeClientSource(t)
	full := t.TempDir()
	want, err := NewBuilder(CompilerFunc(writeTestBinary)).Build(context.Background(), Request{SourceDir: source, OutputDir: full, Identity: testIdentity()})
	if err != nil {
		t.Fatalf("full build: %v", err)
	}
	merged := filepath.Join(t.TempDir(), "merged")
	got, err := Merge(allLegs(t, source), merged)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(got.Artifacts) != len(want.Artifacts) {
		t.Fatalf("artifact count %d != %d", len(got.Artifacts), len(want.Artifacts))
	}
	assertSameReleaseTree(t, full, merged, want)
	for _, name := range []string{"release-manifest.json", "SHA256SUMS"} {
		a, _ := os.ReadFile(filepath.Join(full, name))
		b, _ := os.ReadFile(filepath.Join(merged, name))
		if string(a) != string(b) || len(a) == 0 {
			t.Fatalf("%s differs between full build and merge", name)
		}
	}
}

func TestMerge_rejects_missing_platform(t *testing.T) {
	source := writeClientSource(t)
	legs := allLegs(t, source)[:3]
	if _, err := Merge(legs, filepath.Join(t.TempDir(), "out")); err == nil || !strings.Contains(err.Error(), "artifact count") {
		t.Fatalf("expected missing-platform rejection, got %v", err)
	}
}

func TestMerge_rejects_duplicate_platform(t *testing.T) {
	source := writeClientSource(t)
	legs := append(allLegs(t, source), buildLeg(t, source, "windows/amd64"))
	if _, err := Merge(legs, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected duplicate rejection")
	}
}

func TestMerge_rejects_tampered_leg_archive(t *testing.T) {
	source := writeClientSource(t)
	legs := allLegs(t, source)
	name := ArtifactName(Target{Product: "acr-api", GOOS: "linux", GOARCH: "amd64"}, testIdentity().Version)
	if err := os.WriteFile(filepath.Join(legs[0], name), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Merge(legs, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected checksum rejection")
	}
}

func TestParseTargets_rejects_unknown_platform(t *testing.T) {
	if _, err := ParseTargets("plan9/amd64"); err == nil {
		t.Fatal("expected rejection")
	}
}
