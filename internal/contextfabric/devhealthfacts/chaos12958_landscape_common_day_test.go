package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// commonDayMarker occurs only in the statement that finds the latest day every
// team of the read holds live rows.
const commonDayMarker = "day_identities"

func twoTeamLandscapeRows(ownDay, otherDay string) [][]any {
	return [][]any{
		{"ops", ownDay, int64(5), int64(441951), int64(397), 12.5, int64(3), int64(0)},
		{"chaos", otherDay, int64(7), int64(2094394), int64(2217), 18.5, int64(6), int64(0)},
	}
}

func readTwoTeamLandscape(t *testing.T, client *fakeClient) contextfabric.FactProviderResult {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{teamSubject("ops"), teamSubject("chaos")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result
}

func commonDayOf(t *testing.T, fact contextfabric.CanonicalFact) (string, bool) {
	t.Helper()
	value, ok := fact.Fields["common_live_day"]
	if !ok {
		return "", false
	}
	if value.String == nil {
		t.Fatalf("common_live_day is not a string: %#v", value)
	}
	return *value.String, true
}

func commonDayStatements(client *fakeClient) []capturedQuery {
	var found []capturedQuery
	for _, query := range client.queries {
		if strings.Contains(query.statement, commonDayMarker) {
			found = append(found, query)
		}
	}
	return found
}

// Teams whose own latest days differ each carry the latest day they share, and
// each fact keeps its own latest day (CHAOS-4611 ruling 3: the quadrant is never
// read on the common day).
func TestLandscapeTeamsOnDifferentDaysCarryTheirLatestSharedDay(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: commonDayMarker, rows: [][]any{{"2026-10-07"}}},
		{match: "sumIf(one_churn", rows: twoTeamLandscapeRows("2026-10-07", "2026-11-04")},
	}}
	result := readTwoTeamLandscape(t, client)
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(result.Facts))
	}
	ownDays := map[string]string{}
	for _, fact := range result.Facts {
		day, ok := commonDayOf(t, fact)
		if !ok || day != "2026-10-07" {
			t.Fatalf("%s: common_live_day = %q, %v; want 2026-10-07", fact.Subject.Label, day, ok)
		}
		rows := fact.Fields["area_breakdown"].Rows
		ownDays[fact.Subject.Label] = *rows[0].Fields["as_of_day"].String
	}
	if ownDays["ops"] != "2026-10-07" || ownDays["chaos"] != "2026-11-04" {
		t.Fatalf("a fact's own day moved onto the common day: %v", ownDays)
	}
	statements := commonDayStatements(client)
	if len(statements) != 1 {
		t.Fatalf("%d common-day statements, want 1", len(statements))
	}
	for _, part := range []string{"HAVING uniqExact(team_id) = 2", fmt.Sprintf("day_identities >= %d", contextfabric.LandscapeMinIdentities), "WHERE is_live", "ORDER BY as_of_day DESC", "LIMIT 1"} {
		if !strings.Contains(statements[0].statement, part) {
			t.Errorf("the statement lacks %q:\n%s", part, statements[0].statement)
		}
	}
}

// The teams share no day: the fact carries no field, so the answer offers no
// window (check 12: absent is not a day).
func TestLandscapeTeamsWithNoSharedDayCarryNone(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "sumIf(one_churn", rows: twoTeamLandscapeRows("2026-10-07", "2026-11-04")},
	}}
	result := readTwoTeamLandscape(t, client)
	for _, fact := range result.Facts {
		if day, ok := commonDayOf(t, fact); ok {
			t.Fatalf("%s: common_live_day = %q with no shared day", fact.Subject.Label, day)
		}
	}
	if len(commonDayStatements(client)) != 1 {
		t.Fatal("the shared day was not looked for")
	}
}

// Teams already on one day need no second read.
func TestLandscapeTeamsOnOneDayAreNotReadForASharedDay(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: commonDayMarker, rows: [][]any{{"2026-01-01"}}},
		{match: "sumIf(one_churn", rows: twoTeamLandscapeRows("2026-11-04", "2026-11-04")},
	}}
	result := readTwoTeamLandscape(t, client)
	if n := len(commonDayStatements(client)); n != 0 {
		t.Fatalf("%d common-day statements for teams on one day", n)
	}
	for _, fact := range result.Facts {
		if _, ok := commonDayOf(t, fact); ok {
			t.Fatalf("%s carries a common day for teams on one day", fact.Subject.Label)
		}
	}
}

// One team is no pair.
func TestLandscapeOneTeamIsNotReadForASharedDay(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "sumIf(one_churn", rows: [][]any{landscapeTeamRow("ops", 4, 1200, 30, 18.5, 6)}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{teamSubject("ops")},
	}); err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if n := len(commonDayStatements(client)); n != 0 {
		t.Fatalf("%d common-day statements for one team", n)
	}
}
