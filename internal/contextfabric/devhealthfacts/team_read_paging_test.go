package devhealthfacts

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// clientRowCapSim plays the production ClickHouse client's result cap: a
// statement that returns more rows than max_result_rows (1000, or the
// statement's own SETTINGS value) fails with the server's code 396, and an
// ownership-shaped statement honours its keyset cursor and LIMIT the way the
// server does, so a reader that pages gets exactly the rows the server would
// hand it.
type clientRowCapSim struct {
	teamID   string
	owned    []string // repository keys, sorted ascending
	queries  []string
	maxIDs   int // largest ids binding seen on a per-repository statement
	perRepo  func(key string) []any
	observed []int
}

var (
	simLimit    = regexp.MustCompile(`(?m)^LIMIT (\d+)\s*$`)
	simSettings = regexp.MustCompile(`max_result_rows = (\d+)`)
)

func (c *clientRowCapSim) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.queries = append(c.queries, statement)
	binding := func(name string) (any, bool) {
		for _, b := range bindings {
			if b.Name == name {
				return b.Value, true
			}
		}
		return nil, false
	}
	capRows := 1000
	if m := simSettings.FindStringSubmatch(statement); m != nil {
		capRows, _ = strconv.Atoi(m[1])
	}
	var rows [][]any
	switch {
	case strings.Contains(statement, "GROUP BY team_id, repo_key"):
		after, hasAfter := binding("after_repo")
		limit := 1 << 30
		if m := simLimit.FindStringSubmatch(statement); m != nil {
			limit, _ = strconv.Atoi(m[1])
		}
		for _, key := range c.owned {
			if hasAfter && key <= after.(string) {
				continue
			}
			if len(rows) == limit {
				break
			}
			rows = append(rows, []any{c.teamID, key, "acme/" + key})
		}
	case strings.Contains(statement, "FROM deploy_metrics_daily") && c.perRepo != nil:
		ids, _ := binding("ids")
		keys := ids.([]string)
		c.maxIDs = max(c.maxIDs, len(keys))
		for _, key := range keys {
			rows = append(rows, c.perRepo(key))
		}
	}
	c.observed = append(c.observed, len(rows))
	if len(rows) > capRows {
		return nil, fmt.Errorf("code: 396, message: Limit for result exceeded, max rows: %d, current rows: %d", capRows, len(rows))
	}
	return &fakeScannerSim{rows: rows}, nil
}

// fakeScannerSim advances in Next, as the driver does, so a reader that
// skips Scan on a row still makes progress.
type fakeScannerSim struct {
	rows [][]any
	i    int
}

func (s *fakeScannerSim) Next() bool {
	if s.i >= len(s.rows) {
		return false
	}
	s.i++
	return true
}

func (s *fakeScannerSim) Scan(dest ...any) error {
	row := s.rows[s.i-1]
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[index].(string)
		case *int64:
			*value = row[index].(int64)
		case *uint8:
			*value = row[index].(uint8)
		case *float64:
			*value = row[index].(float64)
		default:
			return fmt.Errorf("unsupported scan target %T", target)
		}
	}
	return nil
}
func (s *fakeScannerSim) Close() error { return nil }
func (s *fakeScannerSim) Err() error   { return nil }

func simOwned(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
	}
	sort.Strings(keys)
	return keys
}

func TestTeamOwnedRepositoriesPagesUnderTheClientRowCap(t *testing.T) {
	bound := factTimeBound{active: true, end: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	for _, tc := range []struct {
		name        string
		rows        int
		wantQueries int
	}{
		{"one_page_below_probe", 499, 1},
		{"exactly_one_page", 500, 1},
		{"one_past_a_page", 501, 2},
		{"two_full_pages", 1000, 2},
		{"one_past_the_client_cap", 1001, 3},
		{"between_cap_and_bound", 1500, 3},
		{"exactly_at_bound", maxTeamOwnedRepositoryRows, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &clientRowCapSim{teamID: "team-a", owned: simOwned(tc.rows)}
			owned, err := teamOwnedRepositories(context.Background(), client, "org-1", []string{"team-a"}, bound)
			if err != nil {
				t.Fatalf("a team owning %d repositories must be served: %v", tc.rows, err)
			}
			got := owned["team-a"]
			if len(got) != tc.rows {
				t.Fatalf("owned = %d, want %d", len(got), tc.rows)
			}
			for i, repo := range got {
				if repo.key != client.owned[i] {
					t.Fatalf("row %d = %s, want %s (order or duplicate across pages)", i, repo.key, client.owned[i])
				}
			}
			if len(client.queries) != tc.wantQueries {
				t.Fatalf("queries = %d, want %d (rows per query %v)", len(client.queries), tc.wantQueries, client.observed)
			}
		})
	}
}

