package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readStatusFactsByProvider(t *testing.T, providers map[string]string) (map[string]contextfabric.CanonicalFact, *fakeClient, error) {
	t.Helper()
	var statusRows [][]any
	var subjects []contextfabric.SubjectRef
	for _, id := range []string{"ITEM-A", "ITEM-B"} {
		statusRows = append(statusRows, []any{id, "in_progress", "repo-1", providers[id]})
		subjects = append(subjects, workItemSubject("repo-1", id))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_items", rows: statusRows},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	result, readErr := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactStatus, Subjects: subjects,
	})
	out := map[string]contextfabric.CanonicalFact{}
	for _, fact := range result.Facts {
		out[fact.Subject.CanonicalID] = fact
	}
	return out, client, readErr
}

func TestStatusBasisIsPerItemFromItsProvider(t *testing.T) {
	t.Parallel()
	facts, _, err := readStatusFactsByProvider(t, map[string]string{"ITEM-A": "jira", "ITEM-B": "github"})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(facts))
	}
	want := map[string][2]string{
		"ITEM-A": {"status_mapping_configuration", "The provider of this item is jira"},
		"ITEM-B": {"issue_labels_and_state", "The provider of this item is github"},
	}
	for _, fact := range facts {
		var key string
		for id := range want {
			if strings.HasSuffix(fact.Subject.CanonicalID, id) || strings.Contains(fact.Subject.CanonicalID, id) {
				key = id
			}
		}
		if key == "" {
			t.Fatalf("unmatched subject %q", fact.Subject.CanonicalID)
		}
		basis, note := fact.Fields["status_basis"].String, fact.Fields["status_provenance"].String
		if basis == nil || *basis != want[key][0] {
			t.Errorf("%s: status_basis = %v, want %q", key, basis, want[key][0])
		}
		if note == nil || !strings.Contains(*note, want[key][1]) || strings.Contains(*note, "is not carried by this read") {
			t.Errorf("%s: status_provenance = %v, want it to name %q", key, note, want[key][1])
		}
	}
}

func TestStatusBasisForEveryProviderAndAnUnknownOne(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"jira": "status_mapping_configuration", "github": "issue_labels_and_state", "gitlab": "issue_labels_and_state", "linear": "workflow_state_type",
		"GitHub": "issue_labels_and_state", "": "dev_health_normalized", "bitbucket": "dev_health_normalized",
	}
	for provider, want := range cases {
		facts, _, err := readStatusFactsByProvider(t, map[string]string{"ITEM-A": provider, "ITEM-B": provider})
		if err != nil {
			t.Fatal(err)
		}
		for _, fact := range facts {
			if got := fact.Fields["status_basis"].String; got == nil || *got != want {
				t.Errorf("provider %q: status_basis = %v, want %q", provider, got, want)
			}
		}
	}
}

func TestStatusProviderComesFromTheSingleStatusRead(t *testing.T) {
	t.Parallel()
	_, client, err := readStatusFactsByProvider(t, map[string]string{"ITEM-A": "jira", "ITEM-B": "jira"})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.queries) != 1 {
		t.Fatalf("queries = %d, want the status read alone", len(client.queries))
	}
	if !strings.Contains(client.queries[0].statement, "ifNull(w.provider, '')") {
		t.Fatalf("the status read does not select the provider: %s", client.queries[0].statement)
	}
}
