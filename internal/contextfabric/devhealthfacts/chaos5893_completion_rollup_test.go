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

// completionRollupQueryMatch is the marker unique to
// workItemProjectCompletionStatement's aggregate -- both it and the
// per-item ActualCompletionProvider statement contain "FROM work_items",
// so tests must key on the GROUP BY the aggregate alone has.
const completionRollupQueryMatch = "GROUP BY p.provider, p.id"

// completionRollupRow shapes one row of workItemProjectCompletionStatement's
// SELECT list: (project key, work_item_count, cancelled_count,
// unknown_status_count, completed_count).
func completionRollupRow(provider, projectID string, workItemCount, cancelledCount, unknownStatusCount, completedCount uint64) []any {
	return []any{provider + ":" + projectID, workItemCount, cancelledCount, unknownStatusCount, completedCount}
}

func readProjectCompletion(t *testing.T, client *fakeClient, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactActualCompletion)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactActualCompletion, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result
}

// --- Domain sweep: members x completion x cancelled x unknown-status ---

func TestActualCompletionProjectRollup_AllComplete(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 5, 0, 0, 5),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	assertInt(t, fact, "work_item_count", 5)
	assertInt(t, fact, "counted_work_items", 5)
	assertInt(t, fact, "completed_count", 5)
	assertNumber(t, fact, "completion_ratio", 1.0)
}

func TestActualCompletionProjectRollup_NoneComplete(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 5, 0, 0, 0),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	// A genuinely computed 0% (real members, real zero completions) is not
	// the "defaulted 0%" chris's ruling forbids -- that prohibition is about
	// a project with NOTHING countable being reported as 0%, covered by the
	// AllCancelled/ZeroMembers tests below.
	assertNumber(t, result.Facts[0], "completion_ratio", 0.0)
}

func TestActualCompletionProjectRollup_Mixed(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 10, 0, 0, 4),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	assertNumber(t, result.Facts[0], "completion_ratio", 0.4)
}

func TestActualCompletionProjectRollup_ZeroMembers_NoFact(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: nil}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-404"))
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- a project with no matching work items gets no row from the GROUP BY", result.Facts)
	}
	if result.State != contextfabric.SourceNoData {
		t.Fatalf("State = %q, want %q -- never a defaulted 0%%/100%% for a project with nothing to count", result.State, contextfabric.SourceNoData)
	}
}

func TestActualCompletionProjectRollup_CancelledSome_ExcludedFromRatioAndDisclosed(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 10, 3, 0, 4),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	fact := result.Facts[0]
	assertInt(t, fact, "work_item_count", 10)
	assertInt(t, fact, "cancelled_count", 3)
	assertInt(t, fact, "counted_work_items", 7)
	assertInt(t, fact, "completed_count", 4)
	assertNumber(t, fact, "completion_ratio", 4.0/7.0)
}

func TestActualCompletionProjectRollup_CancelledAll_NoFact(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 3, 3, 0, 0),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- an all-cancelled project has nothing left to count", result.Facts)
	}
	if result.State != contextfabric.SourceNoData {
		t.Fatalf("State = %q, want %q -- ClickHouse answered (the row exists) but nothing was SERVABLE; collapsing rowCount into servedCount would wrongly report available with zero facts", result.State, contextfabric.SourceNoData)
	}
}

func TestActualCompletionProjectRollup_UnknownStatus_CountedAndDisclosed(t *testing.T) {
	t.Parallel()
	// 10 members, 0 cancelled, 2 unknown-status, 3 completed: unknown-status
	// items are NOT excluded -- counted_work_items stays 10, only disclosed
	// separately via unknown_status_count.
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 10, 0, 2, 3),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	fact := result.Facts[0]
	assertInt(t, fact, "unknown_status_count", 2)
	assertInt(t, fact, "counted_work_items", 10)
	assertNumber(t, fact, "completion_ratio", 0.3)
}

func TestActualCompletionProjectRollup_ArchivedItemsAlwaysDisclosedAsAbsent(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 5, 0, 0, 5),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	fact := result.Facts[0]
	if fact.Fields["archived_items"].String == nil || *fact.Fields["archived_items"].String != "absent_from_source" {
		t.Fatalf("archived_items = %#v, want the fixed absent-from-source constant -- an archived item leaves this source's own sync and is never a work_items row at all, so it is already excluded by construction", fact.Fields["archived_items"])
	}
	if _, hasArchivedCount := fact.Fields["archived_count"]; hasArchivedCount {
		t.Fatalf("fields = %#v, want no archived_count -- there is nothing to count; archived items are absent from the source, not filtered by this producer", fact.Fields)
	}
}

