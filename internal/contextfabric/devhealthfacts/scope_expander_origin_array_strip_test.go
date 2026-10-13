package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// The memory guard re-runs each expander statement without its origin id
// arrays; the stripped text must lose every array expression and keep a
// well-formed select list.
func TestStripOriginArraysLeavesEachExpanderStatementWellFormed(t *testing.T) {
	recorder := &recordingQueryClient{inner: &fakeClient{}}
	expander := devhealthfacts.NewScopeExpander(recorder)
	fixture := everyRootFixture{
		orgID: "org-strip", shared: repoUUID("strip-shared"), own: repoUUID("strip-own"),
		teamA: teamSubject("A"), teamB: teamSubject("B"), teamC: teamSubject("C"),
		projectP: projectSubject("linear", "P"), projectQ: projectSubject("linear", "Q"), projectR: projectSubject("linear", "R"),
	}
	for _, tc := range fixture.cases() {
		if _, err := expander.ExpandFactScope(context.Background(), tc.request); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	for _, statement := range everyRootStatementFragments {
		captured, ok := recorder.find(statement.fragment)
		if !ok {
			t.Fatalf("%s: no statement carrying %q", statement.label, statement.fragment)
		}
		if !strings.Contains(captured.statement, "arraySort(groupUniqArray(") {
			t.Fatalf("%s: the served statement carries no origin array", statement.label)
		}
		stripped := stripOriginArrays(captured.statement)
		// Every other aggregate stays: only the origin arrays go.
		if strings.Count(captured.statement, "groupUniqArray")-strings.Count(stripped, "groupUniqArray") != strings.Count(captured.statement, "arraySort(groupUniqArray(")-strings.Count(stripped, "arraySort(groupUniqArray(") {
			t.Fatalf("%s: stripping removed an aggregate that is not an origin array", statement.label)
		}
		if strings.Contains(captured.statement, "wia_project_repositories") && !strings.Contains(stripped, "AS wia_project_repositories") {
			t.Fatalf("%s: stripping removed the work-item authorization's repository list", statement.label)
		}
		for _, banned := range []string{"groupUniqArray(p.id)", "groupUniqArray(a.origin_project_id)", "groupUniqArray(origin_team_id)", "groupUniqArray(a.team_id)", "groupUniqArray(own.team_id)", "groupUniqArray(concat(p.provider", "origin_ids", "origin_team_ids", "origin_project_ids", "project_keys", ",,", ", ,", ",\nFROM", ", FROM", "SELECT ,", "population,\nFROM", "population, FROM"} {
			if at := strings.Index(stripped, banned); at >= 0 {
				t.Fatalf("%s: stripped statement still holds %q near: %s", statement.label, banned, stripped[max(0, at-200):min(len(stripped), at+120)])
			}
		}
		if strings.Count(stripped, "(") != strings.Count(stripped, ")") {
			t.Fatalf("%s: unbalanced parentheses after stripping:\n%s", statement.label, stripped)
		}
		if !strings.Contains(stripped, statement.fragment) {
			t.Fatalf("%s: stripping removed the statement's own fragment", statement.label)
		}
	}
}
