package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

func canonicalProjectID(t *testing.T, provider, id string) string {
	t.Helper()
	canonical, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive(project, %q, %q): omitted=%v err=%v", provider, id, omitted, err)
	}
	return canonical
}

// pullRequestBreakdownFact is a pull request fact carrying a project_breakdown
// the way the pull request provider emits it: one row per project, scope and
// scope_id, the id a canonical project identity.
func pullRequestBreakdownFact(memberID string, rows ...map[string]string) CanonicalFact {
	table := make([]FactValueRow, 0, len(rows))
	for _, row := range rows {
		fields := map[string]FactValue{}
		for column, value := range row {
			fields[column] = StringFactValue(value)
		}
		table = append(table, FactValueRow{Fields: fields})
	}
	return CanonicalFact{
		Kind:    FactPullRequests,
		Subject: SubjectRef{Kind: SubjectPullRequest, CanonicalID: memberID, Label: memberID},
		Fields: map[string]FactValue{
			"project_breakdown": TableFactValue(FactTable{Shape: FactTableBreakdown, Key: []string{"scope_id"}, Rows: table}),
		},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

func TestAProjectGroupAxisIsBuiltFromCanonicalProjectRows(t *testing.T) {
	t.Parallel()
	a, b := canonicalProjectID(t, "jira", "A"), canonicalProjectID(t, "jira", "B")
	facts := []CanonicalFact{
		pullRequestBreakdownFact("pull_request:r:1", map[string]string{"scope": "project", "scope_id": a, "scope_name": "Alpha", "link_tier": "native"}),
		// one pull request reaching two projects belongs under both
		pullRequestBreakdownFact("pull_request:r:2",
			map[string]string{"scope": "project", "scope_id": a, "scope_name": "Alpha", "link_tier": "heuristic"},
			map[string]string{"scope": "project", "scope_id": b, "scope_name": "Beta", "link_tier": "explicit_text"}),
	}
	groups, ungrouped, outcome := BuildCohortGroups(AnswerPlan{GroupKind: SubjectProject}, prCohort("pull_request:r:1", "pull_request:r:2", "pull_request:r:3"), facts)
	if outcome.Refusal != CohortGroupingRefusalNone || ungrouped != 1 || len(groups) != 2 {
		t.Fatalf("groups=%d ungrouped=%d outcome=%+v, want two project groups and one unplaced member", len(groups), ungrouped, outcome)
	}
	byID := map[string]int{}
	for _, group := range groups {
		if group.Subject.Kind != SubjectProject {
			t.Fatalf("group kind = %q, want project", group.Subject.Kind)
		}
		byID[group.Subject.CanonicalID] = group.Total
	}
	if byID[a] != 2 || byID[b] != 1 {
		t.Fatalf("group totals = %v, want %s=2 %s=1", byID, a, b)
	}
}

func TestAProjectRowWithANonCanonicalIdPlacesNobody(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{
		pullRequestBreakdownFact("pull_request:r:1", map[string]string{"scope": "project", "scope_id": "A", "scope_name": "Alpha"}),
		pullRequestBreakdownFact("pull_request:r:2", map[string]string{"scope": "project", "scope_id": "project:A"}),
	}
	groups, ungrouped, outcome := BuildCohortGroups(AnswerPlan{GroupKind: SubjectProject}, prCohort("pull_request:r:1", "pull_request:r:2"), facts)
	if len(groups) != 0 || ungrouped != 2 || outcome.Refusal != CohortGroupingRefusalNoMemberPlaced {
		t.Fatalf("groups=%d ungrouped=%d outcome=%+v, want no_member_placed: a raw key is never composed into a project identity", len(groups), ungrouped, outcome)
	}
}

func TestATeamQuestionIsNotPlacedByProjectRows(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{pullRequestBreakdownFact("pull_request:r:1", map[string]string{"scope": "project", "scope_id": canonicalProjectID(t, "jira", "A")})}
	groups, _, outcome := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, prCohort("pull_request:r:1"), facts)
	if len(groups) != 0 || outcome.Refusal != CohortGroupingRefusalGroupKindSourceMismatch || outcome.SourceKind != SubjectProject {
		t.Fatalf("groups=%d outcome=%+v, want a kind mismatch naming the project source", len(groups), outcome)
	}
}

func TestMixedTeamAndProjectRowsPlaceByTheAskedAxisOnly(t *testing.T) {
	t.Parallel()
	a := canonicalProjectID(t, "jira", "A")
	fact := pullRequestBreakdownFact("pull_request:r:1",
		map[string]string{"scope": "project", "scope_id": a, "scope_name": "Alpha"},
		map[string]string{"scope": "team", "scope_id": "t1", "scope_name": "Platform"})
	for _, tc := range []struct {
		kind SubjectKind
		want string
	}{{SubjectProject, a}, {SubjectTeam, "team:t1"}} {
		groups, _, outcome := BuildCohortGroups(AnswerPlan{GroupKind: tc.kind}, prCohort("pull_request:r:1"), []CanonicalFact{fact})
		if outcome.Refusal != CohortGroupingRefusalNone || len(groups) != 1 || groups[0].Subject.CanonicalID != tc.want || groups[0].Subject.Kind != tc.kind {
			t.Fatalf("%s: groups=%+v outcome=%+v, want one group %s", tc.kind, groups, outcome, tc.want)
		}
	}
}
