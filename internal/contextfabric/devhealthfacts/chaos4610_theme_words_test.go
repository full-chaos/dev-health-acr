package devhealthfacts

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The contract cannot import this package, so it names the theme tokens itself.
// This keeps the two lists one list: a table row's token always has a served word.
func TestEveryCanonicalThemeHasAServedWord(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, theme := range canonicalInvestmentThemes {
		word, known := contractsv1.ContextFabricInvestmentThemeLabel(theme)
		if !known || word == "" {
			t.Fatalf("theme %q has no served word", theme)
		}
		if other, repeated := seen[word]; repeated {
			t.Fatalf("themes %q and %q share the word %q", other, theme, word)
		}
		seen[word] = theme
	}
}
