package synthesisprompt_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
)

// The prompt tells the model which deficiency fields name the rule's basis
// and which name the current value. Each name it uses must be a field the
// deficiency provider declares, so a rename cannot leave the prompt pointing
// at nothing.
func TestSynthesisPromptNamesTheRuleBasisAndLatestFieldsTheProviderDeclares(t *testing.T) {
	prompt := synthesisprompt.System()
	var capability contextfabric.FactCapability
	for _, provider := range devhealthfacts.NewProviders(nil) {
		if c := provider.Capability(); c.Kind == contextfabric.FactOperationalDeficiencies {
			capability = c
		}
	}
	for _, field := range []string{
		"rule_basis_compounding_risk", "rule_basis_severity", "rule_computed_at",
		"latest_compounding_risk", "latest_severity", "latest_computed_at",
		"score_moved_since_rule", "latest_unavailable_reason",
		"latest_severity_unavailable_reason", "rule_basis_unavailable_reason",
	} {
		if !strings.Contains(prompt, field) {
			t.Errorf("the synthesis prompt does not name %q", field)
		}
		if _, ok := capability.FieldDeclaration(field, contextfabric.SubjectTeam); !ok {
			t.Errorf("the deficiency capability does not declare %q for a team", field)
		}
	}
}
