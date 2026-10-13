package v1

import "testing"

func TestRankingRowSubjectIDPrefersNewKeyAndFallsBackToOld(t *testing.T) {
	t.Parallel()
	newID, oldID := "new", "old"
	str := func(v *string) ContextFabricScalarValue { return ContextFabricScalarValue{String: v} }
	for name, tc := range map[string]struct {
		fields map[string]ContextFabricScalarValue
		want   string
		ok     bool
	}{
		"both":     {map[string]ContextFabricScalarValue{"subject_canonical_id": str(&newID), "team_canonical_id": str(&oldID)}, "new", true},
		"old only": {map[string]ContextFabricScalarValue{"team_canonical_id": str(&oldID)}, "old", true},
		"none":     {map[string]ContextFabricScalarValue{}, "", false},
	} {
		got, ok := RankingRowSubjectID(ContextFabricClaimedFactRow{Fields: tc.fields})
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q,%v", name, got, ok)
		}
	}
}
