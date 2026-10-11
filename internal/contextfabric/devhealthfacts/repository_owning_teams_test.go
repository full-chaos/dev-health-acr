package devhealthfacts_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
			{"repo-1", "team-a", "Alpha", int64(2)},
			{"repo-1", "team-b", "Beta", int64(2)},
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
	for _, want := range []string{"valid_to IS NULL", "FROM teams FINAL", "WHERE org_id = {org_id:String} AND is_active = 1) AS t", "LIMIT 64 BY repo_key", "count() OVER (PARTITION BY repo_key)", "lower(r.repo) = lower(o.repo_full_name)"} {
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

func TestIdentityProviderDisclosesOwnersTheCapLeftOut(t *testing.T) {
	t.Parallel()
	rows := make([][]any, 0, contextfabric.MaxFactValueRows)
	for i := 0; i < contextfabric.MaxFactValueRows; i++ {
		rows = append(rows, []any{"repo-1", fmt.Sprintf("team-%03d", i), "Team", int64(contextfabric.MaxFactValueRows + 5)})
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "team_repo_ownership", rows: rows},
		{match: "FROM repos", rows: [][]any{{"repo-1", "acme/one", "github"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactIdentity,
		Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["owning_teams_omitted_count"].Integer; got == nil || *got != 5 {
		t.Fatalf("owning_teams_omitted_count = %v, want 5 (the owners the per-repository cap left out)", got)
	}
	if result.State != contextfabric.SourceTruncated || result.OmittedCount != 5 || !strings.Contains(result.Reason, "owning_teams_truncated") {
		t.Fatalf("state=%q omitted=%d reason=%q, want truncated, 5, owning_teams_truncated", result.State, result.OmittedCount, result.Reason)
	}
}

func TestIdentityProviderReadsOwnershipInChunks(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repos", rows: nil}}}
	subjects := make([]contextfabric.SubjectRef, 0, 200)
	repoRows := make([][]any, 0, 200)
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("repo-%03d", i)
		subjects = append(subjects, repoSubject(id))
		repoRows = append(repoRows, []any{id, "acme/" + id, "github"})
	}
	client.tables = []fakeTable{{match: "FROM repos", rows: repoRows}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactIdentity, Subjects: subjects,
	}); err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	chunks, covered := 0, 0
	for _, q := range client.queries {
		if !strings.Contains(q.statement, "team_repo_ownership") {
			continue
		}
		chunks++
		for _, b := range q.bindings {
			if b.Name == "ids" {
				ids := b.Value.([]string)
				if len(ids) > 100 {
					t.Fatalf("one ownership query covered %d repositories, want at most 100", len(ids))
				}
				covered += len(ids)
			}
		}
	}
	if chunks != 2 || covered != 200 {
		t.Fatalf("ownership queries = %d covering %d repositories, want 2 covering all 200", chunks, covered)
	}
}
