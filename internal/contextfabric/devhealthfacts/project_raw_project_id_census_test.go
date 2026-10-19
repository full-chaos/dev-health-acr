package devhealthfacts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project is identified by provider and id; two providers can mint one raw
// id. No production statement or helper of this package may select, group or
// map a requested project by its raw id alone.
func TestNoProjectIsMappedByRawIDInTheScopeExpander(t *testing.T) {
	banned := []string{
		"projectOriginsByRawID",
		"decodeProjectOriginIDs",
		"requestedProjectIDsPredicate",
		"AND p.id IN {project_ids",
		"min(p.id)",
		"groupUniqArray(p.id)",
		"p.id AS origin_project_id",
	}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files found: %v", err)
	}
	scanned := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, token := range banned {
				if strings.Contains(line, token) {
					t.Errorf("%s maps projects by raw id: %q", file, token)
				}
			}
		}
	}
	if scanned < 10 {
		t.Fatalf("scanned %d production files, the census cannot see the package", scanned)
	}
}
