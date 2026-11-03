package devhealthfacts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// chaos12639_column_words_test.go pins the cell words the producers declare for
// their tables (CHAOS-12639). It reads each provider's published declaration as
// JSON, the form a declaration leaves the producer in, so the test needs no
// symbol of the change itself.

type declaredColumn struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	SubjectRef json.RawMessage `json:"subject_ref"`
	Vocabulary string          `json:"vocabulary"`
	WordColumn string          `json:"word_column"`
}

type declaredField struct {
	Name    string           `json:"name"`
	Columns []declaredColumn `json:"columns"`
}

type declaredCapability struct {
	Kind   string          `json:"kind"`
	Fields []declaredField `json:"fields"`
}

func publishedDeclarations(t *testing.T) []declaredCapability {
	t.Helper()
	var out []declaredCapability
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		raw, err := json.Marshal(provider.Capability())
		if err != nil {
			t.Fatalf("marshal capability: %v", err)
		}
		var capability declaredCapability
		if err := json.Unmarshal(raw, &capability); err != nil {
			t.Fatalf("unmarshal capability: %v", err)
		}
		out = append(out, capability)
	}
	if len(out) == 0 {
		t.Fatal("no provider is published: this measured nothing")
	}
	return out
}

// Every table's theme column is declared in the investment theme vocabulary, so
// an answer serves its words.
func TestCHAOS12639EveryThemeColumnDeclaresTheThemeVocabulary(t *testing.T) {
	t.Parallel()
	themes := 0
	for _, capability := range publishedDeclarations(t) {
		for _, field := range capability.Fields {
			for _, column := range field.Columns {
				if column.Name != "theme" {
					continue
				}
				themes++
				if column.Vocabulary != "investment_theme" {
					t.Errorf("%s.%s column theme declares vocabulary %q, want investment_theme", capability.Kind, field.Name, column.Vocabulary)
				}
			}
		}
	}
	if themes == 0 {
		t.Fatal("no table declares a theme column: this measured nothing")
	}
}

// Every subject id column with a name beside it (<x>_id and <x>_name) names that
// column as its word column, so a reader sees the name and not the id.
func TestCHAOS12639EverySubjectIDBesideItsNameDeclaresTheWordColumn(t *testing.T) {
	t.Parallel()
	pairs := 0
	for _, capability := range publishedDeclarations(t) {
		for _, field := range capability.Fields {
			names := map[string]bool{}
			for _, column := range field.Columns {
				names[column.Name] = true
			}
			for _, column := range field.Columns {
				base, isID := strings.CutSuffix(column.Name, "_id")
				if !isID || len(column.SubjectRef) == 0 || !names[base+"_name"] {
					continue
				}
				pairs++
				if column.WordColumn != base+"_name" {
					t.Errorf("%s.%s column %s declares word column %q, want %s_name", capability.Kind, field.Name, column.Name, column.WordColumn, base)
				}
			}
		}
	}
	if pairs == 0 {
		t.Fatal("no table holds an id beside its name: this measured nothing")
	}
}
