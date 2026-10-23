package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The closed deadline token is a member of the published error schema, not only
// a Go constant: the schema's enum and the Go vocabulary must be one list.
func TestDeadlineTokenMatchesTheErrorSchemaEnum(t *testing.T) {
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
	enum := schema.Properties.Error.Properties.Details.Properties[DeadlineDetailsKey].Enum
	if len(enum) == 0 {
		t.Fatalf("error.v1 schema declares no details.%s enum", DeadlineDetailsKey)
	}
	want := slices.Clone(Deadlines())
	got := slices.Clone(enum)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("schema enum %v and Go vocabulary %v differ", got, want)
	}
	for _, token := range want {
		if !isDeadline(token) {
			t.Errorf("vocabulary member %q is rejected by isDeadline", token)
		}
	}
}

func isDeadline(token string) bool { return slices.Contains(Deadlines(), token) }
