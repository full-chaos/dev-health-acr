package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

const (
	wantTeamOwnership     = "A team owns repositories and projects: team ownership = OWNED_BY_TEAM edges from both kinds to the team; for a team ownership question read `read_relationships` on the team with types OWNED_BY_TEAM and direction in (or `find_subjects` `owned_by`) first."
	wantProjectRepository = "A project reaches repositories only through its issues' linked pull requests; no repository is mapped to a team by a project directly."
	wantHomeRunOperation  = "`home` is served by run_operation only, by design."
	wantInvestmentScopes  = "Per-team, per-repository and per-project investment is served by `read_facts` kind `investment`; `investmentBreakdown` serves the organization only."
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
		} {
			if strings.Contains(text, stale) {
				t.Errorf("%s still carries stale wording %q", site, stale)
			}
		}
	}
}
