package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var statusSinceNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func readStatusSince(t *testing.T, statusRows, transitionRows [][]any, subjects ...contextfabric.SubjectRef) (contextfabric.FactProviderResult, *fakeClient, error) {
	t.Helper()
	restore := devhealthfacts.SetClockForTest(func() time.Time { return statusSinceNow })
	t.Cleanup(restore)
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_items", rows: statusRows},
		{match: "FROM work_item_transitions", rows: transitionRows},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactStatus, Subjects: subjects,
	})
	return result, client, err
}

func statusSinceFields(t *testing.T, result contextfabric.FactProviderResult, i int) (since *string, secs *int64, basis string, nullSince, nullSecs bool) {
	t.Helper()
	if len(result.Facts) <= i {
		t.Fatalf("facts = %d, want > %d", len(result.Facts), i)
	}
	f := result.Facts[i].Fields
	b := f["status_since_basis"].String
	if b == nil {
		t.Fatalf("status_since_basis missing: %#v", f)
	}
	return f["status_since"].String, f["status_duration_seconds"].Integer, *b, f["status_since"].Null, f["status_duration_seconds"].Null
}

func TestStatusSinceObservedIsDurationFromTheLatestEntry(t *testing.T) {
	entered := statusSinceNow.Add(-90 * time.Minute)
	result, _, err := readStatusSince(t,
		[][]any{{"W-1", "in_progress", "repo-1", "jira"}},
		[][]any{{"repo-1", "W-1", "in_progress", entered}},
		workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	since, secs, basis, _, _ := statusSinceFields(t, result, 0)
	if basis != "transition_observed" || since == nil || *since != "2026-10-11T10:30:00Z" || secs == nil || *secs != 5400 {
		t.Fatalf("since=%v secs=%v basis=%q", since, secs, basis)
	}
}

func TestStatusSinceNoTransitionIsNullNotZeroNorCreation(t *testing.T) {
	result, _, err := readStatusSince(t,
		[][]any{{"W-1", "todo", "repo-1", "jira"}}, nil,
		workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	since, secs, basis, nullSince, nullSecs := statusSinceFields(t, result, 0)
	if basis != "no_transition_recorded" || since != nil || secs != nil || !nullSince || !nullSecs {
		t.Fatalf("since=%v secs=%v basis=%q nulls=%v/%v", since, secs, basis, nullSince, nullSecs)
	}
}

func TestStatusSinceLatestTransitionWentElsewhereIsNotAnOlderMatch(t *testing.T) {
	result, _, err := readStatusSince(t,
		[][]any{{"W-1", "in_progress", "repo-1", "jira"}},
		[][]any{{"repo-1", "W-1", "done", statusSinceNow.Add(-time.Hour)}},
		workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, secs, basis, _, _ := statusSinceFields(t, result, 0); basis != "no_transition_recorded" || secs != nil {
		t.Fatalf("secs=%v basis=%q", secs, basis)
	}
}

func TestStatusSinceEntryInTheFutureIsNotADuration(t *testing.T) {
	result, _, err := readStatusSince(t,
		[][]any{{"W-1", "in_progress", "repo-1", "jira"}},
		[][]any{{"repo-1", "W-1", "in_progress", statusSinceNow.Add(time.Minute)}},
		workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, secs, basis, _, _ := statusSinceFields(t, result, 0); basis != "no_transition_recorded" || secs != nil {
		t.Fatalf("secs=%v basis=%q", secs, basis)
	}
}

func TestStatusSinceEntryAtTheClockIsZeroSecondsObserved(t *testing.T) {
	result, _, err := readStatusSince(t,
		[][]any{{"W-1", "in_progress", "repo-1", "jira"}},
		[][]any{{"repo-1", "W-1", "in_progress", statusSinceNow}},
		workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, secs, basis, _, _ := statusSinceFields(t, result, 0); basis != "transition_observed" || secs == nil || *secs != 0 {
		t.Fatalf("secs=%v basis=%q", secs, basis)
	}
}

func TestStatusSinceMissingStatusHasNoEntry(t *testing.T) {
	result, _, err := readStatusSince(t,
		[][]any{{"W-1", "", "repo-1", "jira"}},
		[][]any{{"repo-1", "W-1", "", statusSinceNow.Add(-time.Hour)}},
		workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, secs, basis, _, _ := statusSinceFields(t, result, 0); basis != "no_transition_recorded" || secs != nil {
		t.Fatalf("secs=%v basis=%q", secs, basis)
	}
}

func TestStatusSinceIsKeyedByRepositoryAndItem(t *testing.T) {
	entered := statusSinceNow.Add(-time.Hour)
	result, _, err := readStatusSince(t,
		[][]any{{"W-1", "in_progress", "repo-1", "jira"}},
		[][]any{{"repo-2", "W-1", "in_progress", entered}},
		workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, secs, basis, _, _ := statusSinceFields(t, result, 0); basis != "no_transition_recorded" || secs != nil {
		t.Fatalf("a transition of the same id in another repository was used: secs=%v basis=%q", secs, basis)
	}
}

func TestStatusSinceStatementIsOrgScopedAndKeyedToTheReadRows(t *testing.T) {
	_, client, err := readStatusSince(t,
		[][]any{{"W-1", "in_progress", "repo-1", "jira"}},
		nil,
		workItemSubject("repo-1", "W-1"), workItemSubject("repo-1", "W-2"))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, q := range client.queries {
		if !strings.Contains(q.statement, "FROM work_item_transitions") {
			continue
		}
		found = true
		if !strings.Contains(q.statement, "t.org_id = {org_id:String}") || !strings.Contains(q.statement, "FINAL") {
			t.Fatalf("statement is not org-scoped/FINAL:\n%s", q.statement)
		}
		for _, b := range q.bindings {
			switch b.Name {
			case "org_id":
				if b.Value != "org-1" {
					t.Fatalf("org_id = %v", b.Value)
				}
			case "keys":
				keys, _ := b.Value.([]string)
				if len(keys) != 1 || keys[0] != "repo-1:W-1" {
					t.Fatalf("keys = %#v, want only the item the scoped status read returned", keys)
				}
			}
		}
	}
	if !found {
		t.Fatal("no work_item_transitions statement ran")
	}
}

func TestStatusSinceNothingReadMeansNoTransitionProbe(t *testing.T) {
	_, client, err := readStatusSince(t, nil, nil, workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range client.queries {
		if strings.Contains(q.statement, "work_item_transitions") {
			t.Fatalf("an item the scoped read did not return was probed:\n%s", q.statement)
		}
	}
}

func TestStatusSinceReadFailureFailsTheFactRead(t *testing.T) {
	restore := devhealthfacts.SetClockForTest(func() time.Time { return statusSinceNow })
	t.Cleanup(restore)
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{{"W-1", "in_progress", "repo-1", "jira"}}},
		{match: "FROM work_item_transitions", err: errors.New("boom")},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactStatus,
		Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "W-1")},
	})
	if err == nil {
		t.Fatalf("a failed transitions read produced facts: %#v", result.Facts)
	}
}
