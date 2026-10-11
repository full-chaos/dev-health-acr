package devhealthfacts_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestIdentityProviderEmitsOwningTeamsFromCurrentRepoOwnership(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "team_repo_ownership", rows: [][]any{
			{"repo-1", "team-a", "Alpha"},
			{"repo-1", "team-b", "Beta"},
		}},
		{match: "FROM repos", rows: [][]any{
			{"repo-1", "acme/one", "github"},
			{"repo-2", "acme/two", "github"},
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactIdentity,
		Subjects: []contextfabric.SubjectRef{repoSubject("repo-1"), repoSubject("repo-2")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	byID := map[string]contextfabric.CanonicalFact{}
	for _, fact := range result.Facts {
		byID[fact.Subject.CanonicalID] = fact
	}
	owning, ok := byID["repository:repo-1"].Fields["owning_teams"]
	if !ok || owning.Table == nil || len(owning.Table.Rows) != 2 {
		t.Fatalf("repo-1 owning_teams = %+v, want a 2-row table", owning)
	}
	if err := owning.Table.Validate(); err != nil {
		t.Fatalf("owning_teams table invalid: %v", err)
	}
	if got := *owning.Table.Rows[1].Fields["team_id"].String; got != "team-b" {
		t.Fatalf("second owner = %q, want team-b", got)
	}
	if got := *owning.Table.Rows[0].Fields["team_name"].String; got != "Alpha" {
		t.Fatalf("first owner name = %q, want Alpha", got)
	}
	if _, present := byID["repository:repo-2"].Fields["owning_teams"]; present {
		t.Fatalf("repo-2 has no owner row and must carry no owning_teams (missing is not empty)")
	}
	last := client.queries[0]
	for _, q := range client.queries {
		if strings.Contains(q.statement, "team_repo_ownership") {
			last = q
		}
	}
	for _, want := range []string{"valid_to IS NULL", "FROM teams FINAL", "WHERE org_id = {org_id:String} AND is_active = 1) AS t", "lower(r.repo) = lower(o.repo_full_name)"} {
		if !strings.Contains(last.statement, want) {
			t.Fatalf("ownership statement lacks %q:\n%s", want, last.statement)
		}
	}
	if strings.Contains(last.statement, "team_member") {
		t.Fatalf("ownership statement reads membership:\n%s", last.statement)
	}
}

// Not parallel: it swaps the process default logger to capture the log line.
func TestIdentityProviderServesFactsWhenTheOwnershipReadFails(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	client := &fakeClient{tables: []fakeTable{
		{match: "team_repo_ownership", err: errors.New("ownership store down")},
		{match: "FROM repos", rows: [][]any{{"repo-1", "acme/one", "github"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactIdentity,
		Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v, want the identity fact served", err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Fields["name"].String == nil {
		t.Fatalf("facts = %+v, want the repository identity fact", result.Facts)
	}
	if _, present := result.Facts[0].Fields["owning_teams"]; present {
		t.Fatalf("owning_teams present after a failed ownership read")
	}
	if result.State != contextfabric.SourceTruncated || result.OmittedCount != 1 || !strings.Contains(result.Reason, "owning_teams_unread") {
		t.Fatalf("state=%q omitted=%d reason=%q, want truncated, 1, owning_teams_unread: ownership is unknown, not empty", result.State, result.OmittedCount, result.Reason)
	}
	if !strings.Contains(logs.String(), "context_fabric_owning_teams_read_failed") {
		t.Fatalf("no log line for the failed ownership read: %q", logs.String())
	}
}
