package devhealthfacts

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// ownershipPairSim serves the (team, repo) ownership statement the way the
// server does: ordered by (team_id, repo_key), after the keyset cursor when
// one is bound, cut at the statement's LIMIT.
type ownershipPairSim struct {
	pairs   [][2]string
	queries []string
}

func (s *ownershipPairSim) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	s.queries = append(s.queries, statement)
	value := func(name string) (string, bool) {
		for _, b := range bindings {
			if b.Name == name {
				v, _ := b.Value.(string)
				return v, true
			}
		}
		return "", false
	}
	afterTeam, hasAfter := value("after_team")
	afterRepo, _ := value("after_repo")
	limit := 1 << 30
	if m := simLimit.FindStringSubmatch(statement); m != nil {
		limit, _ = strconv.Atoi(m[1])
	}
	var rows [][]any
	for _, pair := range s.pairs {
		if hasAfter && (pair[0] < afterTeam || (pair[0] == afterTeam && pair[1] <= afterRepo)) {
			continue
		}
		if len(rows) == limit {
			break
		}
		if !strings.Contains(statement, "any(repo_full_name)") {
			rows = append(rows, []any{pair[0], pair[1]})
		} else {
			rows = append(rows, []any{pair[0], pair[1], "acme/" + pair[1]})
		}
	}
	return &fakeScannerSim{rows: rows}, nil
}

func ownershipPairs(teamRepos map[string]int) [][2]string {
	var pairs [][2]string
	for team, n := range teamRepos {
		for i := 0; i < n; i++ {
			pairs = append(pairs, [2]string{team, fmt.Sprintf("repo-%s-%04d", team, i)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	return pairs
}

func TestInvestmentOwnedRepositoriesAreNotCutAtTheFactRowCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		teams map[string]int
	}{
		{"one_team_201", map[string]int{"team-a": 201}},
		{"two_teams_150_each", map[string]int{"team-a": 150, "team-b": 150}},
		{"one_team_1200", map[string]int{"team-a": 1200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &ownershipPairSim{pairs: ownershipPairs(tc.teams)}
			provider := newInvestmentProvider(client)
			var ids []string
			for team := range tc.teams {
				ids = append(ids, team)
			}
			owned, err := provider.readTeamOwnedRepositories(context.Background(), "org-1", ids, factTimeBound{})
			if err != nil {
				t.Fatal(err)
			}
			for team, want := range tc.teams {
				if got := len(owned[team]); got != want {
					t.Fatalf("%s owned = %d, want %d", team, got, want)
				}
			}
		})
	}
}

func TestInvestmentOwnedRepositoriesAboveTheBoundIsANamedFailure(t *testing.T) {
	client := &ownershipPairSim{pairs: ownershipPairs(map[string]int{"team-a": maxTeamOwnedRepositoryRows + 1})}
	_, err := newInvestmentProvider(client).readTeamOwnedRepositories(context.Background(), "org-1", []string{"team-a"}, factTimeBound{})
	if err == nil || err.Error() != teamOwnedRepositoriesOverflowReason {
		t.Fatalf("want the named bound failure, got %v", err)
	}
}
