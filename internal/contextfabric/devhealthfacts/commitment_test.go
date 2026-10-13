package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	commitmentProjectsMatch = "FROM projects AS p FINAL"
	commitmentItemsMatch    = "WITH project_items AS"
)

var commitmentAsOf = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func commitmentProjectRow(provider, id, state, stateSource, targetDate string, reachable bool) []any {
	flag := uint8(0)
	if reachable {
		flag = 1
	}
	return []any{provider + ":" + id, provider, state, stateSource, targetDate, flag}
}

type commitmentCounts struct {
	total, open, closed, withDueAt, pastDueOpen, sprints, nextSprints int64
	nextEnd, latestEnd                                                time.Time
}

func commitmentItemRow(provider, id string, c commitmentCounts) []any {
	return []any{provider + ":" + id, c.total, c.open, c.closed, c.withDueAt, c.pastDueOpen, c.sprints, c.nextSprints, c.nextEnd, c.latestEnd}
}

func commitmentClient(projects, items [][]any) *fakeClient {
	return &fakeClient{tables: []fakeTable{
		{match: commitmentProjectsMatch, rows: projects},
		{match: commitmentItemsMatch, rows: items},
	}}
}

func readCommitmentFacts(t *testing.T, client *fakeClient, subjects ...contextfabric.SubjectRef) directread.FactsResponse {
	t.Helper()
	return readCommitmentFactsAs(t, client, storage.Principal{OrgID: "org-commitment", Subject: "commitment", CredentialID: "commitment"}, subjects...)
}

func readCommitmentFactsAs(t *testing.T, client contextpacket.ClickHouseQueryClient, principal storage.Principal, subjects ...contextfabric.SubjectRef) directread.FactsResponse {
	t.Helper()
	restore := devhealthfacts.SetClockForTest(func() time.Time { return commitmentAsOf })
	t.Cleanup(restore)
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(&seamCapture{registry: registry.WithoutScopeExpansion()}), nil)
	request := directread.FactsRequest{Kinds: []string{"commitment"}}
	for _, subject := range subjects {
		request.Subjects = append(request.Subjects, directread.RequestSubject{Kind: "project", CanonicalID: subject.CanonicalID})
	}
	response, err := reader.Read(observability.WithRequestID(context.Background(), "req_00000000000000000000000000000c01"), principal, request)
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	return response
}

func servedCommitment(t *testing.T, response directread.FactsResponse, subject contextfabric.SubjectRef) directread.ServedFact {
	t.Helper()
	for _, fact := range response.Facts {
		if fact.Kind == "commitment" && fact.Subject.CanonicalID == subject.CanonicalID {
			return fact
		}
	}
	raw, _ := json.Marshal(response)
	t.Fatalf("no served commitment fact for %s: %s", subject.CanonicalID, raw)
	return directread.ServedFact{}
}

func wantServed(t *testing.T, fact directread.ServedFact, want map[string]any) {
	t.Helper()
	for name, value := range want {
		got, present := fact.Fields[name]
		if !present {
			t.Errorf("%s: field %q is absent, want %#v", fact.Subject.CanonicalID, name, value)
			continue
		}
		if got != value {
			t.Errorf("%s: field %q = %#v, want %#v", fact.Subject.CanonicalID, name, got, value)
		}
	}
}

func wantAbsent(t *testing.T, fact directread.ServedFact, names ...string) {
	t.Helper()
	for _, name := range names {
		if value, present := fact.Fields[name]; present {
			t.Errorf("%s: field %q = %#v, want it absent", fact.Subject.CanonicalID, name, value)
		}
	}
}

