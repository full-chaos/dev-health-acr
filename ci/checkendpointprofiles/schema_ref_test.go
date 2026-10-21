package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ops-owned credential-classes schema $refs a sibling schema file by a
// path relative to its own $id (jsonschema/endpoint-auth-profile.v1.schema.json).
// The gate compiled that schema standalone, so the first ops pin that carries
// the $ref could not be adopted. These fixtures reproduce the shape with
// self-contained files: the root schema borrows one property from a second
// schema file under a jsonschema/ directory beside it.

const refRootSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://example.test/contracts/auth/v1/root.schema.json",
  "type": "object",
  "properties": {
    "kind": { "$ref": "jsonschema/shared.v1.schema.json#/properties/kind" }
  }
}`

const refSharedSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://example.test/contracts/auth/v1/jsonschema/shared.v1.schema.json",
  "type": "object",
  "properties": {
    "kind": { "type": "string", "enum": ["jwt_header_typ", "none"] }
  }
}`

func seedRefFixture(t *testing.T, withShared bool) string {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root.schema.json")
	writeFile(t, root, refRootSchema)
	if withShared {
		writeFile(t, filepath.Join(dir, "jsonschema", "shared.v1.schema.json"), refSharedSchema)
	}
	return root
}

func TestSchemaRefToASiblingSchemaFileIsResolvedAndApplied(t *testing.T) {
	root := seedRefFixture(t, true)

	var errs []string
	if err := validateAgainstSchema(root, map[string]any{"kind": "jwt_header_typ"}, "REF", &errs); err != nil {
		t.Fatalf("a schema whose $ref points at a present sibling file must resolve: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("a document the referenced enum allows was reported: %v", errs)
	}

	errs = nil
	if err := validateAgainstSchema(root, map[string]any{"kind": "not_a_kind"}, "REF", &errs); err != nil {
		t.Fatal(err)
	}
	if len(errs) == 0 {
		t.Fatal("a document the referenced enum forbids was accepted: the $ref resolved but was not applied")
	}
}

func TestSchemaRefThroughASymlinkToAFileOutsideTheSchemaDirectoryFailsTheGate(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "sub", "root.schema.json")
	writeFile(t, root, refRootSchema)
	// The linked file allows a value the real sibling schema forbids.
	writeFile(t, filepath.Join(dir, "outside.schema.json"), strings.Replace(refSharedSchema, `["jwt_header_typ", "none"]`, `["not_a_kind"]`, 1))
	if err := os.MkdirAll(filepath.Join(dir, "sub", "jsonschema"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside.schema.json"), filepath.Join(dir, "sub", "jsonschema", "shared.v1.schema.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var errs []string
	err := validateAgainstSchema(root, map[string]any{"kind": "not_a_kind"}, "REF", &errs)
	if err == nil {
		t.Fatalf("a $ref satisfied through a symlink to a file outside the schema directory was accepted; errs=%v", errs)
	}
	if !strings.Contains(err.Error(), "shared.v1.schema.json") {
		t.Fatalf("the error must name the reference: %v", err)
	}
}

func TestSchemaRefToAMissingSiblingFileFailsTheGate(t *testing.T) {
	root := seedRefFixture(t, false)

	var errs []string
	err := validateAgainstSchema(root, map[string]any{"kind": "not_a_kind"}, "REF", &errs)
	if err == nil {
		t.Fatalf("a $ref whose file was not fetched must be an error, not a clean run; errs=%v", errs)
	}
	if !strings.Contains(err.Error(), "shared.v1.schema.json") {
		t.Fatalf("the error must name the file that was not found: %v", err)
	}
}

func TestSchemaRefOutsideTheSchemasOwnIDBaseFailsTheGate(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root.schema.json")
	writeFile(t, root, strings.Replace(refRootSchema, `"jsonschema/shared.v1.schema.json#`, `"https://elsewhere.test/contracts/auth/v1/jsonschema/shared.v1.schema.json#`, 1))
	// A file that happens to exist must not be picked up for a foreign host.
	writeFile(t, filepath.Join(dir, "jsonschema", "shared.v1.schema.json"), refSharedSchema)
	if err := os.WriteFile(filepath.Join(dir, "shared.v1.schema.json"), []byte(refSharedSchema), 0o600); err != nil {
		t.Fatal(err)
	}

	var errs []string
	err := validateAgainstSchema(root, map[string]any{"kind": "none"}, "REF", &errs)
	if err == nil {
		t.Fatalf("a $ref to a URI outside the schema's own $id base must not be loaded from disk; errs=%v", errs)
	}
	if !strings.Contains(err.Error(), "elsewhere.test") {
		t.Fatalf("the error must name the reference: %v", err)
	}
}

func TestSchemaRefCannotEscapeTheSchemaDirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "sub", "root.schema.json")
	writeFile(t, root, strings.Replace(refRootSchema, `"jsonschema/shared.v1.schema.json#`, `"jsonschema/../../outside.v1.schema.json#`, 1))
	writeFile(t, filepath.Join(dir, "outside.v1.schema.json"), refSharedSchema)

	var errs []string
	err := validateAgainstSchema(root, map[string]any{"kind": "none"}, "REF", &errs)
	if err == nil {
		t.Fatalf("a $ref that climbs out of the schema's $id base must be refused; errs=%v", errs)
	}
	if !strings.Contains(err.Error(), "outside.v1.schema.json") {
		t.Fatalf("the error must name the reference: %v", err)
	}
}

// The loader is handed whatever URI the resolver builds, so it must refuse an
// uncleaned path that climbs out of the base even though the resolver normally
// cleans it first.
func TestSiblingSchemaLoaderRefusesAnUncleanedPathThatClimbsOutOfTheBase(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "sub", "root.schema.json")
	writeFile(t, root, refRootSchema)
	writeFile(t, filepath.Join(dir, "outside.v1.schema.json"), refSharedSchema)

	load := siblingSchemaLoader(root, "https://example.test/contracts/auth/v1/root.schema.json")
	for _, p := range []string{
		"/contracts/auth/v1/../outside.v1.schema.json",
		"/contracts/auth/v1/jsonschema/../../outside.v1.schema.json",
		"/contracts/auth/v1/..",
	} {
		if s, err := load(&url.URL{Scheme: "https", Host: "example.test", Path: p}); err == nil {
			t.Errorf("loader accepted %q and returned %v", p, s)
		}
	}
	if _, err := load(&url.URL{Scheme: "https", Host: "example.test", Path: "/contracts/auth/v1/root.schema.json"}); err != nil {
		t.Errorf("loader refused a file beside the root schema: %v", err)
	}
}
