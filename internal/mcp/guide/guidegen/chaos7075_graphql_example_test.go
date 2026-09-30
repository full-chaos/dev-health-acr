package guidegen

import (
	"strings"
	"testing"
)

// The graphql_query section of the guide carries the rules and points to
// data_catalog; it names no root field (CHAOS-7075 class sweep).
func TestGraphQLGuideSectionHasRulesAndNoRootList(t *testing.T) {
	text, err := buildData(FromRegistries())
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range GraphQLRules {
		if !strings.Contains(text, rule) {
			t.Errorf("the guide lacks the rule %q", rule)
		}
	}
	if strings.Contains(text, "| Root field |") || strings.Contains(text, "| Operation |") {
		t.Fatal("the guide carries an operation or root table")
	}
}
