package devhealthschema

import "testing"

func TestTeamAttributionPredicate(t *testing.T) {
	for _, tc := range []struct {
		alias string
		scope AttributionScope
		want  string
	}{
		{"a", AttributionScopeOrg, "a.is_primary = 1"},
		{"a", AttributionScopeTeam, "a.is_primary IN (1, 2)"},
		{"", AttributionScopeOrg, "is_primary = 1"},
		{"", AttributionScopeTeam, "is_primary IN (1, 2)"},
	} {
		if got := TeamAttributionPredicate(tc.alias, tc.scope); got != tc.want {
			t.Errorf("TeamAttributionPredicate(%q, %d) = %q, want %q", tc.alias, tc.scope, got, tc.want)
		}
	}
}
