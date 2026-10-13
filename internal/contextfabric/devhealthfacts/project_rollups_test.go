package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	projectPullRequestRollupMatch = "lp.project_key"
	projectBlockerRollupMatch     = "SELECT project_key, toInt64(uniqExactIf(tgt, is_open = 1))"
)

func projectRollupSubject(t *testing.T, id string) contextfabric.SubjectRef {
	t.Helper()
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectCanonicalID(t, "github", id), Label: id}
}

func readProjectRollup(t *testing.T, client *fakeClient, kind contextfabric.FactKind, timeContext contextfabric.TimeContext, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-a", RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Kind: kind, Subjects: subjects, Time: timeContext,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result
}

func TestProjectPullRequestRollupCountsEachPullRequestOnceAndNamesTheWeakestTier(t *testing.T) {
	t.Parallel()
	project := projectRollupSubject(t, "P1")
	client := &fakeClient{tables: []fakeTable{{match: projectPullRequestRollupMatch, rows: [][]any{
		{"github:P1", int64(1), int64(1), int64(0), int64(0), int64(1)},
		{"github:P1", int64(3), int64(2), int64(1), int64(1), int64(0)},
	}}}}
	result := readProjectRollup(t, client, contextfabric.FactPullRequests, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, project)
	fact := factFor(t, result, project)
	wantInt(t, fact.Fields, "pull_requests_open_current", 3)
	wantInt(t, fact.Fields, "pull_requests_opened_window", 1)
	wantInt(t, fact.Fields, "pull_requests_merged_window", 1)
	wantInt(t, fact.Fields, "pull_requests_closed_unmerged_window", 1)
	if got := fact.Fields["pull_request_link_basis"]; got.String == nil || *got.String != "heuristic" {
		t.Fatalf("pull_request_link_basis = %#v, want heuristic (the weakest tier counted)", got)
	}
	if got := fact.Fields["rollup_basis"]; got.String == nil || *got.String != "project_issues" {
		t.Fatalf("rollup_basis = %#v, want project_issues", got)
	}
	table := fact.Fields["link_tier_breakdown"].Table
	if table == nil || len(table.Rows) != 2 {
		t.Fatalf("link_tier_breakdown = %#v, want one row per tier", fact.Fields["link_tier_breakdown"])
	}
	if tier := table.Rows[0].Fields["link_tier"]; tier.String == nil || *tier.String != "native" {
		t.Fatalf("first tier row = %#v, want native first", table.Rows[0].Fields)
	}
	if open := table.Rows[0].Fields["pull_requests_open_current"]; open.Integer == nil || *open.Integer != 2 {
		t.Fatalf("native open = %#v, want 2", table.Rows[0].Fields)
	}
	if len(fact.EvidenceRefIDs) != 1 || !strings.Contains(fact.EvidenceRefIDs[0], "project") {
		t.Fatalf("evidence = %v, want the project reference", fact.EvidenceRefIDs)
	}
	if ids := client.idsBinding(); len(ids) != 1 || ids[0] != "github:P1" {
		t.Fatalf("ids binding = %v, want the requested project only", ids)
	}
}

func TestProjectPullRequestRollupWithholdsTheOpenCountOnAHistoricalRead(t *testing.T) {
	t.Parallel()
	project := projectRollupSubject(t, "P1")
	client := &fakeClient{tables: []fakeTable{{match: projectPullRequestRollupMatch, rows: [][]any{
		{"github:P1", int64(3), int64(2), int64(1), int64(1), int64(0)},
	}}}}
	start, end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	result := readProjectRollup(t, client, contextfabric.FactPullRequests, contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, project)
	fact := factFor(t, result, project)
	if _, ok := fact.Fields["pull_requests_open_current"]; ok {
		t.Fatalf("a historical read serves pull_requests_open_current: %#v", fact.Fields)
	}
	for _, row := range fact.Fields["link_tier_breakdown"].Table.Rows {
		if _, ok := row.Fields["pull_requests_open_current"]; ok {
			t.Fatalf("a historical read serves an open count per tier: %#v", row.Fields)
		}
	}
	wantInt(t, fact.Fields, "pull_requests_opened_window", 1)
	if !strings.Contains(result.Reason, "link rows carry no validity window") {
		t.Fatalf("reason = %q, want the unwindowed link limitation", result.Reason)
	}
}

func TestProjectRollupsServeNoFactForAProjectWithNothingCounted(t *testing.T) {
	t.Parallel()
	project := projectRollupSubject(t, "P1")
	for _, kind := range []contextfabric.FactKind{contextfabric.FactPullRequests, contextfabric.FactBlockers} {
		result := readProjectRollup(t, &fakeClient{}, kind, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, project)
		if len(result.Facts) != 0 {
			t.Fatalf("%s: facts = %#v, want none (absent, never zero)", kind, result.Facts)
		}
	}
}

