package answerprojection

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	canonicalExample  = "context_fabric_investigation_result.v1.json"
	projectionExample = "context_fabric_answer_projection.v1.json"
)

func examplePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "contracts", "examples", "v1", name)
}

// TestGoldenProjectionMatchesCanonicalExample pins the golden projection
// fixture to the golden investigation-result fixture: the published
// projection example is exactly what Project produces from the published
// result example, at the default budget.
//
// This makes the example pair a contract fact rather than two documents
// that merely look consistent. If the projection logic changes, the shipped
// example stops matching and this fails, instead of the repository quietly
// publishing an example no code would ever emit.
//
// Set ACR_WRITE_FIXTURE=1 to regenerate the projection example after a
// deliberate projection change.
func TestGoldenProjectionMatchesCanonicalExample(t *testing.T) {
	raw, err := os.ReadFile(examplePath(t, canonicalExample))
	if err != nil {
		t.Fatalf("read canonical example: %v", err)
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode canonical example: %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("canonical example is not a valid result: %v", err)
	}

	projection := Project(result, Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection of the canonical example is invalid: %v", err)
	}

	encoded, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		t.Fatalf("encode projection: %v", err)
	}
	encoded = append(encoded, '\n')

	path := examplePath(t, projectionExample)
	if os.Getenv("ACR_WRITE_FIXTURE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write projection example: %v", err)
		}
		t.Logf("regenerated %s", projectionExample)
		return
	}

	golden, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read projection example (set ACR_WRITE_FIXTURE=1 to generate): %v", err)
	}
	// Compare decoded values rather than bytes: the pin is about
	// contract content, not about which encoder wrote the file.
	var want, got any
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatalf("decode golden projection example: %v", err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode generated projection: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("golden projection example has drifted from Project's output; rerun with ACR_WRITE_FIXTURE=1 if the change was deliberate")
	}
}