func TestCommitmentServesTheStatedDatesOfAProjectAtTheReadFactsBoundary(t *testing.T) {
	project := projectSubject("linear", "proj-l")
	client := commitmentClient(
		[][]any{commitmentProjectRow("linear", "proj-l", "started", "linear.project.state", "2026-11-01", true)},
		[][]any{commitmentItemRow("linear", "proj-l", commitmentCounts{
			total: 5, open: 3, closed: 2, withDueAt: 3, pastDueOpen: 1, sprints: 2, nextSprints: 1,
			nextEnd: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), latestEnd: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
		})},
	)
	fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
	wantServed(t, fact, map[string]any{
		"as_of":                "2026-10-13T12:00:00Z",
		"commitment_status":    "stated",
		"target_date":          "2026-11-01",
		"target_date_provider": "linear",
		"days_to_target":       "19",
		"project_state":        "started",
		"state_source":         "linear.project.state",
		"items_total":          "5",
		"items_open":           "3",
		"items_closed":         "2",
		"items_with_due_at":    "3",
		"items_without_due_at": "2",
		"items_past_due_open":  "1",
		"sprint_kind":          "cycle",
		"next_sprint_end":      "2026-10-20T00:00:00Z",
		"latest_sprint_end":    "2026-10-27T00:00:00Z",
	})
	wantAbsent(t, fact, "items_unavailable_reason", "at_risk", "score", "verdict")
	if fact.PopulationScope != "caller_authorized_items" {
		t.Errorf("population_scope = %q, want caller_authorized_items over the item counts", fact.PopulationScope)
	}
}

func TestCommitmentProjectWithNoStatedDateIsNoneStatedForEveryProvider(t *testing.T) {
	for _, provider := range []string{"jira", "gitlab", "github", "linear"} {
		t.Run(provider, func(t *testing.T) {
			project := projectSubject(provider, "proj-none")
			client := commitmentClient(
				[][]any{commitmentProjectRow(provider, "proj-none", "", "", "", true)},
				[][]any{commitmentItemRow(provider, "proj-none", commitmentCounts{total: 2, open: 1, closed: 1})},
			)
			fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
			wantServed(t, fact, map[string]any{
				"commitment_status":    "none_stated",
				"items_total":          "2",
				"items_with_due_at":    "0",
				"items_without_due_at": "2",
				"items_past_due_open":  "0",
				"project_state":        nil,
				"state_source":         nil,
			})
			wantAbsent(t, fact, "target_date", "target_date_provider", "days_to_target", "sprint_kind", "next_sprint_end", "latest_sprint_end", "items_unavailable_reason")
		})
	}
}

func TestCommitmentProjectWithNoIssuesIsNoneStatedWithMeasuredZeroCounts(t *testing.T) {
	project := projectSubject("github", "proj-empty")
	client := commitmentClient([][]any{commitmentProjectRow("github", "proj-empty", "", "", "", true)}, nil)
	fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
	wantServed(t, fact, map[string]any{"commitment_status": "none_stated", "items_total": "0", "items_open": "0", "items_closed": "0"})
}

func TestCommitmentSprintKindIsNamedByTheProviderAndNeverMerged(t *testing.T) {
	for provider, kind := range map[string]string{"jira": "sprint", "linear": "cycle", "gitlab": "milestone", "github": "milestone"} {
		t.Run(provider, func(t *testing.T) {
			project := projectSubject(provider, "proj-s")
			client := commitmentClient(
				[][]any{commitmentProjectRow(provider, "proj-s", "", "", "", true)},
				[][]any{commitmentItemRow(provider, "proj-s", commitmentCounts{
					total: 1, open: 1, sprints: 1, latestEnd: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
				})},
			)
			fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
			wantServed(t, fact, map[string]any{"sprint_kind": kind, "latest_sprint_end": "2026-09-30T00:00:00Z", "commitment_status": "stated"})
			wantAbsent(t, fact, "next_sprint_end", "target_date")
		})
	}
}

func TestCommitmentDueDatesAloneAreAStatedCommitment(t *testing.T) {
	project := projectSubject("jira", "proj-j")
	client := commitmentClient(
		[][]any{commitmentProjectRow("jira", "proj-j", "started", "jira.project.category", "", true)},
		[][]any{commitmentItemRow("jira", "proj-j", commitmentCounts{total: 4, open: 3, closed: 1, withDueAt: 2, pastDueOpen: 1})},
	)
	fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
	wantServed(t, fact, map[string]any{"commitment_status": "stated", "items_with_due_at": "2", "items_past_due_open": "1", "items_without_due_at": "2"})
	wantAbsent(t, fact, "target_date", "target_date_provider", "days_to_target", "sprint_kind")
}

