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

// schemaRequiringANewField adds "c" to properties and to required at once: a
// field new since any baseline built from schemaOneRequired, so always refused.
var schemaRequiringANewField = strings.Replace(strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","c"]`, 1), `"b":{}`, `"b":{},"c":{}`, 1)

func commitSchema(t *testing.T, dir, schema string) {
	t.Helper()
	writeBaseline(t, dir, "contracts/jsonschema/v1/a.v1.schema.json", schema)
	runGit(t, dir, "commit", "-q", "-am", "change")
}

// A field that is new since the baseline cannot be required in the same
// release that adds it: "c" is added to properties and to required at once.
func TestRequiredBaselineRejectsNewlyRequiredField(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","c"]`, 1), `"b":{}`, `"b":{},"c":{}`, 1))
	var out bytes.Buffer
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir, Out: &out})
	if err == nil {
		t.Fatal("expected newly required field to fail")
	}
	for _, want := range []string{"a.v1.schema.json", `"c"`, "v1.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}

func TestRequiredBaselineRejectsNewlyRequiredNestedField(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(strings.Replace(schemaOneRequired, `"required":["x"]`, `"required":["x","z"]`, 1), `"y":{}`, `"y":{},"z":{}`, 1))
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), "/properties/n") || !strings.Contains(err.Error(), `"z"`) {
		t.Fatalf("expected nested failure, got %v", err)
	}
}

// CHAOS-12218: the documented rule. A field the baseline declared in
// properties (optional for at least that release) may be required now.
func TestRequiredBaselineAllowsRequiringAFieldTheBaselineDeclared(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","b"]`, 1))
	var out bytes.Buffer
	if err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir, Out: &out}); err != nil {
		t.Fatalf("requiring a field the baseline declared optional was refused: %v", err)
	}
	if !strings.Contains(out.String(), "v1.0.0") {
		t.Fatalf("output names no tag: %q", out.String())
	}
}

func TestRequiredBaselineAllowsRequiringANestedFieldTheBaselineDeclared(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(schemaOneRequired, `"required":["x"]`, `"required":["x","y"]`, 1))
	if err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir}); err != nil {
		t.Fatalf("requiring a nested field the baseline declared optional was refused: %v", err)
	}
}

// Declared means declared at the SAME path: "b" is a root property at the
// baseline, but the nested object never declared it.
func TestRequiredBaselineRejectsAFieldDeclaredOnlyAtAnotherPath(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(strings.Replace(schemaOneRequired, `"required":["x"]`, `"required":["x","b"]`, 1), `"y":{}`, `"y":{},"b":{}`, 1))
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), "/properties/n") || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("a field declared only at another path was allowed: %v", err)
	}
}

// The other direction: "x" is declared only inside the nested object at the
// baseline, so requiring it at the root is requiring a new root field.
func TestRequiredBaselineRejectsAFieldDeclaredOnlyInANestedObject(t *testing.T) {
	dir := baselineRepo(t, schemaOneRequired)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, strings.Replace(strings.Replace(schemaOneRequired, `"required":["a"]`, `"required":["a","x"]`, 1), `"b":{}`, `"b":{},"x":{}`, 1))
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), `"x"`) || !strings.Contains(err.Error(), `at "/"`) {
		t.Fatalf("a root field declared only in a nested object at the baseline was allowed: %v", err)
	}
}

// Codex #1816 r1 P2: a baseline schema of true accepts any value, so an
// object that now requires a field there is a new constraint on bodies the
// baseline already allowed.
func TestRequiredBaselineRejectsAFieldRequiredWhereTheBaselineAcceptedAnything(t *testing.T) {
	dir := baselineRepo(t, `{"type":"object","properties":{"box":true}}`)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, `{"type":"object","properties":{"box":{"type":"object","required":["id"],"properties":{"id":{}}}}}`)
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), `"id"`) || !strings.Contains(err.Error(), "/properties/box") {
		t.Fatalf("a field required where the baseline accepted anything was allowed: %v", err)
	}
}

// A property whose schema was false was forbidden at the baseline, never
// optional: no producer could send it, so it cannot become required.
func TestRequiredBaselineRejectsRequiringAFieldTheBaselineForbade(t *testing.T) {
	dir := baselineRepo(t, `{"type":"object","required":["a"],"properties":{"a":{},"b":false}}`)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, `{"type":"object","required":["a","b"],"properties":{"a":{},"b":{}}}`)
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("requiring a field the baseline forbade was allowed: %v", err)
	}
}

// The asymmetry: a baseline of false allowed no value at all, so no producer
// sent the field; an object that now requires fields there is a new
// subschema, as when the path was absent.
func TestRequiredBaselineAllowsAnObjectWhereTheBaselineForbadeAnyValue(t *testing.T) {
	dir := baselineRepo(t, `{"type":"object","properties":{"box":false}}`)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, `{"type":"object","properties":{"box":{"type":"object","required":["id"],"properties":{"id":{}}}}}`)
	if err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir}); err != nil {
		t.Fatalf("a new object where the baseline allowed nothing was refused: %v", err)
	}
}

// A baseline subschema with no properties declares nothing.
func TestRequiredBaselineRejectsWhenTheBaselineDeclaredNoProperties(t *testing.T) {
	const bare = `{"type":"object","required":["a"]}`
	dir := baselineRepo(t, bare)
	runGit(t, dir, "tag", "v1.0.0")
	commitSchema(t, dir, `{"type":"object","required":["a","b"],"properties":{"a":{},"b":{}}}`)
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("a field the baseline never declared was allowed: %v", err)
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
	commitSchema(t, dir, schemaRequiringANewField)
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
	commitSchema(t, dir, schemaRequiringANewField)
	err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
	if err == nil || !strings.Contains(err.Error(), "v1.0.0") {
		t.Fatalf("expected v1.0.0 fallback, got %v", err)
	}
}
