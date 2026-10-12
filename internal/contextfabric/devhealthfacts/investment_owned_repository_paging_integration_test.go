package devhealthfacts_test

// The investment mix of a team reads its owned repositories through one
// ownership read. That read used to stop silently at 200 rows shared by every
// requested team: a team past the cut was served over a partial repository set
// or, when it fell wholly after the cut, as owning no repository. The read is
// paged to completion; a set above the 2000 bound is a named failure. Executed
// against a real ClickHouse; the container is mandatory.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const investmentPagingOrg = "3b8a6c1e-52d4-4f07-9e3a-7c1d2b4e8f60"

// seedOwnedRepositoriesWithWork gives teamID n owned repositories, each with
// one work unit of effort 1 in feature_delivery. Ids are offset so two teams
// never share a repository.
func seedOwnedRepositoriesWithWork(t *testing.T, ctx context.Context, exec func(string) error, teamID string, offset, n int) {
	t.Helper()
	id := `toUUID(concat('00000000-0000-4000-8000-', leftPad(toString(number + ` + strconv.Itoa(offset) + `), 12, '0')))`
	name := `concat('acme/` + teamID + `-', toString(number))`
	count := strconv.Itoa(n)
	for _, statement := range []string{
		`INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT ` + id + `, '` + investmentPagingOrg + `', ` + name + `, 'github', now64(3) FROM numbers(` + count + `)`,
		`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) SELECT '` + investmentPagingOrg + `', 'github', '` + teamID + `', ` + id + `, ` + name + `, 'exact', 'native', 1, 100, 0, toDateTime64('2026-01-01 00:00:00', 3, 'UTC'), NULL, now64(3) FROM numbers(` + count + `)`,
		`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) SELECT concat('wu-` + teamID + `-', toString(number)), toDateTime64('2026-02-01 00:00:00', 3, 'UTC'), toDateTime64('2026-02-01 00:00:00', 3, 'UTC'), ` + id + `, 1.0, map('feature_delivery', 1.0), map(), '{}', toDateTime64('2026-02-01 00:00:00', 3, 'UTC'), '` + investmentPagingOrg + `' FROM numbers(` + count + `)`,
	} {
		if err := exec(statement); err != nil {
			t.Fatalf("seed %s: %v\n%s", teamID, err, statement)
		}
	}
}

func themeBreakdownEffort(t *testing.T, fact contextfabric.CanonicalFact) float64 {
	t.Helper()
	table := fact.Fields["theme_breakdown"].Table
	if table == nil {
		t.Fatalf("no theme_breakdown: %#v", fact.Fields)
	}
	sum := 0.0
	for _, row := range table.Rows {
		sum += *row.Fields["weighted_effort"].Number
	}
	return sum
}

func TestInvestmentMixOwnedRepositoriesAreServedInFullOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	exec := func(statement string) error { return direct.Exec(ctx, statement) }
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	read := func(teams ...string) (contextfabric.FactProviderResult, error) {
		subjects := make([]contextfabric.SubjectRef, 0, len(teams))
		for _, team := range teams {
			subjects = append(subjects, teamSubject(team))
		}
		return provider.ReadFacts(ctx, storage.Principal{OrgID: investmentPagingOrg}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects,
		})
	}

	seedOwnedRepositoriesWithWork(t, ctx, exec, "team-a", 0, 150)
	seedOwnedRepositoriesWithWork(t, ctx, exec, "team-b", 1000, 150)
	seedOwnedRepositoriesWithWork(t, ctx, exec, "team-c", 2000, 201)

	t.Run("two teams of 150 are both served over every owned repository", func(t *testing.T) {
		result, err := read("team-a", "team-b")
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		for _, team := range []string{"team-a", "team-b"} {
			fact := factFor(t, result, teamSubject(team))
			wantInt(t, fact.Fields, "owned_repository_count", 150)
			if got := themeBreakdownEffort(t, fact); got != 150 {
				t.Fatalf("%s mix effort = %v, want 150", team, got)
			}
		}
	})

	t.Run("one team of 201 is served over all 201", func(t *testing.T) {
		result, err := read("team-c")
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		fact := factFor(t, result, teamSubject("team-c"))
		wantInt(t, fact.Fields, "owned_repository_count", 201)
		if got := themeBreakdownEffort(t, fact); got != 201 {
			t.Fatalf("mix effort = %v, want 201", got)
		}
	})

	t.Run("above the 2000 bound is a named failure, never a partial mix", func(t *testing.T) {
		seedOwnedRepositoriesWithWork(t, ctx, exec, "team-d", 10000, 2001)
		result, err := read("team-d")
		if err == nil {
			t.Fatalf("2001 owned repositories must be rejected, got facts=%d", len(result.Facts))
		}
		if !strings.Contains(fmt.Sprint(err), "query team owned repositories exceeded its bound") && !strings.Contains(readFailureCause(err), "query team owned repositories exceeded its bound") {
			t.Fatalf("error = %v, want the named ownership bound failure", err)
		}
	})
}

func TestInvestmentMixOwnedRepositoriesAtTheReadFactsBoundary(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	exec := func(statement string) error { return direct.Exec(ctx, statement) }
	seedOwnedRepositoriesWithWork(t, ctx, exec, "team-a", 0, 150)
	seedOwnedRepositoriesWithWork(t, ctx, exec, "team-b", 1000, 150)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	seam := &seamCapture{registry: registry.WithoutScopeExpansion()}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(seam), nil)
	start, end := ts(2026, 1, 1, 0, 0, 0), ts(2026, 3, 1, 0, 0, 0)
	a, b := teamSubject("team-a"), teamSubject("team-b")
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000009359"),
		storage.Principal{OrgID: investmentPagingOrg, Subject: "t9359", CredentialID: "t9359"}, directread.FactsRequest{
			Kinds: []string{"investment"},
			Subjects: []directread.RequestSubject{
				{Kind: "team", CanonicalID: a.CanonicalID},
				{Kind: "team", CanonicalID: b.CanonicalID},
			},
			Window: &directread.RequestWindow{Mode: directread.WindowRange, Start: &start, End: &end},
		})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	raw, _ := json.Marshal(response)
	for _, want := range []string{a.CanonicalID, b.CanonicalID} {
		var served *directread.ServedFact
		for i := range response.Facts {
			if response.Facts[i].Subject.CanonicalID == want {
				served = &response.Facts[i]
			}
		}
		if served == nil {
			t.Fatalf("%s serves no investment fact (an owns-nothing answer): %s", want, raw)
		}
		if got := fmt.Sprint(served.Fields["owned_repository_count"]); got != "150" {
			t.Fatalf("%s owned_repository_count = %s, want 150", want, got)
		}
	}
}
