package devhealthfacts

import (
	"context"
	"testing"
)

// Codex r3 on #1962: a project-reached repository whose linked effort is zero in
// every theme draws no row, so its name does not decide the table. The linked
// mix includes such repositories (only evidence-gated units, say).
func TestCHAOS12580AZeroEffortRepositoryWithNoNameDoesNotWithholdTheTable(t *testing.T) {
	t.Parallel()
	owned := map[string]*repoThemeTotals{"repo-A": {theme: map[string]float64{"feature_delivery": 3}}}
	linked := map[string]map[string]float64{"repo-L": {"feature_delivery": 0, "quality": 0}}
	names := map[string]string{"repo-A": "acme/api"}
	links, ok := teamRepositoryThemeLinks(context.Background(), "T", []string{"repo-A"}, owned, linked, names)
	if !ok {
		t.Fatal("a nameless repository with no effort withheld the table")
	}
	if len(links) != 1 || links[0].repoID != "repo-A" {
		t.Fatalf("links = %+v, want the one owned link", links)
	}
	// The same repository with effort still needs its name.
	linked["repo-L"]["quality"] = 1
	if _, ok := teamRepositoryThemeLinks(context.Background(), "T", []string{"repo-A"}, owned, linked, names); ok {
		t.Fatal("a nameless repository with effort was served")
	}
}
