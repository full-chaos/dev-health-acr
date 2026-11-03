package devhealthfacts_test

// A landscape identity with no merged pull request in its window has no cycle:
// Ops stores NULL, not 0. A team or project row whose identities hold no cycle
// serves no cycle, and a stored 0 still reads 0.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func landscapeTeamRowCycle(teamID string, identityCount int64, cycle any) []any {
	return []any{teamID, "2026-02-21", identityCount, int64(100), int64(3), cycle, int64(2), int64(0)}
}

func landscapeProjectRowCycle(teamID string, identityCount int64, cycle any) []any {
	return []any{"linear:proj-1", teamID, "2026-02-21", identityCount, int64(100), int64(3), cycle, int64(2), int64(0)}
}

func requireNoCycle(t *testing.T, label string, value contextfabric.FactValue) {
	t.Helper()
	if !value.Null || value.Number != nil || value.Integer != nil {
		t.Fatalf("%s: cycle_p50_30d_hours_avg = %#v, want null: a team with no cycle must not read as a 0-hour cycle", label, value)
	}
}

func requireCycle(t *testing.T, label string, value contextfabric.FactValue, want float64) {
	t.Helper()
	if value.Null || value.Number == nil || *value.Number != want {
		t.Fatalf("%s: cycle_p50_30d_hours_avg = %#v, want %v", label, value, want)
	}
}

func TestLandscapeTeamWithNoCycleServesNoCycle(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "sumIf(one_churn", rows: [][]any{
		landscapeTeamRowCycle("team-none", 3, nil),
		landscapeTeamRowCycle("team-zero", 2, float64(0)),
		landscapeTeamRowCycle("team-some", 2, 7.5),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{teamSubject("team-none"), teamSubject("team-zero"), teamSubject("team-some")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 3 {
		t.Fatalf("facts = %d, want 3: a team with no cycle still has a landscape", len(result.Facts))
	}
	byTeam := map[string]contextfabric.CanonicalFact{}
	for _, fact := range result.Facts {
		byTeam[fact.Subject.CanonicalID] = fact
	}
	for _, fact := range result.Facts {
		if _, scalar := fact.Fields["cycle_p50_30d_hours_avg"]; scalar {
			t.Fatalf("%s: the cycle is served as a team scalar, but answers hold it out", fact.Subject.CanonicalID)
		}
	}
	none := byTeam[teamSubject("team-none").CanonicalID]
	noneRow := none.Fields["area_breakdown"].Rows[0]
	requireNoCycle(t, "team-none area_breakdown", noneRow.Fields["cycle_p50_30d_hours_avg"])
	if got := noneRow.Fields["identity_count"].Integer; got == nil || *got != 3 {
		t.Fatalf("team-none identity_count = %#v, want 3: the people are served without a cycle", noneRow.Fields["identity_count"])
	}
	zero := byTeam[teamSubject("team-zero").CanonicalID]
	requireCycle(t, "team-zero area_breakdown", zero.Fields["area_breakdown"].Rows[0].Fields["cycle_p50_30d_hours_avg"], 0)
	some := byTeam[teamSubject("team-some").CanonicalID]
	requireCycle(t, "team-some area_breakdown", some.Fields["area_breakdown"].Rows[0].Fields["cycle_p50_30d_hours_avg"], 7.5)
	for _, fact := range result.Facts {
		if err := fact.Fields["area_breakdown"].Table.Validate(); err != nil {
			t.Fatalf("%s area_breakdown: %v", fact.Subject.CanonicalID, err)
		}
	}
}

func TestLandscapeProjectTeamWithNoCycleServesNoCycle(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "sumIf(il.one_churn", rows: [][]any{
		landscapeProjectRowCycle("team-none", 3, nil),
		landscapeProjectRowCycle("team-zero", 2, float64(0)),
		landscapeProjectRowCycle("team-some", 2, 7.5),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	rows := result.Facts[0].Fields["team_breakdown"].Rows
	if len(rows) != 3 {
		t.Fatalf("team_breakdown rows = %d, want 3", len(rows))
	}
	for _, row := range rows {
		switch team := *row.Fields["team_id"].String; team {
		case "team-none":
			requireNoCycle(t, "project row team-none", row.Fields["cycle_p50_30d_hours_avg"])
		case "team-zero":
			requireCycle(t, "project row team-zero", row.Fields["cycle_p50_30d_hours_avg"], 0)
		case "team-some":
			requireCycle(t, "project row team-some", row.Fields["cycle_p50_30d_hours_avg"], 7.5)
		default:
			t.Fatalf("unexpected team %s", team)
		}
	}
	if err := result.Facts[0].Fields["team_breakdown"].Table.Validate(); err != nil {
		t.Fatalf("team_breakdown: %v", err)
	}
}

func TestLandscapeDeclaresTheCycleNullable(t *testing.T) {
	t.Parallel()
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{}), contextfabric.FactLandscape)
	capability := provider.Capability()
	for _, table := range []struct {
		name    string
		subject contextfabric.SubjectKind
	}{{"area_breakdown", contextfabric.SubjectTeam}, {"team_breakdown", contextfabric.SubjectProject}} {
		decl, ok := capability.FieldDeclaration(table.name, table.subject)
		if !ok {
			t.Fatalf("%s is not declared", table.name)
		}
		column, ok := decl.Column("cycle_p50_30d_hours_avg")
		if !ok || !column.Nullable || column.Type != contextfabric.FactFieldNumber {
			t.Fatalf("%s.cycle_p50_30d_hours_avg = %#v (declared %v), want a nullable number", table.name, column, ok)
		}
	}
}
