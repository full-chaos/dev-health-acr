package devhealthfacts_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestBlockersProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_dependencies", rows: [][]any{{"WIDGET-099", "WIDGET-101", "repo-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Subject.CanonicalID != "work_item.v2:repo-1:WIDGET-101" {
		t.Fatalf("fact subject = %+v", fact.Subject)
	}
	if fact.Fields["blocked_by_work_item_id"].String == nil || *fact.Fields["blocked_by_work_item_id"].String != "WIDGET-099" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

func TestBlockersProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestBlockersProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestBlockersProviderOrgScoped(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-7"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-7" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "repo-1:WIDGET-101" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
}

func TestRequiredChildrenProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_dependencies", rows: [][]any{{"WIDGET-101", "WIDGET-200", "related_to", "repo-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactRequiredChildren)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactRequiredChildren, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["required_child_work_item_id"].String == nil || *fact.Fields["required_child_work_item_id"].String != "WIDGET-200" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["relationship_type"].String == nil || *fact.Fields["relationship_type"].String != "related_to" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

func TestRequiredChildrenProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactRequiredChildren)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactRequiredChildren, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestRequiredChildrenProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactRequiredChildren)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactRequiredChildren, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

// maxFactRowsPerQueryForTest mirrors shared.go's unexported
// maxFactRowsPerQuery -- it can't be imported (this file is in
// devhealthfacts_test, a separate package), so the H7 tests below assert
// against this local copy of the same value.
const maxFactRowsPerQueryForTest = 200

// blockerRows builds n synthetic work_item_dependencies rows all blocking
// the same target work item, standing in for a subject with a pathological
// number of matching rows (H7).
func blockerRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = []any{"blocker-" + strconv.Itoa(i), "WIDGET-101", "repo-1"}
	}
	return rows
}

// TestBlockersProviderTruncatesWhenTheProbeRowIsPresent is the RE-AIMED form
// of this file's original truncation pin (CHAOS-5438).
//
// It used to seed EXACTLY maxFactRowsPerQueryForTest rows and require
// Truncated -- which pinned the DEFECT, not the contract: under a statement
// bounded at that same number, a full page and a truncated one are
// indistinguishable, so "the row count reached the limit" was never evidence
// that anything was left behind. The provider now PROBES one row further
// (withRowProbeLimit), so the honest signal is the presence of the
// (limit+1)-th row, and that is what this asserts. Its twin immediately
// below pins the other half: an exactly-full page is COMPLETE.
//
// Not deleted, because the count it pins is a signal rather than a number to
// bump -- see TestBlockersProviderNotTruncatedOnAnExactlyFullPage for the
// discriminating control that the guard still fires.
func TestBlockersProviderTruncatesWhenTheProbeRowIsPresent(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_dependencies", rows: blockerRows(maxFactRowsPerQueryForTest + 1)},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	// The output bound is SEPARATE from the probe bound: the extra row is
	// evidence, and must never be served.
	if len(result.Facts) != maxFactRowsPerQueryForTest {
		t.Fatalf("len(result.Facts) = %d, want exactly %d -- the probe row is evidence, never content", len(result.Facts), maxFactRowsPerQueryForTest)
	}
	if !result.Truncated {
		t.Fatalf("result.Truncated = false, want true when a %dth row exists", maxFactRowsPerQueryForTest+1)
	}
	if len(client.queries) == 0 || !strings.Contains(client.queries[len(client.queries)-1].statement, "LIMIT "+strconv.Itoa(maxFactRowsPerQueryForTest+1)) {
		t.Fatalf("query statement = %#v, want a LIMIT %d clause", client.queries, maxFactRowsPerQueryForTest+1)
	}
}

// TestBlockersProviderNotTruncatedOnAnExactlyFullPage is the half the old
// pin got backwards, and the discriminating control for the one above: a
// population of EXACTLY the output bound is complete, every row of it is
// served, and degrading the answer would assert a shortfall that did not
// happen.
func TestBlockersProviderNotTruncatedOnAnExactlyFullPage(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_dependencies", rows: blockerRows(maxFactRowsPerQueryForTest)},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if result.Truncated {
		t.Fatalf("result.Truncated = true on an exactly-full page of %d rows -- a complete population read as a shortfall", maxFactRowsPerQueryForTest)
	}
	if len(result.Facts) != maxFactRowsPerQueryForTest {
		t.Fatalf("len(result.Facts) = %d, want %d -- every row of a complete page is servable", len(result.Facts), maxFactRowsPerQueryForTest)
	}
}

func TestBlockersProviderNotTruncatedBelowLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_dependencies", rows: blockerRows(maxFactRowsPerQueryForTest - 1)},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != maxFactRowsPerQueryForTest-1 {
		t.Fatalf("len(result.Facts) = %d, want %d", len(result.Facts), maxFactRowsPerQueryForTest-1)
	}
	if result.Truncated {
		t.Fatalf("result.Truncated = true, want false when the row count is below the limit")
	}
}
