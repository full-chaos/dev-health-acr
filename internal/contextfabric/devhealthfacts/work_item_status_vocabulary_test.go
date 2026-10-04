package devhealthfacts_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readStatusFacts(t *testing.T, statuses ...string) []contextfabric.CanonicalFact {
	t.Helper()
	var rows [][]any
	var subjects []contextfabric.SubjectRef
	for i, status := range statuses {
		id := "ITEM-" + string(rune('A'+i))
		rows = append(rows, []any{id, status, "repo-1", ""})
		subjects = append(subjects, workItemSubject("repo-1", id))
	}
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: rows}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactStatus, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != len(statuses) {
		t.Fatalf("facts = %d, want %d", len(result.Facts), len(statuses))
	}
	return result.Facts
}

func TestWorkItemStatusVocabularyIsTheClosedSetOfEight(t *testing.T) {
	t.Parallel()
	want := []string{"backlog", "todo", "in_progress", "in_review", "blocked", "done", "canceled", "unknown"}
	if got := devhealthfacts.WorkItemStatusVocabulary(); !reflect.DeepEqual(got, want) {
		t.Fatalf("vocabulary = %v, want %v", got, want)
	}
	got := devhealthfacts.WorkItemStatusVocabulary()
	got[0] = "mutated"
	if devhealthfacts.WorkItemStatusVocabulary()[0] != "backlog" {
		t.Fatalf("WorkItemStatusVocabulary returned shared storage")
	}
}

func TestStatusFactDisclosesNormalizedBasisAndLabelDerivedProviders(t *testing.T) {
	t.Parallel()
	for _, status := range devhealthfacts.WorkItemStatusVocabulary() {
		fact := readStatusFacts(t, status)[0]
		if v := fact.Fields["status"]; v.String == nil || *v.String != status {
			t.Fatalf("status %q: field = %+v", status, v)
		}
		if v := fact.Fields["status_basis"]; v.String == nil || *v.String != "dev_health_normalized" {
			t.Fatalf("status %q: status_basis = %+v", status, v)
		}
		if v := fact.Fields["status_in_vocabulary"]; v.Boolean == nil || !*v.Boolean {
			t.Fatalf("status %q: status_in_vocabulary = %+v, want true", status, v)
		}
		note := fact.Fields["status_provenance"].String
		if note == nil {
			t.Fatalf("status %q: no status_provenance", status)
		}
		for _, must := range []string{"normalized vocabulary", "missing status is null", "served as read", "status_in_vocabulary says which", "varies by provider", "jira from", "github and gitlab from issue labels", "not a provider fact", "linear from", "provider of this item is not carried"} {
			if !strings.Contains(*note, must) {
				t.Fatalf("status_provenance lacks %q: %s", must, *note)
			}
		}
	}
}

func TestStatusFactFlagsAValueOutsideTheClosedSetWithoutMappingIt(t *testing.T) {
	t.Parallel()
	fact := readStatusFacts(t, "In Review")[0]
	if v := fact.Fields["status"]; v.String == nil || *v.String != "In Review" {
		t.Fatalf("status = %+v, want the value served as read", v)
	}
	if v := fact.Fields["status_in_vocabulary"]; v.Boolean == nil || *v.Boolean {
		t.Fatalf("status_in_vocabulary = %+v, want false", v)
	}
}

func TestStatusFactMissingStatusIsNullNotInVocabularyFalse(t *testing.T) {
	t.Parallel()
	fact := readStatusFacts(t, "")[0]
	if !fact.Fields["status"].Null || !fact.Fields["status_in_vocabulary"].Null {
		t.Fatalf("fields = %+v, want status and status_in_vocabulary null", fact.Fields)
	}
}

// The sentence is a statement about the vocabulary. It must not claim a
// provider as THIS item's source: every provider name appears only inside the
// "varies by provider" enumeration, never as "this item is ..." or "this
// <provider> item".
func TestStatusProvenanceNamesNoSingleProviderAsThisItemsSource(t *testing.T) {
	t.Parallel()
	note := *readStatusFacts(t, "done")[0].Fields["status_provenance"].String
	lower := strings.ToLower(note)
	for _, provider := range []string{"jira", "github", "gitlab", "linear"} {
		for _, claim := range []string{"this " + provider, "this item is " + provider, "this item came from " + provider, provider + " item"} {
			if strings.Contains(lower, claim) {
				t.Fatalf("status_provenance claims %q as this item's source: %s", claim, note)
			}
		}
	}
	if strings.Contains(lower, "label-derived") || strings.Contains(lower, "derived from") {
		t.Fatalf("status_provenance uses an unconditional derivation claim: %s", note)
	}
	if !strings.Contains(lower, "provider of this item is not carried") {
		t.Fatalf("status_provenance does not say the item's provider is not carried: %s", note)
	}
	cut := strings.Index(lower, "varies by provider")
	before, basis := lower[:cut], lower[cut:]
	for _, provider := range []string{"jira", "github", "gitlab", "linear"} {
		if strings.Contains(before, provider) {
			t.Fatalf("%s is named outside the per-provider basis: %s", provider, note)
		}
		if !strings.Contains(basis, provider) {
			t.Fatalf("the per-provider basis omits %s: %s", provider, note)
		}
	}
	// Every provider name sits inside the enumeration, and a phrasing that
	// attributes this item to one is refused wherever it appears.
	for _, attribution := range []string{" comes from a ", " came from a ", " is a github", " is a gitlab", " is a jira", " is a linear", "this fact comes", "this item comes"} {
		if strings.Contains(lower, attribution) {
			t.Fatalf("status_provenance attributes the item with %q: %s", attribution, note)
		}
	}
}
