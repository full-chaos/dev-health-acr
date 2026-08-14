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

	// The historical pair (CHAOS-3781 through CHAOS-3746). It exists
	// because the current-axis pair above cannot exercise the temporal
	// label at all: the label is nil on the current axis, so no assertion
	// over that example says anything about it.
	//
	// Publishing it is the point, not a side effect. Go's
	// ContextFabricAnswerProjection.Validate is the only thing that had
	// ever seen a temporal-carrying projection; contractcheck validates
	// every published example against its JSON SCHEMA, so this pair is
	// what proves the schema admits what Project actually emits for a
	// historical answer.
	canonicalHistoricalExample  = "context_fabric_investigation_result_historical.v1.json"
	projectionHistoricalExample = "context_fabric_answer_projection_historical.v1.json"
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
	for name, pair := range map[string][2]string{
		"current":    {canonicalExample, projectionExample},
		"historical": {canonicalHistoricalExample, projectionHistoricalExample},
	} {
		t.Run(name, func(t *testing.T) { assertGoldenProjectionPair(t, pair[0], pair[1]) })
	}
}

func assertGoldenProjectionPair(t *testing.T, canonicalExample, projectionExample string) {
	t.Helper()
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
