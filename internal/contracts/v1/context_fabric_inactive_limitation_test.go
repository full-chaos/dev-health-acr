package v1

import (
	"strings"
	"testing"
)

// The inactive-team limitation may only promise the active team's id under
// the condition that holds: the twin lookup names none when there are several
// matches or the sole match is unreadable.
func TestInactiveTeamHintLimitationPromisesTheIDOnlyForASingleReadableTwin(t *testing.T) {
	text := ContextFabricInactiveTeamHintLimitation
	if !strings.Contains(text, "single readable active team") {
		t.Fatalf("limitation = %q: the id promise must be conditioned on a single readable active team", text)
	}
	if !strings.Contains(text, "otherwise no id is given") {
		t.Fatalf("limitation = %q: must say that no id is given in the other branches", text)
	}
	if strings.Contains(text, "Where an active team of the same name exists") {
		t.Fatalf("limitation = %q: unconditional id promise returned", text)
	}
}