func TestTeamOwnedRepositoriesAboveTheBoundIsANamedFailure(t *testing.T) {
	for _, rows := range []int{maxTeamOwnedRepositoryRows + 1, maxTeamOwnedRepositoryRows + 600} {
		client := &clientRowCapSim{teamID: "team-a", owned: simOwned(rows)}
		_, err := teamOwnedRepositories(context.Background(), client, "org-1", []string{"team-a"}, factTimeBound{})
		if err == nil || err.Error() != teamOwnedRepositoriesOverflowReason {
			t.Fatalf("%d rows: want the named bound failure, got %v", rows, err)
		}
	}
}

func TestTeamOwnershipPagesCarryTheCursorAfterTheFirstPage(t *testing.T) {
	client := &clientRowCapSim{teamID: "team-a", owned: simOwned(700)}
	if _, err := teamOwnedRepositories(context.Background(), client, "org-1", []string{"team-a"}, factTimeBound{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(client.queries[0], "HAVING") {
		t.Fatalf("the first page carries a cursor:\n%s", client.queries[0])
	}
	if !strings.Contains(client.queries[1], "HAVING (team_id, repo_key) > ({after_team:String}, {after_repo:String})") {
		t.Fatalf("the second page lacks the keyset predicate:\n%s", client.queries[1])
	}
	for _, statement := range client.queries {
		m := simLimit.FindStringSubmatch(statement)
		if m == nil || m[1] != strconv.Itoa(teamReadPageRows+1) {
			t.Fatalf("page LIMIT must be %d (under the client cap):\n%s", teamReadPageRows+1, statement)
		}
	}
}

func TestTeamDeploymentsRollupServesMoreRepositoriesThanTheClientRowCap(t *testing.T) {
	const repositories = 1500
	client := &clientRowCapSim{teamID: "team-a", owned: simOwned(repositories)}
	client.perRepo = func(key string) []any {
		return []any{key, int64(2), int64(1), int64(1), "2026-09-20", uint8(0), float64(0), uint8(0), float64(0)}
	}
	var provider contextfabric.FactProvider
	for _, candidate := range NewProviders(client) {
		if candidate.Capability().Kind == contextfabric.FactDeployments {
			provider = candidate
		}
	}
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactDeployments,
		Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectTeam, CanonicalID: "team:team-a", Label: "team-a"}},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	fields := result.Facts[0].Fields
	if v := fields["owned_repository_count"]; v.Integer == nil || *v.Integer != repositories {
		t.Fatalf("owned_repository_count = %#v, want %d", v, repositories)
	}
	if v := fields["deployments_count_window"]; v.Integer == nil || *v.Integer != 2*repositories {
		t.Fatalf("deployments_count_window = %#v, want %d (every repository's row merged across chunks)", v, 2*repositories)
	}
	if client.maxIDs > teamRepoKeyChunk {
		t.Fatalf("a per-repository statement carried %d ids, want at most %d", client.maxIDs, teamRepoKeyChunk)
	}
}

func TestRepoKeyChunksCoverEveryKeyOnceInOrder(t *testing.T) {
	for _, n := range []int{0, 1, teamRepoKeyChunk - 1, teamRepoKeyChunk, teamRepoKeyChunk + 1, 3*teamRepoKeyChunk + 7} {
		keys := simOwned(n)
		var seen []string
		chunks := 0
		err := forEachRepoKeyChunk(keys, func(chunk []string) error {
			chunks++
			if len(chunk) == 0 || len(chunk) > teamRepoKeyChunk {
				t.Fatalf("n=%d: chunk of %d", n, len(chunk))
			}
			seen = append(seen, chunk...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(seen, ",") != strings.Join(keys, ",") {
			t.Fatalf("n=%d: chunks do not reproduce the key list", n)
		}
		if want := (n + teamRepoKeyChunk - 1) / teamRepoKeyChunk; chunks != want {
			t.Fatalf("n=%d: chunks = %d, want %d", n, chunks, want)
		}
	}
}
