package devhealthfacts

import (
	"math"
	"testing"
)

// CHAOS-6559 invariant: a team's mix equals the SUM of its owned
// repositories' mixes; a repository named twice (two ownership sources) is
// counted once; a repository the team does not own contributes nothing; a
// team owning nothing with data is absent, never a zero mix.
func TestSumOwnedRepoMixIsTheSumOfOwnedRepoMixes(t *testing.T) {
	t.Parallel()
	perRepo := map[string]*repoThemeTotals{
		"r1": {theme: map[string]float64{"feature_delivery": 6, "risk": 2}, bugfix: 1},
		"r2": {theme: map[string]float64{"feature_delivery": 1, "maintenance": 3}, bugfix: 0.5},
		"r3": {theme: map[string]float64{"quality": 100}},
	}
	owned := map[string][]string{
		"team-a": {"r1", "r2", "r2"}, // r2 twice: two sources
		"team-b": {"r3"},
		"team-c": {"missing"},
	}
	got := sumOwnedRepoMix(owned, perRepo)
	near := func(a, b float64, what string) {
		t.Helper()
		if math.Abs(a-b) > 1e-12 {
			t.Fatalf("%s = %v, want %v", what, a, b)
		}
	}
	a := got["team-a"]
	if a == nil {
		t.Fatalf("team-a missing")
	}
	for theme, want := range map[string]float64{
		"feature_delivery": perRepo["r1"].theme["feature_delivery"] + perRepo["r2"].theme["feature_delivery"],
		"risk":             perRepo["r1"].theme["risk"],
		"maintenance":      perRepo["r2"].theme["maintenance"],
		"quality":          0,
	} {
		near(a.theme[theme], want, "team-a "+theme)
	}
	near(a.bugfix, 1.5, "team-a bugfix")
	if a.repos != 2 {
		t.Fatalf("team-a repos = %d, want 2 (r2 counted once)", a.repos)
	}
	near(a.total(), perRepo["r1"].total()+perRepo["r2"].total(), "team-a total")
	near(got["team-b"].theme["quality"], 100, "team-b (r1/r2 not leaked in)")
	if _, ok := got["team-c"]; ok {
		t.Fatalf("team owning no repo with data must be absent, got %#v", got["team-c"])
	}
}
