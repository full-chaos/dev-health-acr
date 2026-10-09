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
	"internal/contextfabric/devhealthschema/active_team.go":                  "the predicate builder itself",
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
			if !strings.Contains(window, "ActiveTeamPredicate(") && !strings.Contains(window, "InactiveTeamPredicate(") {
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

var teamDailyReadPattern = regexp.MustCompile("FROM (?:work_item_metrics_daily|estimate_coverage_metrics_daily)\\b")

// A read of a daily table keyed by team_id either names its teams (team_id IN
// the requested ids) or carries the scope predicate that drops superseded team
// ids; a new read with neither doubles a carried team.
func TestEveryTeamDailyReadNamesItsTeamsOrDropsInactiveOnes(t *testing.T) {
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
		for _, loc := range teamDailyReadPattern.FindAllStringIndex(src, -1) {
			lineStart := strings.LastIndex(src[:loc[0]], "\n") + 1
			if strings.HasPrefix(strings.TrimSpace(src[lineStart:]), "//") {
				continue
			}
			sites++
			end := loc[1] + 200
			if end > len(src) {
				end = len(src)
			}
			window := src[loc[1]:end]
			if !strings.Contains(window, "ActiveTeamScopePredicate(") && !strings.Contains(window, "team_id) IN {ids") && !strings.Contains(window, "team_id IN {ids") {
				t.Errorf("%s: team-keyed daily read at byte %d neither names its teams nor carries ActiveTeamScopePredicate", rel, loc[0])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sites < 6 {
		t.Fatalf("census found %d daily reads, want at least 6", sites)
	}
}
