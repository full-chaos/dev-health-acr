package contextfabric

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every non-test file that serves a document through the completeness
// authority also applies the served-status floors. A new serving point that
// calls the authority and skips the floors fails here. The measurement-only
// shapes below measure documents built by servedLateWriters, which applies both.
func TestEveryAuthorityServingPointAppliesTheServedStatusFloors(t *testing.T) {
	measureOnly := map[string]bool{
		"completeness_authority.go": true, "budget_trim.go": true, "chaos4636_budget_stage3.go": true,
	}
	roots := []string{".", filepath.Join("..", "api")}
	for _, root := range roots {
		files, err := filepath.Glob(filepath.Join(root, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") || measureOnly[filepath.Base(file)] {
				continue
			}
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			if strings.Contains(text, "ApplyServerCompletenessAuthority(") && !strings.Contains(text, "ApplyServedStatusFloors(") {
				t.Errorf("%s serves through the completeness authority without ApplyServedStatusFloors", file)
			}
		}
	}
}
