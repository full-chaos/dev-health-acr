package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The closed limit token is a member of the published error schema, not only
// a Go constant: the schema's enum and the Go vocabulary must be one list.
func TestRateLimitLimitTokenMatchesTheErrorSchemaEnum(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "error.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Error struct {
				Properties struct {
					Details struct {
						Properties map[string]struct {
							Enum []string `json:"enum"`
						} `json:"properties"`
					} `json:"details"`
				} `json:"properties"`
			} `json:"error"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	enum := schema.Properties.Error.Properties.Details.Properties[RateLimitDetailsLimitKey].Enum
	if len(enum) == 0 {
		t.Fatalf("error.v1 schema declares no details.%s enum", RateLimitDetailsLimitKey)
	}
	for _, token := range enum {
		if !IsRateLimitLimit(token) {
			t.Errorf("schema token %q is not in the Go vocabulary", token)
		}
	}
	if !slices.Contains(enum, RateLimitRequestBudget) {
		t.Errorf("Go token %q is not in the schema enum %v", RateLimitRequestBudget, enum)
	}
}
