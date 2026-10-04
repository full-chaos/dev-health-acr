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

// The status fact read through the production reader against real ClickHouse:
// one work item per normalized value, spread over the four providers, plus a
// value outside the closed set and an empty one.
func TestStatusFactVocabularyAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	repoID := "5b1c7e0e-2f56-4a53-9d0a-7a1f0c9d0001"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoID, orgID, "vocab/repo", "github", at); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	type seeded struct{ id, provider, status string }
	items := []seeded{
		{"v-1", "jira", "backlog"}, {"v-2", "github", "todo"}, {"v-3", "gitlab", "in_progress"},
		{"v-4", "linear", "in_review"}, {"v-5", "jira", "blocked"}, {"v-6", "github", "done"},
		{"v-7", "gitlab", "canceled"}, {"v-8", "linear", "unknown"},
		{"v-9", "jira", "In Progress"}, {"v-10", "github", ""},
	}
	var subjects []contextfabric.SubjectRef
	for _, item := range items {
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.id, repoID, orgID, item.provider, "t", item.status, at, at, at); err != nil {
			t.Fatalf("seed %s: %v", item.id, err)
		}
		subjects = append(subjects, workItemSubject(repoID, item.id))
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactStatus)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactStatus, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != len(items) {
		t.Fatalf("facts = %d, want %d (state %s)", len(result.Facts), len(items), result.State)
	}
	byLabel := map[string]contextfabric.CanonicalFact{}
	for _, fact := range result.Facts {
		byLabel[fact.Subject.Label] = fact
	}
	for _, item := range items {
		fact, ok := byLabel[item.id]
		if !ok {
			t.Fatalf("no fact for %s", item.id)
		}
		inSet := fact.Fields["status_in_vocabulary"]
		switch item.status {
		case "":
			if !fact.Fields["status"].Null || !inSet.Null {
				t.Fatalf("%s: fields = %+v, want null status and null flag", item.id, fact.Fields)
			}
		case "In Progress":
			if got := fact.Fields["status"].String; got == nil || *got != item.status || inSet.Boolean == nil || *inSet.Boolean {
				t.Fatalf("%s: fields = %+v, want the value as read, flagged outside the set", item.id, fact.Fields)
			}
		default:
			if got := fact.Fields["status"].String; got == nil || *got != item.status || inSet.Boolean == nil || !*inSet.Boolean {
				t.Fatalf("%s (%s): fields = %+v, want %q inside the set", item.id, item.provider, fact.Fields, item.status)
			}
		}
		wantBasis := map[string]string{"jira": "status_mapping_configuration", "github": "issue_labels_and_state", "gitlab": "issue_labels_and_state", "linear": "workflow_state_type"}[item.provider]
		if b := fact.Fields["status_basis"].String; b == nil || *b != wantBasis {
			t.Fatalf("%s (%s): status_basis = %+v, want %q", item.id, item.provider, fact.Fields["status_basis"], wantBasis)
		}
		if n := fact.Fields["status_provenance"].String; n == nil || !strings.Contains(*n, "The provider of this item is "+item.provider+":") {
			t.Fatalf("%s: status_provenance does not name provider %s: %v", item.id, item.provider, n)
		}
	}
}
