package devhealthfacts

import (
	"reflect"
	"testing"
)

func TestDropCoveredLinksAppliesBothSidesOfTheFallbackOnlyRule(t *testing.T) {
	cover := nativeCover{
		issues:       map[string]struct{}{"i-native": {}},
		pullRequests: map[string]struct{}{coverPullRequestKey("r2", "7"): {}},
	}
	links := []linkedSetRow{
		{repo: "r1", number: "1", team: "t", rank: nativeRank},
		{repo: "r1", number: "2", team: "t", issue: "i-native", rank: 2},
		{repo: "r2", number: "7", team: "t", issue: "i-free", rank: 1},
		{repo: "r3", number: "3", team: "t", issue: "i-free", rank: 1},
		{repo: "r3", number: "3", team: "t", issue: "i-other", rank: 2},
		{repo: "r1", number: "1", team: "t", issue: "i-free", rank: 2},
		{repo: "r1", number: "1", team: "u", rank: nativeRank},
		{repo: "r4", number: "4", team: "t", rank: nativeRank, issue: ""},
		{repo: "r2", number: "7", team: "t", rank: nativeRank},
	}
	kept, dropped := dropCoveredLinks(links, cover)
	// i-native's fallback and the fallback on the natively linked pull request
	// r2#7 are dropped; the native row on r2#7 stays; a pull request kept by
	// two fallback rows is one entry; a repeated key is one entry.
	if dropped != 2 {
		t.Fatalf("dropped %d rows, want 2", dropped)
	}
	wantRepos := []string{"r1", "r3", "r1", "r4", "r2"}
	wantNumbers := []string{"1", "3", "1", "4", "7"}
	wantTeams := []string{"t", "t", "u", "t", "t"}
	if !reflect.DeepEqual(kept.repoIDs, wantRepos) || !reflect.DeepEqual(kept.prNumbers, wantNumbers) || !reflect.DeepEqual(kept.teamIDs, wantTeams) {
		t.Fatalf("kept %v %v %v, want %v %v %v", kept.repoIDs, kept.prNumbers, kept.teamIDs, wantRepos, wantNumbers, wantTeams)
	}
}

func TestNativeFirstOutcomeIsClosed(t *testing.T) {
	native := []linkedSetRow{{rank: nativeRank}}
	fallback := []linkedSetRow{{rank: 2, issue: "i"}}
	for _, c := range []struct {
		links   []linkedSetRow
		dropped int
		want    string
	}{
		{native, 0, "no_fallback_rows"},
		{fallback, 0, "no_fallback_row_covered"},
		{fallback, 1, "covered_fallback_rows_dropped"},
	} {
		if got := nativeFirstOutcome(c.links, c.dropped); got != c.want {
			t.Errorf("nativeFirstOutcome(%v, %d) = %q, want %q", c.links, c.dropped, got, c.want)
		}
	}
}
