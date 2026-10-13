package interpretprompt

import (
	"strings"
	"testing"
)

func TestSecurityRiskQuestionsAreRoutedToSecurityAlerts(t *testing.T) {
	t.Parallel()
	const want = `"security risk": security_alerts, health.`
	if !strings.Contains(interpretationFactKindGlossary, want) && !strings.Contains(interpretationFactRequirementRules, want) {
		t.Fatalf("the interpretation prompt does not route a %q question to security_alerts and health", "security risk")
	}
}
