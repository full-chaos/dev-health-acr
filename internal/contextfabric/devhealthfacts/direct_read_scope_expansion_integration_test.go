package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// grantedRepositoryGraph admits every subject and reports each team as
// reaching the one granted repository, so a repository-restricted caller is
// admitted to the team root.
type grantedRepositoryGraph struct {
	admitAllGraph
	reached string
}

func (g grantedRepositoryGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	out := make([][]string, len(subjects))
	for index := range out {
		out[index] = []string{g.reached}
	}
	return out, nil
}

type derivedCountsRecorder struct{ records []directread.FactsReadRecord }

func (r *derivedCountsRecorder) RecordDirectFactsRead(_ context.Context, _ storage.Principal, record directread.FactsReadRecord) {
	r.records = append(r.records, record)
}

// read_facts for a team's blockers, through the production direct fact
// source with the REAL scope expander on ClickHouse: the team's work items are
// reached and their blocker facts served, each naming the team; a work item
// on a repository outside a restricted caller's grant is dropped by the
// expander and its fact is never served.
func TestDirectReadServesTeamBlockersThroughTheRealScopeExpander(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	for _, statement := range devhealthschema.DDL("work_item_dependencies", "work_item_dependency_first_seen") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := time.Now().UTC().Truncate(time.Second)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	const teamID = "PLATFORM"
	granted, other := repoUUID("direct-read-granted"), repoUUID("direct-read-other")
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, granted, orgID, "acme/granted", "github", at)
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, other, orgID, "acme/other", "github", at)
	// The provider authorizes both ends of a blocker edge, so each blocking
	// item is a work item in the same repository as the item it blocks.
	for _, item := range []struct{ id, repo string }{{"WI-GRANTED", granted}, {"WI-OTHER", other}} {
		for _, id := range []string{item.id, item.id + "-BLOCKER"} {
			exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, item.repo, orgID, "linear", "issue "+id, "open", "", "", "", at)
		}
		exec(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			orgID, item.repo, item.id, teamID, "native_team", uint8(1), "high", at)
		exec(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?)`,
			item.id+"-BLOCKER", item.id, "blocks", "Blocks", at, orgID)
	}
	workItem := func(repoID, id string) contextfabric.SubjectRef {
		canonical, _, err := identity.Derive(identity.KindWorkItem, []string{repoID, id}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: canonical}
	}
	grantedItem, otherItem := workItem(granted, "WI-GRANTED"), workItem(other, "WI-OTHER")
	team := teamSubject(teamID)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{ScopeExpander: devhealthfacts.NewScopeExpander(query)})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	read := func(t *testing.T, principal storage.Principal) (directread.FactsResponse, directread.FactsReadRecord) {
		t.Helper()
		recorder := &derivedCountsRecorder{}
		reader := directread.NewFactsReader(directread.NewSubjectGate(grantedRepositoryGraph{reached: "acme/granted"}, nil), directread.NewFactReader(registry.DirectReadFactSource()), recorder)
		response, err := reader.Read(observability.WithRequestID(ctx, "req_0000000000000000000000000000d1e5"), principal, directread.FactsRequest{
			Kinds:    []string{"blockers"},
			Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: team.CanonicalID}},
		})
		if err != nil {
			t.Fatalf("read_facts: %v", err)
		}
		return response, recorder.records[0]
	}
	served := func(response directread.FactsResponse, subject contextfabric.SubjectRef) *directread.ServedFact {
		for index := range response.Facts {
			fact := &response.Facts[index]
			if fact.Kind == "blockers" && fact.Subject.Kind == string(subject.Kind) && fact.Subject.CanonicalID == subject.CanonicalID {
				return fact
			}
		}
		return nil
	}

	t.Run("organization-wide caller", func(t *testing.T) {
		response, record := read(t, storage.Principal{OrgID: orgID, Subject: "direct-read", CredentialID: "direct-read"})
		for _, item := range []contextfabric.SubjectRef{grantedItem, otherItem} {
			fact := served(response, item)
			if fact == nil {
				t.Fatalf("no blocker fact on a reached work item: %+v", response)
			}
			if len(fact.ReachedFrom) != 1 || fact.ReachedFrom[0].Kind != "team" || fact.ReachedFrom[0].CanonicalID != team.CanonicalID {
				t.Fatalf("reached_from %+v, want the team", fact.ReachedFrom)
			}
		}
		if record.DerivedFactsServed != 2 || record.DerivedUnauthorized != 0 {
			t.Fatalf("record %+v", record)
		}
	})
	t.Run("repository-restricted caller", func(t *testing.T) {
		response, record := read(t, storage.Principal{OrgID: orgID, Subject: "direct-read-r", CredentialID: "direct-read-r", RepositoryScopes: []string{"acme/granted"}})
		if served(response, grantedItem) == nil {
			t.Fatalf("no blocker fact on the granted work item: %+v", response)
		}
		if fact := served(response, otherItem); fact != nil {
			t.Fatalf("a work item outside the grant was served: %+v", fact)
		}
		if record.DerivedFactsServed != 1 || record.DerivedUnauthorized == 0 {
			t.Fatalf("record %+v", record)
		}
	})
}
