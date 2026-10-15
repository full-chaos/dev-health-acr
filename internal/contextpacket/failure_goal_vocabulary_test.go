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

// Every code-health entity type is emitted by the hotspot or complexity
// catalog source, and each of those sources emits one of them.
func TestCodeHealthEntityTypesAreTheHotspotAndComplexitySources(t *testing.T) {
	for entityType := range codeHealthEntityTypes {
		found := false
		for _, query := range SourceQueryCatalogV1 {
			if (query.ID == "file_hotspots.v1" || query.ID == "file_complexity.v1") && strings.Contains(query.Statement, "'"+entityType+"' entity_type") {
				found = true
			}
		}
		if !found {
			t.Errorf("code-health entity type %q is not emitted by file_hotspots.v1 or file_complexity.v1", entityType)
		}
	}
	for _, id := range []string{"file_hotspots.v1", "file_complexity.v1"} {
		emitted := false
		for _, query := range SourceQueryCatalogV1 {
			for entityType := range codeHealthEntityTypes {
				emitted = emitted || (query.ID == id && strings.Contains(query.Statement, "'"+entityType+"' entity_type"))
			}
		}
		if !emitted {
			t.Errorf("%s emits no code-health entity type", id)
		}
	}
}

func TestGoalNamesCodeHealthOnlyForACodeHealthWord(t *testing.T) {
	for goal, want := range map[string]bool{
		"reduce churn in the checkout service": true,
		"which files are hotspots":             true,
		"refactor the payment module":          true,
		"Reduce failed deployments":            false,
		"release notes":                        false,
	} {
		if got := goalNamesCodeHealth(goal); got != want {
			t.Errorf("goalNamesCodeHealth(%q) = %v, want %v", goal, got, want)
		}
	}
}