func TestCommitmentUnreachableIssuesAreNamedNeverCountedAsZero(t *testing.T) {
	unreachable := projectSubject("linear", "proj-dup")
	targeted := projectSubject("linear", "proj-dup-target")
	client := commitmentClient([][]any{
		commitmentProjectRow("linear", "proj-dup", "", "", "", false),
		commitmentProjectRow("linear", "proj-dup-target", "", "", "2026-10-01", false),
	}, nil)
	response := readCommitmentFacts(t, client, unreachable, targeted)
	fact := servedCommitment(t, response, unreachable)
	wantServed(t, fact, map[string]any{"items_unavailable_reason": "project_key_ambiguous"})
	wantAbsent(t, fact, "commitment_status", "items_total", "items_open", "items_closed", "items_with_due_at", "items_without_due_at", "items_past_due_open")
	fact = servedCommitment(t, response, targeted)
	wantServed(t, fact, map[string]any{"commitment_status": "stated", "days_to_target": "-12", "items_unavailable_reason": "project_key_ambiguous"})
	wantAbsent(t, fact, "items_total")
}

func TestCommitmentRefusesAHistoricalAxis(t *testing.T) {
	provider := findProvider(t, devhealthfacts.NewProviders(commitmentClient(nil, nil)), contextfabric.FactCommitment)
	asOf := commitmentAsOf.AddDate(0, -1, 0)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-commitment"}, contextfabric.FactQuery{
		Kind: contextfabric.FactCommitment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-l")},
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if result.State != contextfabric.SourceNotApplicable || len(result.Facts) != 0 {
		t.Fatalf("historical read = %+v, want not_applicable and no facts: work item status keeps no history", result)
	}
}

func TestCommitmentStatementsCountOnlyOpenItemsPastDueOverTheProjectIssues(t *testing.T) {
	client := commitmentClient(
		[][]any{commitmentProjectRow("linear", "proj-l", "", "", "", true)},
		[][]any{commitmentItemRow("linear", "proj-l", commitmentCounts{total: 1, open: 1})},
	)
	readCommitmentFacts(t, client, projectSubject("linear", "proj-l"))
	var projects, items *capturedQuery
	for i := range client.queries {
		switch {
		case strings.Contains(client.queries[i].statement, commitmentItemsMatch):
			items = &client.queries[i]
		case strings.Contains(client.queries[i].statement, commitmentProjectsMatch):
			projects = &client.queries[i]
		}
	}
	if projects == nil || items == nil {
		t.Fatalf("statements = %d, want the project and the item statement", len(client.queries))
	}
	assertQueryScopedToOrgAndSubjects(t, projects.statement)
	assertQueryScopedToOrgAndSubjects(t, items.statement)
	for _, fragment := range []string{
		"FROM work_items AS w FINAL",
		"k.join_key = w.project_id",
		"ifNull(w.type, '') NOT IN ('pr', 'merge_request')",
		"argMax(tuple(lower(w.status), w.due_at, w.sprint_id), w.last_synced)",
		"toInt64(countIf(status IN ('done', 'canceled'))) AS items_closed",
		"toInt64(countIf(status NOT IN ('done', 'canceled') AND due_at IS NOT NULL AND due_at < {as_of:DateTime64(3, 'UTC')})) AS items_past_due_open",
		"FROM sprints FINAL",
		"s.provider = pi.provider AND s.sprint_id = pi.sprint_id",
	} {
		if !strings.Contains(items.statement, fragment) {
			t.Errorf("item statement lacks %q:\n%s", fragment, items.statement)
		}
	}
	if strings.Contains(items.statement, "w.repo_id =") || strings.Contains(items.statement, "project_key = w.project_key") {
		t.Errorf("item statement reaches issues by a repository column or a key prefix:\n%s", items.statement)
	}
	bound := false
	for _, binding := range items.bindings {
		if binding.Name == "as_of" {
			at, ok := binding.Value.(time.Time)
			bound = ok && at.Equal(commitmentAsOf)
		}
	}
	if !bound {
		t.Errorf("as_of binding = %+v, want the request clock %s", items.bindings, commitmentAsOf)
	}
}
