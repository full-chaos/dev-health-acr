package falkorgraph

// CHAOS-7242: a REFERENCED team/project stub must not inherit the referencing
// relationship's authorization scope. The projector writes relationships
// before entities and the stub is created ON CREATE with the first edge's
// scope (one repository slug), so before this change a principal scoped to
// that repository was admitted to a team/project node before (or without) its
// canonical entity (codex #724 r3). The write payloads are captured from the
// real projectRelationship/projectEntity/projectContent/projectEpisode and
// judged by the real graphrank.AuthorizedAttributes.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type capturedWrite struct {
	cypher string
	params map[string]interface{}
}

func capturingAdapter(t *testing.T) (*Adapter, *[]capturedWrite) {
	t.Helper()
	var writes []capturedWrite
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		writes = append(writes, capturedWrite{cypher: cypher, params: params})
		return nil, nil
	}}
	return newFakeAdapter(t, fake), &writes
}

func attrsOf(t *testing.T, w capturedWrite, param string) map[string]interface{} {
	t.Helper()
	attrs, ok := w.params[param].(map[string]interface{})
	if !ok {
		t.Fatalf("write has no %q attribute map: %v", param, w.params)
	}
	return attrs
}

func ownershipEdge(team contextfabric.SubjectRef, repoSlug string) contextfabric.RelationshipProjection {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoSlug, Label: repoSlug}
	return contextfabric.RelationshipProjection{
		RelationshipID: "rel_" + strings.ReplaceAll(repoSlug, "/", "_") + "_" + team.CanonicalID, Type: contractsv1.ContextFabricRelationshipOwnedByTeam,
		From: repo, To: team, Derivation: contextfabric.DerivationRuleInferred, EpistemicStatus: contextfabric.EpistemicInferred,
		Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{repoSlug}, TeamIDs: []string{team.CanonicalID}},
		EvidenceRefIDs: []string{"evidence_" + repoSlug}, ObservedAt: at, SourceVersion: "v1",
	}
}

var stubPrincipalR = storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/r"}}

