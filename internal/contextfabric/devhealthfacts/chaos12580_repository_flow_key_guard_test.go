package devhealthfacts_test

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// chaos12580_repository_flow_key_guard_test.go is the guard the lead ruled on
// Codex r3 of #1962: the repository_flow row bound class (320 rows) keys on a
// breakdown keyed (repository, theme), because the producer-side table carries
// no field name. So no other producer table may declare both a repository and
// a theme column without joining that class on purpose: a second table with
// that key would get the 320 cap silently.

// repositoryFlowTables is every declared table field allowed to carry both
// columns. Adding a field here is the on-purpose step: it joins the class.
var repositoryFlowTables = map[string]bool{
	contractsv1.ContextFabricRenderRepositoryFlowTableField: true,
}

func TestCHAOS12580OnlyTheRepositoryFlowTableDeclaresARepositoryAndAThemeColumn(t *testing.T) {
	t.Parallel()
	found := 0
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		raw, err := json.Marshal(provider.Capability())
		if err != nil {
			t.Fatal(err)
		}
		var capability struct {
			Kind   string `json:"kind"`
			Fields []struct {
				Name    string `json:"name"`
				Columns []struct {
					Name string `json:"name"`
				} `json:"columns"`
			} `json:"fields"`
		}
		if err := json.Unmarshal(raw, &capability); err != nil {
			t.Fatal(err)
		}
		for _, field := range capability.Fields {
			has := map[string]bool{}
			for _, column := range field.Columns {
				has[column.Name] = true
			}
			if !has[contractsv1.ContextFabricRenderRepositoryField] || !has[contractsv1.ContextFabricRenderThemeField] {
				continue
			}
			if !repositoryFlowTables[field.Name] {
				t.Errorf("%s.%s declares a %q and a %q column: a (repository, theme) key takes the repository_flow row bound (320), so join it on purpose in repositoryFlowTables or rename the columns", capability.Kind, field.Name, contractsv1.ContextFabricRenderRepositoryField, contractsv1.ContextFabricRenderThemeField)
				continue
			}
			found++
		}
	}
	if found == 0 {
		t.Fatal("no producer declares repository_theme_breakdown: the guard measured nothing")
	}
}

// The sankey's node words come from the table itself: repository is worded by
// repository_label (its handle), and theme by the theme vocabulary
// (CHAOS-12639). Without them an answer shows a canonical id and a raw token.
func TestCHAOS12580TheRepositoryThemeTableDeclaresItsWords(t *testing.T) {
	t.Parallel()
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		raw, err := json.Marshal(provider.Capability())
		if err != nil {
			t.Fatal(err)
		}
		var capability struct {
			Fields []struct {
				Name    string `json:"name"`
				Columns []struct {
					Name       string `json:"name"`
					Vocabulary string `json:"vocabulary"`
					WordColumn string `json:"word_column"`
				} `json:"columns"`
			} `json:"fields"`
		}
		if err := json.Unmarshal(raw, &capability); err != nil {
			t.Fatal(err)
		}
		for _, field := range capability.Fields {
			if field.Name != contractsv1.ContextFabricRenderRepositoryFlowTableField {
				continue
			}
			words := map[string][2]string{}
			for _, column := range field.Columns {
				words[column.Name] = [2]string{column.Vocabulary, column.WordColumn}
			}
			if got, want := words[contractsv1.ContextFabricRenderRepositoryField], [2]string{"", "repository_label"}; got != want {
				t.Errorf("repository column words %v, want %v", got, want)
			}
			if got, want := words[contractsv1.ContextFabricRenderThemeField], [2]string{string(contractsv1.ContextFabricVocabularyInvestmentTheme), ""}; got != want {
				t.Errorf("theme column words %v, want %v", got, want)
			}
			return
		}
	}
	t.Fatal("no producer declares repository_theme_breakdown: the test measured nothing")
}
