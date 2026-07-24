package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestVerifySeedHashes_UnlistedFileOnDiskFails is Codex finding 6: the shell seeder globs
// every *.sql under seed/clickhouse and executes it, but this tool previously only hashed the
// files fixture-manifest.json happened to list -- an extra, unlisted file would run against
// the live database completely unverified. A manifest that lists fewer files than exist on
// disk must fail loudly instead.
func TestVerifySeedHashes_UnlistedFileOnDiskFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "001_listed.sql"), "SELECT 1;")
	writeFile(t, filepath.Join(dir, "002_unlisted.sql"), "SELECT 2;")
	hash, err := sha256File(filepath.Join(dir, "001_listed.sql"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := fixtureManifest{SeedFiles: []fixtureSeedFile{{Path: "001_listed.sql", SHA256: hash}}}

	checks, err := verifySeedHashes(manifest, dir)
	if err != nil {
		t.Fatalf("verifySeedHashes: %v", err)
	}
	var bijectionFailed bool
	for _, c := range checks {
		if strings.Contains(c.Path, "bijection") && !c.OK {
			bijectionFailed = true
			if !strings.Contains(c.Message, "002_unlisted.sql") {
				t.Fatalf("bijection failure message should name the unlisted file, got %q", c.Message)
			}
		}
	}
	if !bijectionFailed {
		t.Fatal("expected the bijection check to fail: 002_unlisted.sql exists on disk but is not manifest-listed")
	}
}

// TestVerifySeedHashes_ManifestListsAMissingFileFails is the other half: a manifest entry
// with no corresponding file on disk must also fail the bijection check, not just the
// per-file hash check (which already fails it, but the bijection check should name it too).
func TestVerifySeedHashes_ManifestListsAMissingFileFails(t *testing.T) {
	dir := t.TempDir()
	manifest := fixtureManifest{SeedFiles: []fixtureSeedFile{{Path: "999_missing.sql", SHA256: "deadbeef"}}}

	checks, err := verifySeedHashes(manifest, dir)
	if err != nil {
		t.Fatalf("verifySeedHashes: %v", err)
	}
	var bijectionFailed bool
	for _, c := range checks {
		if strings.Contains(c.Path, "bijection") && !c.OK {
			bijectionFailed = true
		}
	}
	if !bijectionFailed {
		t.Fatal("expected the bijection check to fail: 999_missing.sql is manifest-listed but absent on disk")
	}
}

func TestVerifySeedHashes_ExactMatchPasses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "001_only.sql"), "SELECT 1;")
	hash, err := sha256File(filepath.Join(dir, "001_only.sql"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := fixtureManifest{SeedFiles: []fixtureSeedFile{{Path: "001_only.sql", SHA256: hash}}}

	checks, err := verifySeedHashes(manifest, dir)
	if err != nil {
		t.Fatalf("verifySeedHashes: %v", err)
	}
	for _, c := range checks {
		if !c.OK {
			t.Fatalf("expected every check to pass for an exact bijection with a matching hash, got %+v", c)
		}
	}
}

// TestLoadFixtureManifest_RejectsEmptySections is the manifest half of Codex finding 6: a
// manifest with empty scope_probes/expected_row_counts/etc. would otherwise silently produce
// an empty (therefore vacuously passing) check slice.
func TestLoadFixtureManifest_RejectsEmptySections(t *testing.T) {
	base := map[string]any{
		"schema_version":  "fullstack_fixture_manifest.v1",
		"fixture_version": "2026-07-23.1",
	}
	full := func(overrides map[string]any) string {
		m := map[string]any{
			"source_corpus":       map[string]any{"consumed_files": []any{map[string]any{"path": "a", "sha256": "b"}}},
			"seed_files":          []any{map[string]any{"path": "a.sql", "sha256": "b"}},
			"scope_probes":        []any{map[string]any{"description": "d", "query": "q", "expected": 1}},
			"expected_row_counts": map[string]any{"repos": map[string]any{"repos": 1}},
		}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range overrides {
			m[k] = v
		}
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	cases := []struct {
		name      string
		overrides map[string]any
		wantErr   bool
	}{
		{"fully populated", nil, false},
		{"empty consumed_files", map[string]any{"source_corpus": map[string]any{"consumed_files": []any{}}}, true},
		{"empty seed_files", map[string]any{"seed_files": []any{}}, true},
		{"empty scope_probes", map[string]any{"scope_probes": []any{}}, true},
		{"empty expected_row_counts", map[string]any{"expected_row_counts": map[string]any{}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "fixture-manifest.json")
			writeFile(t, path, full(tc.overrides))
			_, err := loadFixtureManifest(path)
			if tc.wantErr && err == nil {
				t.Fatal("expected loadFixtureManifest to reject this manifest")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected loadFixtureManifest to accept a fully populated manifest, got: %v", err)
			}
		})
	}
}

// TestResolveProbeWords_PrefersArgvFile is the case the team lead built --probe-command-file
// for: the caller's `compose` is a shell function, so the shell-quoted --probe-command form
// can never express it (exec'ing the literal word "compose" fails with "executable file not
// found in $PATH"). When an argv file is given, it must win outright, even if --probe-command
// is also set (belt-and-braces callers, or a stale flag left over from a partial migration).
func TestResolveProbeWords_PrefersArgvFile(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "probe-argv")
	argv := []byte("docker\x00compose\x00-p\x00acr-fs-test\x00exec\x00-T\x00clickhouse\x00clickhouse-client\x00--query\x00")
	if err := os.WriteFile(argvFile, argv, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveProbeWords(argvFile, "compose exec -T clickhouse clickhouse-client --query")
	if err != nil {
		t.Fatalf("resolveProbeWords: %v", err)
	}
	want := []string{"docker", "compose", "-p", "acr-fs-test", "exec", "-T", "clickhouse", "clickhouse-client", "--query"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveProbeWords() = %#v, want %#v", got, want)
	}
}

func TestResolveProbeWords_ArgvFileIgnoresEmptyTrailingSegment(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "probe-argv")
	// A trailing NUL (as a writer that does `printf '%s\0'` per word naturally produces)
	// must not become a spurious empty final argument.
	if err := os.WriteFile(argvFile, []byte("a\x00b\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveProbeWords(argvFile, "")
	if err != nil {
		t.Fatalf("resolveProbeWords: %v", err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveProbeWords() = %#v, want %#v", got, want)
	}
}

func TestResolveProbeWords_EmptyArgvFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "probe-argv")
	if err := os.WriteFile(argvFile, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveProbeWords(argvFile, ""); err == nil {
		t.Fatal("expected an error for an argv file holding no arguments")
	}
}

func TestResolveProbeWords_MissingArgvFileIsAnError(t *testing.T) {
	if _, err := resolveProbeWords(filepath.Join(t.TempDir(), "does-not-exist"), ""); err == nil {
		t.Fatal("expected an error for a nonexistent argv file")
	}
}

// TestResolveProbeWords_FallsBackToShellQuoted covers the older, still-supported form: no
// argv file given, fall back to splitting the shell-quoted --probe-command string.
func TestResolveProbeWords_FallsBackToShellQuoted(t *testing.T) {
	got, err := resolveProbeWords("", "clickhouse-client --user default --password ch --query")
	if err != nil {
		t.Fatalf("resolveProbeWords: %v", err)
	}
	want := []string{"clickhouse-client", "--user", "default", "--password", "ch", "--query"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveProbeWords() = %#v, want %#v", got, want)
	}
}

func TestResolveProbeWords_MalformedShellQuotedStringIsAnError(t *testing.T) {
	if _, err := resolveProbeWords("", "unterminated 'quote"); err == nil {
		t.Fatal("expected an error for an unterminated quote")
	}
}