func TestProjectBlockerRollupServesTheProjectIssueCounts(t *testing.T) {
	t.Parallel()
	project := projectRollupSubject(t, "P1")
	client := &fakeClient{tables: []fakeTable{{match: projectBlockerRollupMatch, rows: [][]any{
		{"github:P1", int64(2), int64(3), int64(1), int64(0)},
	}}}}
	result := readProjectRollup(t, client, contextfabric.FactBlockers, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, project)
	fact := factFor(t, result, project)
	wantInt(t, fact.Fields, "blocked_work_items_current", 2)
	wantInt(t, fact.Fields, "blocker_dependencies_current", 3)
	wantInt(t, fact.Fields, "blocker_dependencies_excluded_by_status_current", 1)
	if got := fact.Fields["blocker_status_basis"]; got.String == nil || *got.String != "open" {
		t.Fatalf("blocker_status_basis = %#v, want open", got)
	}
}

// Every work_items read of a project rollup statement is narrowed to the ids
// a link row or a blocker edge names, or is the requested projects' candidate
// id read (no FINAL, by project join key), and none reads the whole
// organization.
func TestProjectRollupStatementsReadWorkItemsOnlyForNamedIDs(t *testing.T) {
	t.Parallel()
	project := projectRollupSubject(t, "P1")
	client := &fakeClient{}
	readProjectRollup(t, client, contextfabric.FactPullRequests, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, project)
	readProjectRollup(t, client, contextfabric.FactBlockers, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, project)
	seen := 0
	for _, query := range client.queries {
		statement := strings.Join(strings.Fields(query.statement), " ")
		if !strings.Contains(statement, projectPullRequestRollupMatch) && !strings.Contains(statement, projectBlockerRollupMatch) {
			continue
		}
		seen++
		for offset := strings.Index(statement, "FROM work_items"); offset >= 0; {
			where := sameLevelWhereClause(statement[offset+len("FROM work_items"):])
			if !strings.Contains(where, "work_item_id IN") && !strings.Contains(where, "(provider, project_id) IN (SELECT provider, join_key FROM") {
				t.Fatalf("a work_items read is not narrowed to named ids (WHERE %q) in: %s", where, statement)
			}
			next := strings.Index(statement[offset+1:], "FROM work_items")
			if next < 0 {
				break
			}
			offset += next + 1
		}
		if strings.Contains(statement, "LEFT JOIN work_items") {
			t.Fatalf("statement joins the whole work_items table: %s", statement)
		}
	}
	if seen != 2 {
		t.Fatalf("project rollup statements seen = %d, want 2 (measurement did not happen)", seen)
	}
}

// sameLevelWhereClause returns the WHERE clause of the SELECT whose FROM the
// text follows: the first WHERE at the same parenthesis depth, up to the end
// of that SELECT.
func sameLevelWhereClause(text string) string {
	depth, start := 0, -1
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				if start >= 0 {
					return text[start:i]
				}
				return ""
			}
		}
		if depth != 0 {
			continue
		}
		if start < 0 && strings.HasPrefix(text[i:], "WHERE ") {
			start = i
		} else if start >= 0 && (strings.HasPrefix(text[i:], "GROUP BY ") || strings.HasPrefix(text[i:], "ORDER BY ")) {
			return text[start:i]
		}
	}
	if start >= 0 {
		return text[start:]
	}
	return ""
}

// A project rollup field is a count, never a score: pull request counts are
// aggregates over every linked pull request, blocker counts are over the
// caller's authorized items.
func TestProjectRollupDeclarationMarks(t *testing.T) {
	t.Parallel()
	for kind, mark := range map[contextfabric.FactKind]string{contextfabric.FactPullRequests: "aggregate", contextfabric.FactBlockers: "caller_scoped"} {
		capability := findProvider(t, devhealthfacts.NewProviders(&fakeClient{}), kind).Capability()
		counts := 0
		for _, field := range capability.Fields {
			if len(field.SubjectKinds) != 1 || field.SubjectKinds[0] != contextfabric.SubjectProject {
				continue
			}
			if field.Score {
				t.Errorf("%s %s: a project rollup field is a count, never a score", kind, field.Name)
			}
			if field.Type != contextfabric.FactFieldInteger {
				continue
			}
			counts++
			if (mark == "aggregate" && !field.Aggregate) || (mark == "caller_scoped" && !field.CallerScoped) {
				t.Errorf("%s %s: Aggregate=%v CallerScoped=%v, want %s", kind, field.Name, field.Aggregate, field.CallerScoped, mark)
			}
		}
		if counts == 0 {
			t.Fatalf("%s declares no project count", kind)
		}
	}
}
