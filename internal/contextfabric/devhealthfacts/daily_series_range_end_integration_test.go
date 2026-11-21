package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// An explicit read_facts range keeps its end day (read_facts takes at most 60
// days for these kinds, so its tables are never cut by the 64-row display cap). For each of the eight team
// and project daily-series statements, a range that ends at a UTC midnight
// serves the end day the statement counted, so rows shown plus rows omitted is
// the statement's own day count and the newest day served is the end day. The
// workload series is keyed by its forecast run, which here runs at 06:00: a
// range that ends at midnight holds none of the end day's run, the statement
// does not count it, and the table names the end day as a missing point.
func TestRangeEndingAtMidnightServesTheEndDayOfEveryDailySeriesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createDailySeriesTables(t, ctx, direct)
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitEverySubject{}, nil), directread.NewFactReader(registry.WithoutScopeExpansion()), nil)
	newest := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	for _, kind := range dailySeriesKinds {
		for _, tc := range []struct {
			days  int
			end   time.Time
			label string
		}{
			{60, newest, "midnight"},
			{60, newest.Add(time.Hour), "01:00"},
		} {
			t.Run(fmt.Sprintf("%s_%d_days_end_%s", kind.table, tc.days, tc.label), func(t *testing.T) {
				orgID := fmt.Sprintf("org-range-end-%s-%d-%s", kind.table, tc.days, tc.label)
				const teamID, projectID, projectKey = "team-x", "proj-x", "PROJX"
				dailySeriesExec(t, ctx, direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('`+projectID+`', '`+orgID+`', 'linear', '`+projectKey+`', 'Project', 1, 'active', '', toDateTime('2026-01-01 00:00:00'))`)
				dailySeriesExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES ('`+teamID+`', '`+teamID+`', NULL, toDateTime('2026-01-01 00:00:00'), '`+orgID+`', 'linear', [], 1)`)
				dailySeriesExec(t, ctx, direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES ('`+orgID+`', 'linear', '`+teamID+`', 'irrelevant', '`+projectKey+`', 'native', toDateTime('2026-01-01 00:00:00'), NULL, toDateTime('2026-01-01 00:00:00'))`)
				kind.seed(t, ctx, direct, orgID, teamID, projectID, tc.days, false)
				start := newest.AddDate(0, 0, -(tc.days - 1))
				project := projectSubject("linear", projectID)
				response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000000004"), storage.Principal{OrgID: orgID, Subject: "range-end", CredentialID: "range-end"}, directread.FactsRequest{
					Kinds: []string{string(kind.kind)}, Tables: "include",
					Window:   &directread.RequestWindow{Mode: directread.WindowRange, Start: &start, End: &tc.end},
					Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: "team:" + teamID}, {Kind: "project", CanonicalID: project.CanonicalID}},
				})
				if err != nil {
					t.Fatalf("read_facts: %v", err)
				}
				counted, newestServed, missing := tc.days, "2026-08-31", []string(nil)
				if kind.kind == contextfabric.FactWorkload {
					counted, newestServed, missing = tc.days-1, "2026-08-30", []string{"2026-08-31"}
				}
				shown := min(counted, contextfabric.MaxFactValueRows)
				for _, canonicalID := range []string{"team:" + teamID, project.CanonicalID} {
					var table *directread.ServedTable
					for i := range response.Facts {
						if served, ok := response.Facts[i].Tables[kind.table]; ok && response.Facts[i].Subject.CanonicalID == canonicalID {
							table = &served
						}
					}
					if table == nil {
						t.Fatalf("%s: no %s table served: %+v", canonicalID, kind.table, response.Coverage)
					}
					if table.RowsReturned != shown || table.RowsOmitted != counted-shown || table.NewestReturned != newestServed {
						t.Errorf("%s %s rows=%d omitted=%d newest=%q, want %d, %d, %q", canonicalID, kind.table, table.RowsReturned, table.RowsOmitted, table.NewestReturned, shown, counted-shown, newestServed)
					}
					if table.ExpectedPoints == nil || *table.ExpectedPoints != tc.days || *table.ReturnedPoints != shown {
						t.Errorf("%s %s expected=%v returned=%v, want %d, %d", canonicalID, kind.table, table.ExpectedPoints, table.ReturnedPoints, tc.days, shown)
					}
					if fmt.Sprint(table.MissingInstants) != fmt.Sprint(missing) {
						t.Errorf("%s %s missing=%v, want %v", canonicalID, kind.table, table.MissingInstants, missing)
					}
				}
			})
		}
	}
}

// A range longer than the display cap, as the investigation path reads it:
// each team and project statement counts the end day of a range that ends at a
// UTC midnight, shows the newest 64 days from that end day, and names the rest.
func TestRangeEndingAtMidnightPastTheDisplayCapCountsExactlyAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createDailySeriesTables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)
	const days = 70
	end := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -(days - 1))
	for _, kind := range dailySeriesKinds {
		t.Run(kind.table, func(t *testing.T) {
			orgID := "org-range-end-past-cap-" + kind.table
			const teamID, projectID, projectKey = "team-x", "proj-x", "PROJX"
			dailySeriesExec(t, ctx, direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('`+projectID+`', '`+orgID+`', 'linear', '`+projectKey+`', 'Project', 1, 'active', '', toDateTime('2026-01-01 00:00:00'))`)
			dailySeriesExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES ('`+teamID+`', '`+teamID+`', NULL, toDateTime('2026-01-01 00:00:00'), '`+orgID+`', 'linear', [], 1)`)
			dailySeriesExec(t, ctx, direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES ('`+orgID+`', 'linear', '`+teamID+`', 'irrelevant', '`+projectKey+`', 'native', toDateTime('2026-01-01 00:00:00'), NULL, toDateTime('2026-01-01 00:00:00'))`)
			kind.seed(t, ctx, direct, orgID, teamID, projectID, days, false)
			counted, newestDay := days, "2026-08-31"
			if kind.kind == contextfabric.FactWorkload {
				counted, newestDay = days-1, "2026-08-30"
			}
			result, err := findProvider(t, providers, kind.kind).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
				Kind: kind.kind, Subjects: []contextfabric.SubjectRef{teamSubject(teamID), projectSubject("linear", projectID)},
			})
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			seen := 0
			for _, fact := range result.Facts {
				table, ok := fact.Fields[kind.table]
				if !ok {
					continue
				}
				seen++
				rows := table.Rows
				omitted, _ := dailyOmittedCount(fact, kind.table)
				if len(rows) != contextfabric.MaxFactValueRows || omitted != int64(counted-contextfabric.MaxFactValueRows) {
					t.Errorf("%s rows=%d omitted=%d, want %d, %d", fact.Subject.CanonicalID, len(rows), omitted, contextfabric.MaxFactValueRows, counted-contextfabric.MaxFactValueRows)
				}
				if got := rows[0].Fields["day"].String; got == nil || *got != newestDay {
					t.Errorf("%s newest day = %v, want %s", fact.Subject.CanonicalID, got, newestDay)
				}
			}
			if seen != 2 {
				t.Fatalf("facts with %s = %d, want the team and the project", kind.table, seen)
			}
		})
	}
}
