package v1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositorySlugPatternAcceptsTwoToTwentySegments(t *testing.T) {
	for slug, want := range map[string]bool{
		"acme/web":                     true,
		"acme/grp/web":                 true,
		strings.Repeat("g/", 19) + "x": true,
		strings.Repeat("g/", 20) + "x": false,
		"acme":                         false,
		"acme//web":                    false,
		"acme/grp/":                    false,
		"acme/g rp/web":                false,
	} {
		if got := repositorySlugPattern.MatchString(slug); got != want {
			t.Errorf("repositorySlugPattern(%q) = %v, want %v", slug, got, want)
		}
	}
}

// No published schema, canonical or embedded, may keep the two-part slug
// pattern: a nested repository key would pass the guard and fail the schema.
func TestNoSchemaKeepsTheTwoPartRepositorySlugPattern(t *testing.T) {
	roots := []string{"../../../contracts/jsonschema/v1", "../../mcp/schemas"}
	for _, root := range roots {
		files, err := filepath.Glob(filepath.Join(root, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no schemas under %s: %v", root, err)
		}
		for _, file := range files {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, old := range []string{`^[^/\\s]+/[^/\\s]+$`, `^[^/*\\s]+/[^/*\\s]+$`} {
				if strings.Contains(string(raw), old) {
					t.Errorf("%s still holds the two-part slug pattern %s", file, old)
				}
			}
		}
	}
}
