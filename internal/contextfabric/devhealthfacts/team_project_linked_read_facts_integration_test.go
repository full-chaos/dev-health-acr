package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type admitEverySubject struct{}

func (admitEverySubject) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "project-linked", Epoch: 1}, nil
}

func (admitEverySubject) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range out {
		out[i] = contextfabric.StoredSubjectAdmitted
	}
	return out, nil
}

func (admitEverySubject) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

// The read_facts tool's reader (directread.FactsReader over the production
// providers) serves a team whose repositories are reached only through its
// project's linked pull requests: health is not told it owns no repository,
// and investment is the linked-only mix with the weakest link tier named.
func TestReadFactsServesTeamReachedOnlyThroughProjectLinksAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-read-facts-project-linked"
	seedProjectLinkedOrg(t, ctx, direct, orgID)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("fact registry: %v", err)
	}
	gate := directread.NewSubjectGate(admitEverySubject{}, nil)
	reader := directread.NewFactsReader(gate, directread.NewFactReader(registry.WithoutScopeExpansion()), nil)
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000000001"), storage.Principal{OrgID: orgID, Subject: "project-linked", CredentialID: "project-linked"}, directread.FactsRequest{
		Kinds:    []string{"health", "investment"},
		Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: "team:team-project-only"}},
	})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	served := string(encoded)
	if strings.Contains(served, "owns no repository") || strings.Contains(served, "own no repository") {
		t.Fatalf("read_facts served the no-owned-repository statement for a team that reaches repositories through its project: %s", served)
	}
	var investment *directread.ServedFact
	for i := range response.Facts {
		if response.Facts[i].Kind == "investment" && response.Facts[i].Subject.CanonicalID == "team:team-project-only" {
			investment = &response.Facts[i]
		}
	}
	if investment == nil {
		t.Fatalf("no investment fact served for the project-only team: %s", served)
	}
	number := func(name string) float64 {
		t.Helper()
		switch v := investment.Fields[name].(type) {
		case float64:
			return v
		case int64:
			return float64(v)
		case int:
			return float64(v)
		case string:
			parsed, parseErr := strconv.ParseFloat(v, 64)
			if parseErr == nil {
				return parsed
			}
		}
		t.Fatalf("field %s = %#v, want a number", name, investment.Fields[name])
		return 0
	}
	// linked: repo-a#1 (0.5 feature), repo-r#2 (0.5 feature), repo-r#3 (0.5 risk).
	if got := number("project_reached_repository_count"); got != 2 {
		t.Fatalf("project_reached_repository_count = %v, want 2", got)
	}
	if got := number("owned_repository_count"); got != 0 {
		t.Fatalf("owned_repository_count = %v, want 0", got)
	}
	if got := number("theme_feature_delivery"); got < 0.66 || got > 0.67 {
		t.Fatalf("theme_feature_delivery = %v, want 1 of 1.5 linked units", got)
	}
	if got := number("theme_risk"); got < 0.33 || got > 0.34 {
		t.Fatalf("theme_risk = %v, want 0.5 of 1.5 linked units", got)
	}
	if got, _ := investment.Fields["repository_link_basis"].(string); got != "heuristic" {
		t.Fatalf("repository_link_basis = %#v, want the weakest tier heuristic, never native", investment.Fields["repository_link_basis"])
	}
	table, ok := investment.Tables["project_reached_repositories"]
	if !ok {
		t.Fatalf("project_reached_repositories table not served: %s", served)
	}
	tiers := map[string]string{}
	for _, row := range table.Rows {
		cells := map[string]any{}
		for i, column := range table.Columns {
			if i < len(row) {
				cells[column] = row[i]
			}
		}
		id, _ := cells["repository_id"].(string)
		tier, _ := cells["link_tier"].(string)
		tiers[id] = tier
	}
	if len(tiers) != 2 || tiers[repoUUID("repo-a")] != "native" || tiers[repoUUID("repo-r")] != "heuristic" {
		t.Fatalf("project_reached_repositories = %v, want repo-a native and repo-r heuristic", tiers)
	}
}
