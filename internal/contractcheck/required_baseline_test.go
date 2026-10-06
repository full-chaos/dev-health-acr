package contractcheck

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func baselineRepo(t *testing.T, schema string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "tag.gpgsign", "false")
	runGit(t, dir, "config", "user.name", "t")
	runGit(t, dir, "config", "user.email", "t@example.invalid")
	writeBaseline(t, dir, "go.mod", "module x\n")
	writeBaseline(t, dir, "contracts/jsonschema/v1/a.v1.schema.json", schema)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeBaseline(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const schemaOneRequired = `{"type":"object","required":["a"],"properties":{"a":{},"b":{},"n":{"type":"object","required":["x"],"properties":{"x":{},"y":{}}}}}`

func commitSchema(t *testing.T, dir, schema string) {
	t.Helper()
	writeBaseline(t, dir, "contracts/jsonschema/v1/a.v1.schema.json", schema)
	runGit(t, dir, "commit", "-q", "-am", "change")
}

func TestRequiredBaselineRejectsNewlyRequiredField(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","b"]`, 1))
	var out bytes.Buffer
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir, Out: &out})
	if err == nil {
		t.Fatal("expected newly required field to fail")
	}
	for _, want := range []string{"a.v1.schema.json", `"b"`, "v1.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}

func TestRequiredBaselineRejectsNewlyRequiredNestedField(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["x"]`, `"required":["x","y"]`, 1))
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), "/properties/n") || !strings.Contains(err.Error(), `"y"`) {
		t.Fatalf("expected nested failure, got %v", err)
	}
}

func TestRequiredBaselineAllowsFieldRequiredAtTag(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"b":{}`, `"b":{},"c":{}`, 1))
	var out bytes.Buffer
	if err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir, Out: &out}); err != nil {
		t.Fatalf("unexpected failure: %v", err)
	}
	if !strings.Contains(out.String(), "v1.0.0") {
		t.Fatalf("output names no tag: %q", out.String())
	}
}

func TestRequiredBaselineAllowsRequiredRemoval(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":[]`, 1))
	if err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir}); err != nil {
		t.Fatalf("unexpected failure: %v", err)
	}
}

func TestRequiredBaselineNoTagPassesLoudly(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","b"]`, 1))
	var out bytes.Buffer
	if err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir, Out: &out}); err != nil {
		t.Fatalf("unexpected failure: %v", err)
	}
	if !strings.Contains(out.String(), "NO RELEASE TAG") {
		t.Fatalf("no loud notice: %q", out.String())
	}
}

func TestRequiredBaselineSkipsTagAtHead(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","b"]`, 1))
	runGit(t, dir, "tag", "v1.1.0")
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), "v1.0.0") {
		t.Fatalf("expected comparison against v1.0.0, got %v", err)
	}
}

func TestRequiredBaselineProdRevTagBeatsVersionTag(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","b"]`, 1))
	runGit(t, dir, "tag", "prod-rev3")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","b","c"]`, 1))
	var out bytes.Buffer
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir, Out: &out})
	if err == nil || !strings.Contains(err.Error(), "prod-rev3") || strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("expected failure on c only against prod-rev3, got %v", err)
	}
}

func TestRequiredBaselinePicksHighestProdRevNumber(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "prod-rev9")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","b"]`, 1))
	runGit(t, dir, "tag", "prod-rev10")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","b","c"]`, 1))
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), "prod-rev10") || strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("expected prod-rev10 baseline, got %v", err)
	}
}

func TestRequiredBaselineFallsBackToVersionTagWithoutProdRev(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","b"]`, 1))
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), "v1.0.0") {
		t.Fatalf("expected v1.0.0 fallback, got %v", err)
	}
}
