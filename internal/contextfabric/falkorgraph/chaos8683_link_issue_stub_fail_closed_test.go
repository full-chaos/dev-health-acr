package falkorgraph

// CHAOS-8683: the LINKS_PULL_REQUEST edge carries the pull request's
// repository scope. The projector writes relationships before entities, so
// the issue stub the edge creates must not inherit that scope: a caller scoped
// to the pull request's repository would otherwise see a repository-less or
// other-repository issue until its own entity merges. The write payloads come
// from the real projectRelationship and are judged by the real
// graphrank.AuthorizedAttributes.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTheLinkEdgesIssueStubFailsClosedUntilTheIssueEntityArrives(t *testing.T) {
	adapter, writes := capturingAdapter(t)
	issue := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:linear%3ALESS-1", Label: "linear:LESS-1"}
	pull := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:r:11", Label: "PR 11"}
	edge := contextfabric.RelationshipProjection{
		RelationshipID: "rel_link", Type: contractsv1.ContextFabricRelationshipLinksPullRequest, From: issue, To: pull,
		Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
		Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/svc"}},
		ObservedAt:    time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), SourceVersion: "v1",
	}
	if err := adapter.projectRelationship(context.Background(), "key", "org-1", edge); err != nil {
		t.Fatal(err)
	}
	w := (*writes)[len(*writes)-1]
	scoped := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/svc"}}
	fromAttrs := attrsOf(t, w, "fromAttrs")
	if got, _ := fromAttrs[propAuthzRepos].([]string); len(got) != 1 || got[0] != referencedEndpointStubSentinel {
		t.Fatalf("issue stub authorization_repositories = %v, want the fail-closed sentinel (not the pull request's [acme/svc])", fromAttrs[propAuthzRepos])
	}
	if graphrank.AuthorizedAttributes(scoped, contextfabric.RequestedScope{}, fromAttrs) {
		t.Fatal("a caller scoped to the pull request's repository was ADMITTED to the issue stub before its entity arrived")
	}
	if !graphrank.AuthorizedAttributes(storage.Principal{OrgID: "org-1"}, contextfabric.RequestedScope{}, fromAttrs) {
		t.Fatal("an unrestricted caller must still see the issue stub")
	}
	// The pull request end keeps the edge's scope: it IS the pull request's.
	toAttrs := attrsOf(t, w, "toAttrs")
	if got, _ := toAttrs[propAuthzRepos].([]string); len(got) != 1 || got[0] != "acme/svc" {
		t.Fatalf("pull request stub authorization = %v, want [acme/svc]", toAttrs[propAuthzRepos])
	}
	// Any other relationship type keeps the pre-existing stub behaviour.
	other := edge
	other.Type = contractsv1.ContextFabricRelationshipRelatesTo
	if err := adapter.projectRelationship(context.Background(), "key", "org-1", other); err != nil {
		t.Fatal(err)
	}
	if got, _ := attrsOf(t, (*writes)[len(*writes)-1], "fromAttrs")[propAuthzRepos].([]string); len(got) != 1 || got[0] != "acme/svc" {
		t.Fatalf("RELATES_TO work item stub authorization = %v, want the edge's [acme/svc] (unchanged)", got)
	}
}