// TestActualCompletionProjectRollup_StatementUsesTheDeclaredStatusLiterals
// pins the SQL TEXT itself, not just the fake client's canned response --
// a fake client returns its canned rows regardless of what the WHERE
// clause says, so only a statement-content assertion can catch a wrong
// literal (e.g. the wrong ClickHouse enum value) reaching production SQL.
func TestActualCompletionProjectRollup_StatementUsesTheDeclaredStatusLiterals(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 5, 0, 0, 5),
	}}}}
	readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	if len(client.queries) != 1 {
		t.Fatalf("query count = %d, want 1", len(client.queries))
	}
	statement := client.queries[0].statement
	if !strings.Contains(statement, "w.status = 'canceled'") {
		t.Fatalf("statement = %q, want the cancelled-status literal 'canceled'", statement)
	}
	if !strings.Contains(statement, "w.status = 'unknown'") {
		t.Fatalf("statement = %q, want the unknown-status literal 'unknown'", statement)
	}
}

func TestActualCompletionProjectRollup_RollupBasisFields(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 5, 0, 0, 5),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	fact := result.Facts[0]
	if fact.Fields["rollup_basis"].String == nil || *fact.Fields["rollup_basis"].String != "project_work_item_completion" {
		t.Fatalf("rollup_basis = %#v, want %q", fact.Fields["rollup_basis"], "project_work_item_completion")
	}
	if fact.Fields["member_kind"].String == nil || *fact.Fields["member_kind"].String != "work_item" {
		t.Fatalf("member_kind = %#v, want %q", fact.Fields["member_kind"], "work_item")
	}
}

func TestActualCompletionProjectRollup_UnrequestedProjectRowNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-OTHER", 5, 0, 0, 5),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- the returned row belongs to an unrequested project", result.Facts)
	}
}

func TestActualCompletionProjectRollup_WindowActiveUsesAsOfExpression(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 5, 0, 0, 3),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactActualCompletion)
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Kind: contextfabric.FactActualCompletion, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	statement := client.queries[0].statement
	if !strings.Contains(statement, "w.completed_at <=") {
		t.Fatalf("statement = %q, want the Tier-B as-of comparison for an active window", statement)
	}
	if !strings.Contains(statement, "w.created_at <=") {
		t.Fatalf("statement = %q, want the existence predicate for an active window", statement)
	}
}

// --- Grain honesty: each branch fires only for its own subject kind ---

func TestActualCompletionProjectOnlyQuery_NeverFiresWorkItemStatement(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{
		completionRollupRow("linear", "proj-1", 5, 0, 0, 5),
	}}}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	if len(client.queries) != 1 {
		t.Fatalf("query count = %d, want exactly 1 -- a project-only request must never also run the work-item statement", len(client.queries))
	}
}

func TestActualCompletionWorkItemOnlyQuery_NeverFiresProjectStatement(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: [][]any{
		{"WI-1", uint8(1), time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC), "repo-b"},
	}}}}
	result := readProjectCompletion(t, client, workItemSubject("repo-b", "WI-1"))
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	if len(client.queries) != 1 {
		t.Fatalf("query count = %d, want exactly 1 -- a work-item-only request must never also run the project aggregate", len(client.queries))
	}
	statement := client.queries[0].statement
	if strings.Contains(statement, completionRollupQueryMatch) {
		t.Fatalf("statement = %q, a work-item-only query ran the project aggregate", statement)
	}
	if fact := result.Facts[0]; fact.Fields["rollup_basis"].String != nil {
		t.Fatalf("fields = %#v, a work-item-grain fact must never carry the project roll-up basis", fact.Fields)
	}
}

func TestActualCompletionMixedQuery_BothStatementsFireIndependently(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: completionRollupQueryMatch, rows: [][]any{completionRollupRow("linear", "proj-1", 5, 0, 0, 5)}},
		{match: "FROM work_items", rows: [][]any{{"WI-1", uint8(1), time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC), "repo-b"}}},
	}}
	result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"), workItemSubject("repo-b", "WI-1"))
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2 (one work-item, one project)", result.Facts)
	}
	if len(client.queries) != 2 {
		t.Fatalf("query count = %d, want exactly 2 -- one per branch, never a cross-branch merge", len(client.queries))
	}
}

func assertInt(t *testing.T, fact contextfabric.CanonicalFact, field string, want int64) {
	t.Helper()
	got := fact.Fields[field].Integer
	if got == nil || *got != want {
		t.Fatalf("%s = %#v, want %d", field, fact.Fields[field], want)
	}
}

func assertNumber(t *testing.T, fact contextfabric.CanonicalFact, field string, want float64) {
	t.Helper()
	got := fact.Fields[field].Number
	if got == nil || *got != want {
		t.Fatalf("%s = %#v, want %v", field, fact.Fields[field], want)
	}
}
