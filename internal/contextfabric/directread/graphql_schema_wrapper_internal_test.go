package directread

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The embedded SDL wrapper is GENERATED from the vendored SDL: its bytes
// equal EncodeOpsSchemaFile(contracts/mcp/ops-catalogue/schema.graphql), its
// digest is the catalogue's (fdff794c...), and a wrapper whose SDL does not
// match its own digest, or names another contract, yields no SDL and no
// policy (fail closed).
func TestEmbeddedOpsSchemaWrapperIsGeneratedAndFailsClosed(t *testing.T) {
	vendored, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "mcp", "ops-catalogue", "schema.graphql"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := EncodeOpsSchemaFile(vendored, "contracts/mcp/ops-catalogue/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, embeddedOpsSchemaFile) {
		t.Fatal("ops_schema.v1.json is not the generation of the vendored SDL: run go run ./cmd/operationpolicy")
	}
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	const pinned = "sha256:fdff794c3fa3de956e07061645b7494cca33ed760f9405d405c912ae01d3e34b"
	if SchemaDigestOf(EmbeddedOpsSchema()) != pinned || cat.SchemaDigest() != pinned || SchemaDigestOf(vendored) != pinned {
		t.Fatalf("digests differ: embedded %s, catalogue %s, vendored %s", SchemaDigestOf(EmbeddedOpsSchema()), cat.SchemaDigest(), SchemaDigestOf(vendored))
	}

	saved := embeddedOpsSchemaFile
	t.Cleanup(func() { embeddedOpsSchemaFile = saved })
	tampered := bytes.Replace(saved, []byte("enum AIAttributionBucketInput"), []byte("enum AIAttributionBucketInpuX"), 1)
	contract := bytes.Replace(saved, []byte(OpsSchemaContract), []byte("acr.mcp.ops_schema.v2"), 1)
	for name, file := range map[string][]byte{"sdl edited, digest kept": tampered, "other contract": contract, "not json": []byte("{")} {
		embeddedOpsSchemaFile = file
		if got := EmbeddedOpsSchema(); got != nil {
			t.Fatalf("%s: a bad wrapper yielded %d SDL bytes", name, len(got))
		}
		if _, err := NewGraphQLPolicy(cat, EmbeddedOpsSchema(), DefaultGraphQLLimits()); !errors.Is(err, ErrGraphQLPolicyInvalid) {
			t.Fatalf("%s: a policy derived over a bad wrapper: %v", name, err)
		}
	}
}
