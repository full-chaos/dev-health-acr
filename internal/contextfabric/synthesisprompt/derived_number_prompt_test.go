package synthesisprompt_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
)

// CHAOS-11600: the server prompt names the derived numbers and the words that
// state one, so the model does not read a score handed over as a fact as an
// observation.
func TestSynthesisPromptAsksDerivedNumbersToBeQualified(t *testing.T) {
	t.Parallel()
	prompt := synthesisprompt.System()
	for _, term := range []string{
		"A derived number is one the input does not give as an observation",
		"compounding risk score", "investment theme share", "derived_from",
		"states a derived number MUST contain appears, leans or suggests",
		"is, was, has, shows, reports, observes, records, represents, accounts for, includes, stands at, detected, determined, or a bare phrase",
		"is not derived: state it plainly",
	} {
		if !strings.Contains(prompt, term) {
			t.Errorf("the synthesis prompt does not carry %q", term)
		}
	}
}

// Every numeric field a provider declares as a judged score, every numeric
// field that carries a compounding risk score under another name, and every
// numeric investment theme share is one the phrasing measure counts as derived:
// a field added to the providers cannot leave the measure blind to it.
func TestEveryDeclaredScoreAndThemeShareIsADerivedNumber(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, provider := range devhealthfacts.NewProviders(nil) {
		capability := provider.Capability()
		for _, declaration := range capability.Fields {
			numeric := declaration.Type == contextfabric.FactFieldNumber || declaration.Type == contextfabric.FactFieldInteger
			theme := capability.Kind == contextfabric.FactInvestment &&
				(strings.HasPrefix(declaration.Name, "theme_") || strings.HasPrefix(declaration.Name, "prior_theme_"))
			risk := strings.Contains(declaration.Name, "compounding_risk")
			if !numeric || !(declaration.Score || theme || risk) {
				continue
			}
			checked++
			if _, derived := contextfabric.DerivedNumberField(declaration.Name); !derived {
				t.Errorf("%s.%s is a declared score, risk score or theme share, and the phrasing measure does not count it as derived", capability.Kind, declaration.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no declared score or theme share found: the guard checked nothing")
	}
}
