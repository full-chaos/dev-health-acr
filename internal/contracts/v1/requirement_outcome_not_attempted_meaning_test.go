package v1

import (
	"os"
	"strings"
	"testing"
)

// `not_attempted` is published for a requirement whose source WAS read but
// whose account could not be stated (requirement_not_evaluable), so its
// definition must not claim no read was made.
func TestNotAttemptedDefinitionDoesNotClaimNoReadWasMade(t *testing.T) {
	source, err := os.ReadFile("context_fabric_requirement_outcome.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, stale := range []string{"and no read was made for it", "was stopped BEFORE any read"} {
		if strings.Contains(text, stale) {
			t.Errorf("the not_attempted definition still says %q", stale)
		}
	}
	if !strings.Contains(text, "and was not evaluated; the cause says why") {
		t.Errorf("the not_attempted definition does not say it was not evaluated")
	}
}
