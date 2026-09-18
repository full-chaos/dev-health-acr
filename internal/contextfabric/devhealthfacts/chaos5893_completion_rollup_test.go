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

// TestActualCompletionProjectRollup_WindowActiveRefusesRatherThanMisreportHistory
// pins that the roll-up's cancelled/unknown exclusion reads CURRENT
// w.status, a column with no recorded history, so
// an as-of query cannot honestly compute "the roll-up as of T" -- it would
// silently substitute today's status for the requested instant's. The
// project branch refuses the whole non-current axis rather than serve that,
// and the refusal is DISCLOSED (never a bare empty read): no query fires at
// all, and the caller sees the closed-vocabulary reason.
func TestActualCompletionProjectRollup_WindowActiveRefusesRatherThanMisreportHistory(t *testing.T) {
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
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want none -- a historical project read must never serve a status-basis it cannot honestly claim", result.Facts)
	}
	if len(client.queries) != 0 {
		t.Fatalf("query count = %d, want 0 -- the project branch must not run its query on a historical axis at all", len(client.queries))
	}
	if !result.Truncated {
		t.Fatal("Truncated = false, want true -- the refused project subject is a disclosed omission, not a silent empty read")
	}
	if !strings.Contains(result.Reason, "project_completion_current_status_only") {
		t.Fatalf("reason = %q, want it to name the historical-status limitation", result.Reason)
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

// --- No branch starves another on the shared budget ---

// countFactsByKind counts the facts whose Subject is of kind.
func countFactsByKind(facts []contextfabric.CanonicalFact, kind contextfabric.SubjectKind) int {
	n := 0
	for _, fact := range facts {
		if fact.Subject.Kind == kind {
			n++
		}
	}
	return n
}

// projectRowsAndSubjects builds n distinct project rows/subjects for the
// aggregate statement, one project each with 1 counted, 1 completed work
// item (a minimal, always-servable shape -- the numerator/denominator
// arithmetic itself is covered elsewhere).
func projectRowsAndSubjects(n int) ([][]any, []contextfabric.SubjectRef) {
	rows := make([][]any, n)
	subjects := make([]contextfabric.SubjectRef, n)
	for i := 0; i < n; i++ {
		id := "PROJ-" + strconvItoa(i)
		rows[i] = completionRollupRow("linear", id, 1, 0, 0, 1)
		subjects[i] = projectSubject("linear", id)
	}
	return rows, subjects
}

func strconvItoa(i int) string {
	// Local, dependency-free itoa: this file already imports no "strconv",
	// and pulling it in for one call site is not worth a new import line.
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// TestActualCompletionSharedBudgetNoBranchStarvesTheOther pins that the
// work-item and project branches, sharing ONE 200-row factBudget, never
// let one branch's own admissions come at the other's expense: the
// project branch runs first, and a shortfall on either branch names the
// kind it fell on rather than folding into an undifferentiated Truncated
// flag.
//
// This executes the full {0, 1, budget-1, budget, budget+1} work-item x
// {0, 1, many} project cross, in BOTH caller-supplied subject orders, and
// asserts: the project branch (bounded by its own small requested count)
// is NEVER starved regardless of order or work-item volume; the work-item
// branch is served up to whatever the shared budget has left AFTER the
// project branch's own admissions; and a drop is disclosed by KIND, never
// folded into an undifferentiated Truncated flag alone.
func TestActualCompletionSharedBudgetNoBranchStarvesTheOther(t *testing.T) {
	const budgetCap = 200 // must match devhealthfacts' own maxFactRowsPerQuery

	workItemCells := []int{0, 1, budgetCap - 1, budgetCap, budgetCap + 1}
	projectCells := []int{0, 1, 5} // "many" -- well under the cap by design

	for _, wi := range workItemCells {
		for _, pj := range projectCells {
			if wi == 0 && pj == 0 {
				continue
			}
			for _, workItemFirst := range []bool{true, false} {
				wi, pj, workItemFirst := wi, pj, workItemFirst
				order := "project_first"
				if workItemFirst {
					order = "work_item_first"
				}
				t.Run(fmtSharedBudgetCase(wi, pj, order), func(t *testing.T) {
					workItemSubjs := workItemSubjects(wi)
					workItemRows := make([][]any, wi)
					for i := 0; i < wi; i++ {
						workItemRows[i] = []any{workItemSubjs[i].Label, uint8(1), time.Unix(0, 0).UTC(), "repo-1"}
					}
					projectRows, projectSubjs := projectRowsAndSubjects(pj)

					var tables []fakeTable
					if pj > 0 {
						tables = append(tables, fakeTable{match: completionRollupQueryMatch, rows: projectRows})
					}
					if wi > 0 {
						tables = append(tables, fakeTable{match: "FROM work_items", rows: workItemRows})
					}
					client := &fakeClient{tables: tables}

					var subjects []contextfabric.SubjectRef
					if workItemFirst {
						subjects = append(append(subjects, workItemSubjs...), projectSubjs...)
					} else {
						subjects = append(append(subjects, projectSubjs...), workItemSubjs...)
					}

					result := readProjectCompletion(t, client, subjects...)

					if got := countFactsByKind(result.Facts, contextfabric.SubjectProject); got != pj {
						t.Fatalf("project facts served = %d, want %d (every requested project) -- the project branch must never be starved by the work-item branch, regardless of caller order", got, pj)
					}

					remaining := budgetCap - pj
					wantWorkItemFacts := wi
					if wantWorkItemFacts > remaining {
						wantWorkItemFacts = remaining
					}
					if got := countFactsByKind(result.Facts, contextfabric.SubjectWorkItem); got != wantWorkItemFacts {
						t.Fatalf("work-item facts served = %d, want %d", got, wantWorkItemFacts)
					}

					if wi > remaining {
						if !result.Truncated {
							t.Fatal("Truncated = false, want true -- more work items were requested than the shared budget had left after the project branch's own admissions")
						}
						if !strings.Contains(result.Reason, "actual_completion_shared_budget_dropped:work_item") {
							t.Fatalf("reason = %q, want it to name work_item as the kind the shared budget shorted", result.Reason)
						}
					}
				})
			}
		}
	}
}

func fmtSharedBudgetCase(wi, pj int, order string) string {
	return "wi=" + strconvItoa(wi) + "/pj=" + strconvItoa(pj) + "/" + order
}

// --- Class A: a served row must satisfy its own population partition ---

// TestActualCompletionProjectRollup_PartitionInvariantViolation_NoFactServed
// forces a row that could never come out of the shipped statement (every
// count there is a plain countIf over ONE GROUP BY, so the arithmetic holds
// by construction) but that a canned fixture can still hand the scan --
// exactly the shape a future edit to the statement could introduce by
// accident. Each case isolates ONE of the two guards: the first fails
// closed before countedWorkItems is even computed (cancelled/unknown
// exceeding the total), the second after (completed/unknown exceeding the
// counted subset) -- a served fact must never come out of either.
func TestActualCompletionProjectRollup_PartitionInvariantViolation_NoFactServed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		row  []any
	}{
		// cancelled_count (5) exceeds work_item_count (3): impossible from
		// a real countIf, caught before countedWorkItems is computed.
		{"cancelled_exceeds_total", completionRollupRow("linear", "proj-1", 3, 5, 0, 0)},
		// completed_count (10) exceeds counted_work_items (5-1=4): caught
		// after countedWorkItems is computed, with cancelled_count itself
		// well-formed.
		{"completed_exceeds_counted", completionRollupRow("linear", "proj-1", 5, 1, 0, 10)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{{match: completionRollupQueryMatch, rows: [][]any{tc.row}}}}
			result := readProjectCompletion(t, client, projectSubject("linear", "proj-1"))
			if len(result.Facts) != 0 {
				t.Fatalf("facts = %#v, want none -- a row that violates its own partition invariant must never be served", result.Facts)
			}
			if !result.Truncated {
				t.Fatal("Truncated = false, want true -- the withheld row is a disclosed omission, not a silent drop")
			}
			if !strings.Contains(result.Reason, "project_completion_partition_invalid") {
				t.Fatalf("reason = %q, want it to name the partition-invariant violation", result.Reason)
			}
		})
	}
}
