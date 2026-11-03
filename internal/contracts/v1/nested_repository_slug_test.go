package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/reposlug"
	"github.com/full-chaos/dev-health-acr/internal/reposlug/cells"
)

// Every Go contract validator that holds a repository key answers the shared
// cell table the way reposlug does.
func TestEveryGoSlugValidatorHoldsTheCellTable(t *testing.T) {
	validators := map[string]func(slug string) bool{
		"RepositoryRef.Validate":      func(s string) bool { return RepositoryRef{Slug: s}.Validate() == nil },
		"MCPRepositoryRef.Validate":   func(s string) bool { return MCPRepositoryRef{Slug: s}.Validate() == nil },
		"validateBoundedRepositories": func(s string) bool { return validateBoundedRepositoryScopes([]string{s}) == nil },
		"ResolvedScope.validate": func(s string) bool {
			err := ResolvedScope{RepoID: "repo-1", RepoSlug: s}.validate(false)
			return err == nil || !strings.Contains(err.Error(), "repo_slug")
		},
	}
	for name, accepts := range validators {
		for _, cell := range cells.Table() {
			if got := accepts(cell.Slug); got != cell.Valid {
				t.Errorf("%s: %s on %q = %v, want %v", name, cell.Name, cell.Slug, got, cell.Valid)
			}
		}
	}
}

// Every published schema location that holds a repository key, canonical and
// embedded, carries the one reposlug pattern and a 255 maxLength, and the
// pattern answers the cell table.
func TestEverySchemaSlugLocationHoldsTheCellTable(t *testing.T) {
	pattern := regexp.MustCompile(reposlug.SchemaSlugPattern)
	for root, minimum := range map[string]int{"../../../contracts/jsonschema/v1": 12, "../../mcp/schemas": 4} {
		files, err := filepath.Glob(filepath.Join(root, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no schemas under %s: %v", root, err)
		}
		locations := 0
		for _, file := range files {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var document any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			walkSchema(document, "$", func(path string, node map[string]any) {
				text, _ := node["pattern"].(string)
				if text != reposlug.SchemaSlugPattern && !strings.HasPrefix(text, "^[^/") {
					return
				}
				locations++
				if text != reposlug.SchemaSlugPattern {
					t.Errorf("%s %s: pattern %q is not the reposlug pattern", file, path, text)
				}
				if limit, _ := node["maxLength"].(float64); int(limit) != reposlug.MaxSlugBytes {
					t.Errorf("%s %s: maxLength = %v, want %d", file, path, node["maxLength"], reposlug.MaxSlugBytes)
				}
			})
		}
		if locations < minimum {
			t.Errorf("%s: %d slug locations found, want at least %d", root, locations, minimum)
		}
	}
	for _, cell := range cells.Table() {
		if got := pattern.MatchString(cell.Slug) && len(cell.Slug) <= reposlug.MaxSlugBytes; got != cell.Valid {
			t.Errorf("%s: schema pattern on %q = %v, want %v", cell.Name, cell.Slug, got, cell.Valid)
		}
	}
}

func walkSchema(node any, path string, visit func(path string, node map[string]any)) {
	switch typed := node.(type) {
	case map[string]any:
		visit(path, typed)
		for key, child := range typed {
			walkSchema(child, path+"/"+key, visit)
		}
	case []any:
		for _, child := range typed {
			walkSchema(child, path+"/[]", visit)
		}
	}
}
