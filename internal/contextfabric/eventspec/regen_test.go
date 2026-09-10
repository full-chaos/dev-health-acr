package eventspec

import (
	"bytes"
	"os"
	"testing"
)

// TestGeneratedArtifactsRegenerateByteIdentically is CHAOS-5515 A1's proof
// that spec.go is the ONE declaration authority: the checked-in
// zz_generated.go and schema.json must be exactly what Generate() produces
// from spec.go right now, byte for byte. A hand-edit to either generated
// file -- the second-expected-member-list failure mode clause 1 forbids --
// makes this test fail, because the committed bytes stop matching the spec's
// own output.
func TestGeneratedArtifactsRegenerateByteIdentically(t *testing.T) {
	got, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	for _, name := range []string{"zz_generated.go", "schema.json"} {
		want, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read committed %s: %v (run `go generate ./internal/contextfabric/eventspec/...`)", name, err)
		}
		if !bytes.Equal(got[name], want) {
			t.Errorf("%s is stale or hand-edited: committed bytes differ from Generate()'s output.\nRun `go generate ./internal/contextfabric/eventspec/...` and commit the result.\n--- generated ---\n%s\n--- committed ---\n%s", name, got[name], want)
		}
	}
}
