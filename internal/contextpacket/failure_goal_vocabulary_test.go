package contextpacket

import (
	"strings"
	"testing"
)

// Every stored failure status has a goal word that names it, so the two
// vocabularies cannot drift apart.
func TestEveryStoredFailureStatusHasAGoalWord(t *testing.T) {
	for status := range failureTokens {
		named := false
		for _, part := range strings.Split(status, "_") {
			named = named || failureGoalWords[part]
		}
		if !named {
			t.Errorf("failure status %q has no word in failureGoalWords", status)
		}
	}
}

func TestGoalNamesFailureOnlyForAFailureWord(t *testing.T) {
	for goal, want := range map[string]bool{
		"Reduce failed deployments and CI failures": true,
		"no failures this week":                     true,
		"why was the rollback needed":               true,
		"Add saved payment methods at checkout":     false,
		"release notes":                             false,
	} {
		if got := goalNamesFailure(goal); got != want {
			t.Errorf("goalNamesFailure(%q) = %v, want %v", goal, got, want)
		}
	}
}
