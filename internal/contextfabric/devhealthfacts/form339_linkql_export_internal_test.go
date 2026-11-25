package devhealthfacts

// DO NOT MERGE. Throwaway exec proof of a prod read form (FORM-339-LINKQL-1): the three moved team link readers' statements at acr
// a47904e8 (the #2207 test helpers mainReachedStatement / mainCountsStatement / mainStatesStatement, first page, no time bound) with
// the bindings each one names, so the form test can send the OLD texts through the API client stack under each reader's name.

import (
	"context"
	"fmt"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

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

// Form339CandidatesContextForTest attaches the external table _team_link_candidates the a47904e8 texts read (team_link_candidates.go
// withTeamLinkCandidates at a47904e8: the same teamLinkCandidatesExternalTable at both pins), built from the candidate ids the tip read
// itself reads for the teams (readTeamLinkCandidateIDs, sent unnamed through client), and returns how many ids it holds.
func Form339CandidatesContextForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string) (context.Context, int, error) {
	ids, ok, err := readTeamLinkCandidateIDs(ctx, client, orgID, teamIDs, factTimeBound{})
	if err != nil {
		return nil, 0, err
	}
	if !ok || len(ids) == 0 {
		return nil, 0, fmt.Errorf("no link candidates for the teams")
	}
	table, err := teamLinkCandidatesExternalTable(ids)
	if err != nil {
		return nil, 0, err
	}
	return clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(table)), len(ids), nil
}
