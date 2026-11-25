package devhealthfacts

// DO NOT MERGE. Throwaway exec proof of a prod read form (FORM-339-LINKQL-1): the three moved team link readers' statements at acr
// a47904e8 (the #2207 test helpers mainReachedStatement / mainCountsStatement / mainStatesStatement, first page, no time bound) with
// the bindings each one names, so the form test can send the OLD texts through the API client stack under each reader's name.

import (
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// Form339TextForTest is one statement text, the reader name it is sent under, and its bindings (org_id excluded).
type Form339TextForTest struct {
	Reader, Text string
	Bindings     []contextpacket.ClickHouseBinding
}

// Form339MainTextsForTest returns the a47904e8 texts of ReadTeamProjectReachedRepositories, ReadTeamProjectLinkedPullRequests and
// ReadTeamProjectLinkedPullRequestStates for teamIDs, the reached repository ids and an evidence window [start, end).
func Form339MainTextsForTest(teamIDs, reachRepoIDs []string, start, end time.Time) []Form339TextForTest {
	bound := factTimeBound{}
	window := rollupWindow{hasStart: true, hasEnd: true, endExclusive: true, start: start.UTC(), end: end.UTC(), basis: "evidence_window"}
	limit := teamReadPageRows + 1
	ids := contextpacket.ClickHouseBinding{Name: "ids", Value: teamIDs}
	reach := contextpacket.ClickHouseBinding{Name: "reach_repo_ids", Value: reachRepoIDs}
	var windowBindings []contextpacket.ClickHouseBinding
	for _, b := range window.bindings() {
		windowBindings = append(windowBindings, contextpacket.ClickHouseBinding{Name: b.Name, Value: b.Value})
	}
	return []Form339TextForTest{
		{"ReadTeamProjectReachedRepositories", mainReachedStatement(bound, "", limit), []contextpacket.ClickHouseBinding{ids}},
		{"ReadTeamProjectLinkedPullRequests", mainCountsStatement(window, bound, "", limit), append([]contextpacket.ClickHouseBinding{ids, reach}, windowBindings...)},
		{"ReadTeamProjectLinkedPullRequestStates", mainStatesStatement(bound, "", limit), []contextpacket.ClickHouseBinding{ids, reach}},
	}
}
