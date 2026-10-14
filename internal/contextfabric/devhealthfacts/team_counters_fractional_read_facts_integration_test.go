package devhealthfacts_test

import (
	"context"
	"math"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A team that co-owns a repository holds a share of its counters, so the team
// and project counters of the metrics fact are numbers. The read_facts reader
// serves them over a real ClickHouse whose counter columns are still UInt32
// (before ops migration 124) and already Float64 (after it): ACR and ops roll
// independently, so one build must serve the right value on both.
func TestReadFactsServesFractionalTeamCountersOnBothColumnTypes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		counterType string
		shares      [2][3]float64
		want        [3]float64
	}{
		{"whole counts on UInt32", "UInt32", [2][3]float64{{7, 3, 1}, {3, 1, 1}}, [3]float64{10, 4, 2}},
		{"fractional shares on Float64", "Float64", [2][3]float64{{0.3, 0.1, 0.2}, {0.3, 0.2, 0.1}}, [3]float64{0.6, 0.3, 0.3}},
		{"whole counts on Float64", "Float64", [2][3]float64{{7, 3, 1}, {3, 1, 1}}, [3]float64{10, 4, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, direct := newCHAOS3780IntegrationClient(t, ctx)
			createCHAOS4347Tables(t, ctx, direct)
			if tc.counterType != "UInt32" {
				if err := direct.Exec(ctx, "ALTER TABLE team_metrics_daily MODIFY COLUMN commits_count "+tc.counterType+", MODIFY COLUMN after_hours_commits_count "+tc.counterType+", MODIFY COLUMN weekend_commits_count "+tc.counterType); err != nil {
					t.Fatalf("set counter columns to %s: %v", tc.counterType, err)
				}
			}
			const orgID = "org-fractional-team-counters"
			day := date(2026, 8, 12)
			stored := func(v float64) any {
				if tc.counterType == "UInt32" {
					return uint32(v)
				}
				return v
			}
			for i, share := range tc.shares {
				if err := direct.Exec(ctx, `INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id, repo_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
					day, "team-share", "Team share", stored(share[0]), stored(share[1]), stored(share[2]), 0.77, 0.66, ts(2026, 8, 12, 6, 0, 0), orgID, []string{"repo-1", "repo-2"}[i]); err != nil {
					t.Fatalf("seed team metrics: %v", err)
				}
			}
			registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
			if err != nil {
				t.Fatalf("fact registry: %v", err)
			}
			gate := directread.NewSubjectGate(admitEverySubject{}, nil)
			reader := directread.NewFactsReader(gate, directread.NewFactReader(registry.WithoutScopeExpansion()), nil)
			response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000000002"), storage.Principal{OrgID: orgID, Subject: "fractional", CredentialID: "fractional"}, directread.FactsRequest{
				Kinds:    []string{"metrics"},
				Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: "team:team-share"}},
			})
			if err != nil {
				t.Fatalf("read_facts: %v", err)
			}
			var fact *directread.ServedFact
			for i := range response.Facts {
				if response.Facts[i].Kind == "metrics" && response.Facts[i].Subject.CanonicalID == "team:team-share" {
					fact = &response.Facts[i]
				}
			}
			if fact == nil {
				t.Fatalf("no metrics fact served for the team: %#v", response.Facts)
			}
			for i, field := range []string{"commits_count", "after_hours_commits_count", "weekend_commits_count"} {
				got, ok := fact.Fields[field].(float64)
				if !ok {
					t.Fatalf("%s = %#v (%T), want a number served as float64, never an integer", field, fact.Fields[field], fact.Fields[field])
				}
				if math.Abs(got-tc.want[i]) > 1e-9 {
					t.Fatalf("%s = %v, want %v", field, got, tc.want[i])
				}
			}
		})
	}
}
