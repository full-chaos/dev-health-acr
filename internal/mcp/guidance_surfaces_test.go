package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

const (
	wantTeamOwnership     = "A team owns repositories and projects: team ownership = OWNED_BY_TEAM edges from both kinds to the team; for a team ownership question read `read_relationships` (when listed) on the team with types OWNED_BY_TEAM and direction in (or `find_subjects` (when listed) `owned_by`) first. Archived projects stay listed as owned; a team project cohort in an investigation answer flags them archived."
	wantProjectRepository = "A project reaches repositories only through its issues' linked pull requests; no repository is mapped to a team by a project directly."
	wantHomeRunOperation  = "`home` is served by run_operation (when listed) only, by design."
	wantInvestmentScopes  = "Per-team, per-repository, per-project and organization investment is served by `read_facts` (when listed) kind `investment`; the organization subject is kind `organization` with canonical_id `organization:<your organization id>` (`find_subjects` (when listed) kind `organization` returns it): one fact over every repository once (never a sum of team facts), and a repository-bound credential gets a limitation instead. `investmentBreakdown` serves the organization only."
	wantInvestmentWindows = "Investment windows are additive: a work unit counts once, in the window that holds its end stamp (a unit that spans a window boundary counts whole in the window it ends in), so stitched windows add up to the whole period."
)

func TestGuidanceSentencesAtEverySurface(t *testing.T) {
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, toolDataCatalog, toolFindSubjects, toolRunOperation, toolReadFacts, toolReadRelationships)
	instructions := serverInstructions(bootHandlerHalvesConfig(&Bootstrap{Capabilities: caps}))
	data, err := guide.Text(guide.URIData)
	if err != nil {
		t.Fatal(err)
	}
	var manifest toolManifest
	raw, _ := schemaFiles.ReadFile(toolManifestFile)
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	desc := map[string]string{}
	for _, tool := range manifest.Tools {
		desc[tool.Name] = tool.Description
	}

	surfaces := map[string]string{
		"server instructions":            instructions,
		"guide data.md":                  data,
		"data_catalog description":       desc["data_catalog"],
		"find_subjects description":      desc["find_subjects"],
		"read_relationships description": desc["read_relationships"],
	}
	rows := []struct {
		name     string
		sentence string
		sites    []string
	}{
		{"team ownership", wantTeamOwnership, []string{"server instructions", "guide data.md", "find_subjects description", "read_relationships description"}},
		{"project to repository", wantProjectRepository, []string{"server instructions", "guide data.md", "read_relationships description"}},
		{"home run_operation only", wantHomeRunOperation, []string{"server instructions", "guide data.md", "data_catalog description"}},
		{"investment scopes", wantInvestmentScopes, []string{"server instructions", "guide data.md", "data_catalog description"}},
		{"investment windows by end stamp", wantInvestmentWindows, []string{"server instructions", "guide data.md"}},
	}
	for _, row := range rows {
		for _, site := range row.sites {
			if !strings.Contains(surfaces[site], row.sentence) {
				t.Errorf("%s: %s lacks %q", row.name, site, row.sentence)
			}
		}
	}
	for site, text := range surfaces {
		for _, stale := range []string{
			"Investment per team or repository is not served",
			"a team shape is not served here",
			"a sum of stitched windows overcounts it",
		} {
			if strings.Contains(text, stale) {
				t.Errorf("%s still carries stale wording %q", site, stale)
			}
		}
	}
}