func TestReferencedTeamAndProjectStubsFailClosedUntilTheEntityArrives(t *testing.T) {
	ctx := context.Background()
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T", Label: "T"}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:P", Label: "P"}

	t.Run("page 0: edges and NO team entity -> a restricted principal is denied the stub", func(t *testing.T) {
		adapter, writes := capturingAdapter(t)
		if err := adapter.projectRelationship(ctx, "key", "org-1", ownershipEdge(team, "acme/r")); err != nil {
			t.Fatal(err)
		}
		w := (*writes)[len(*writes)-1]
		toAttrs := attrsOf(t, w, "toAttrs")
		if got, _ := toAttrs[propAuthzRepos].([]string); len(got) != 1 || got[0] != referencedEndpointStubSentinel {
			t.Fatalf("team stub authorization_repositories = %v, want the fail-closed sentinel (not the edge's [acme/r])", toAttrs[propAuthzRepos])
		}
		if graphrank.AuthorizedAttributes(stubPrincipalR, contextfabric.RequestedScope{}, toAttrs) {
			t.Fatal("a principal scoped to the edge's repository was ADMITTED to the team stub before its entity arrived")
		}
		// An unrestricted principal still sees the stub.
		if !graphrank.AuthorizedAttributes(storage.Principal{OrgID: "org-1"}, contextfabric.RequestedScope{}, toAttrs) {
			t.Fatal("an unrestricted principal must still see the stub")
		}
		// The repository ENDPOINT keeps the edge scope (unchanged kind).
		fromAttrs := attrsOf(t, w, "fromAttrs")
		if got, _ := fromAttrs[propAuthzRepos].([]string); len(got) != 1 || got[0] != "acme/r" {
			t.Fatalf("repository endpoint authorization = %v, want [acme/r] (only team/project stubs change)", fromAttrs[propAuthzRepos])
		}
	})

	t.Run("project endpoint stub fails closed too", func(t *testing.T) {
		adapter, writes := capturingAdapter(t)
		edge := ownershipEdge(team, "acme/r")
		edge.Type, edge.From, edge.To = contractsv1.ContextFabricRelationshipOwnedByTeam, project, team
		if err := adapter.projectRelationship(ctx, "key", "org-1", edge); err != nil {
			t.Fatal(err)
		}
		w := (*writes)[len(*writes)-1]
		for _, side := range []string{"fromAttrs", "toAttrs"} {
			attrs := attrsOf(t, w, side)
			if got, _ := attrs[propAuthzRepos].([]string); len(got) != 1 || got[0] != referencedEndpointStubSentinel {
				t.Fatalf("%s (team/project stub) authorization = %v, want the sentinel", side, attrs[propAuthzRepos])
			}
			if graphrank.AuthorizedAttributes(stubPrincipalR, contextfabric.RequestedScope{}, attrs) {
				t.Fatalf("%s admitted a restricted principal", side)
			}
		}
	})

	t.Run("an ordinary 2-repository team: both edges' stubs fail closed", func(t *testing.T) {
		adapter, writes := capturingAdapter(t)
		for _, slug := range []string{"acme/r", "acme/s"} {
			if err := adapter.projectRelationship(ctx, "key", "org-1", ownershipEdge(team, slug)); err != nil {
				t.Fatal(err)
			}
			toAttrs := attrsOf(t, (*writes)[len(*writes)-1], "toAttrs")
			principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{slug}}
			if graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, toAttrs) {
				t.Fatalf("principal for %s admitted to the 2-repo team's stub", slug)
			}
		}
	})

	t.Run("the stub write is ON CREATE only: a later edge cannot re-scope it, and the entity wins", func(t *testing.T) {
		adapter, writes := capturingAdapter(t)
		if err := adapter.projectRelationship(ctx, "key", "org-1", ownershipEdge(team, "acme/r")); err != nil {
			t.Fatal(err)
		}
		relCypher := (*writes)[len(*writes)-1].cypher
		if !strings.Contains(relCypher, "ON CREATE SET b += $toAttrs") || strings.Contains(relCypher, "ON MATCH SET b +=") {
			t.Fatalf("stub attrs must be ON CREATE only (a later edge cannot rewrite them):\n%s", relCypher)
		}
		entity := contextfabric.EntityProjection{
			Subject: team, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization:  contextfabric.AuthorizationScope{TeamIDs: []string{"T"}, RepositorySlugs: []string{"acme/r", "acme/s"}},
			EvidenceRefIDs: []string{"evidence_team_t"}, ObservedAt: time.Date(2026, 9, 30, 12, 1, 0, 0, time.UTC), SourceVersion: "v1",
		}
		if err := adapter.projectEntity(ctx, "key", "org-1", entity); err != nil {
			t.Fatal(err)
		}
		attrs := attrsOf(t, (*writes)[len(*writes)-1], "attrs")
		if !strings.Contains((*writes)[len(*writes)-1].cypher, "SET n += $attrs") {
			t.Fatalf("entity write must be the owned `SET n += $attrs`")
		}
		if !graphrank.AuthorizedAttributes(stubPrincipalR, contextfabric.RequestedScope{}, attrs) {
			t.Fatal("after the entity arrived, an owner (acme/r) must be admitted (the entity's decision wins)")
		}
		if graphrank.AuthorizedAttributes(storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/other"}}, contextfabric.RequestedScope{}, attrs) {
			t.Fatal("after the entity arrived, a non-owner must be denied")
		}
	})

	t.Run("orphan edge: the team has NO entity (no teams row) -> the stub stays fail-closed, invisible to a restricted principal", func(t *testing.T) {
		adapter, writes := capturingAdapter(t)
		orphanTeam := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:no-teams-row", Label: "no-teams-row"}
		if err := adapter.projectRelationship(ctx, "key", "org-1", ownershipEdge(orphanTeam, "acme/r")); err != nil {
			t.Fatal(err)
		}
		toAttrs := attrsOf(t, (*writes)[len(*writes)-1], "toAttrs")
		if graphrank.AuthorizedAttributes(stubPrincipalR, contextfabric.RequestedScope{}, toAttrs) {
			t.Fatal("an entity-less team stub must stay invisible to a restricted principal (by design)")
		}
	})

	t.Run("content and episode attachments to a team subject fail closed as well", func(t *testing.T) {
		adapter, writes := capturingAdapter(t)
		at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
		content := contextfabric.ContentProjection{
			ContentID: "content_team_t_1", Subject: team, Title: "note", Body: "body", ContentDigest: "sha256:abcdefgh",
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/r"}},
			EvidenceRefIDs: []string{"evidence_content"}, ObservedAt: at, SourceVersion: "v1", Untrusted: true,
		}
		if err := adapter.projectContent(ctx, "key", "org-1", content); err != nil {
			t.Fatal(err)
		}
		if attrs := attrsOf(t, (*writes)[len(*writes)-1], "subjectAttrs"); graphrank.AuthorizedAttributes(stubPrincipalR, contextfabric.RequestedScope{}, attrs) {
			t.Fatal("content attachment stub admitted a restricted principal to a team node")
		}
		episode := contextfabric.EpisodeProjection{
			EpisodeID: "episode_team_t_1", Subject: team, Goal: "goal", Outcome: "ok", Summary: "sum",
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/r"}},
			EvidenceRefIDs: []string{"evidence_episode"}, StartedAt: at, EndedAt: at.Add(time.Minute), SourceVersion: "v1",
		}
		if err := adapter.projectEpisode(ctx, "key", "org-1", episode); err != nil {
			t.Fatal(err)
		}
		if attrs := attrsOf(t, (*writes)[len(*writes)-1], "subjectAttrs"); graphrank.AuthorizedAttributes(stubPrincipalR, contextfabric.RequestedScope{}, attrs) {
			t.Fatal("episode attachment stub admitted a restricted principal to a team node")
		}
	})
}
