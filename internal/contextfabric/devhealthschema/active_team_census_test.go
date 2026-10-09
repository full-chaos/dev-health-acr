package devhealthschema

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var teamsReadPattern = regexp.MustCompile("(?:FROM|JOIN) teams\\b")

// The projector read lists inactive teams on purpose (an explicit read of an
// inactive id needs the node to answer); every other read of teams must carry
// the active-team predicate.
var teamsReadExempt = map[string]string{
	"internal/contextfabric/devhealthsource/teams_projects.go":               "projector emits inactive nodes with property_is_active=false",
	"internal/contextfabric/devhealthfacts/investment_project_mix_phased.go": "input-source digest list, not a read",
}

func TestEveryTeamsReadCarriesTheActiveTeamPredicate(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var sites int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		src := string(raw)
		for _, loc := range teamsReadPattern.FindAllStringIndex(src, -1) {
			if _, exempt := teamsReadExempt[rel]; exempt {
				continue
			}
			sites++
			end := loc[1] + 400
			if end > len(src) {
				end = len(src)
			}
			window := src[loc[1]:end]
			if cut := strings.Index(window, "\n\n"); cut >= 0 {
				window = window[:cut]
			}
			if !strings.Contains(window, "ActiveTeamPredicate(") {
				t.Errorf("%s: teams read at byte %d lacks devhealthschema.ActiveTeamPredicate", rel, loc[0])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sites < 7 {
		t.Fatalf("census found %d teams reads, want at least 7 (the census itself drifted)", sites)
	}
}

func TestActiveTeamPredicateForm(t *testing.T) {
	if got := ActiveTeamPredicate(""); got != "is_active = 1" {
		t.Fatalf("unaliased = %q", got)
	}
	if got := ActiveTeamPredicate("t"); got != "t.is_active = 1" {
		t.Fatalf("aliased = %q", got)
	}
}
