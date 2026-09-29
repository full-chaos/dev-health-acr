package falkorgraph_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// liveScopeUpstream is a fake ops query service that records every request.
type liveScopeUpstream struct {
	server *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newLiveScopeUpstream(t *testing.T) *liveScopeUpstream {
	t.Helper()
	u := &liveScopeUpstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.bodies = append(u.bodies, body)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"hotspots":{"rows":[]}}}`)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *liveScopeUpstream) requests() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any(nil), u.bodies...)
}

func liveScopeSlug(i int) string { return fmt.Sprintf("acme/repo-%03d", i) }

// projectLiveRepositories projects n repository nodes for orgID, each
// authorized by its own slug, and returns their acr ids in slug order.
func projectLiveRepositories(t *testing.T, ctx context.Context, adapter *falkorgraph.Adapter, orgID, batchID string, slugs []string) []string {
	t.Helper()
	observed := time.Now().UTC().Add(-time.Minute)
	entities := make([]contextfabric.EntityProjection, 0, len(slugs))
	ids := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		id := "repository:" + uuid.NewString()
		ids = append(ids, id)
		entities = append(entities, contextfabric.EntityProjection{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: id, Label: slug},
			Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{slug}},
			EvidenceRefIDs: []string{"evidence_" + id[len("repository:"):len("repository:")+12]}, ObservedAt: observed, SourceVersion: "v1",
		})
	}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: batchID, OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities:      entities,
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	_, err := adapter.ApplyProjectionBatch(ctx, batch)
	require.NoError(t, err, "ApplyProjectionBatch")
	return ids
}

func liveScopeRunner(t *testing.T, gate directread.SubjectAuthorizer, grants directread.GrantedRepositories, upstream *liveScopeUpstream) *directread.OperationRunner {
	t.Helper()
	cat, err := directread.DefaultCatalogue()
	require.NoError(t, err)
	client, err := directread.NewHTTPQueryClient(upstream.server.URL, 5*time.Second)
	require.NoError(t, err)
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{Catalogue: cat, Gate: gate, Client: client, Grants: grants})
	require.NoError(t, err)
	return runner
}

func runLiveHotspots(t *testing.T, runner *directread.OperationRunner, principal storage.Principal) directread.OperationResponse {
	t.Helper()
	now := time.Now().UTC()
	raw, err := json.Marshal(map[string]any{"input": map[string]any{
		"sinceUtc": now.AddDate(0, 0, -7).Format(time.RFC3339), "untilUtc": now.Format(time.RFC3339),
	}})
	require.NoError(t, err)
	resp, err := runner.Run(context.Background(), principal, directread.OperationRequest{Operation: "hotspots", Variables: raw})
	require.NoError(t, err)
	return resp
}

func bareLiveUUID(id string) string { return id[len("repository:"):] }

// TestLiveRestrictedForcedScope proves, over a REAL FalkorDB graph and the
// real falkorgraph adapter, that a repository-restricted caller's
// run_operation is forced to the repositories its grant reaches: one of
// three projected repositories reaches exactly one bare-uuid repoIds on the
// wire; an empty intersection ends as no_granted_scope with zero upstream
// requests; a grant past MaxGrantedRepositories ends as scope_required with
// zero upstream requests; and repositories of another organization graph are
// never listed.
func TestLiveRestrictedForcedScope(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	newOrg := func(name string) string {
		org := "live-forced-scope-" + name + "-" + stamp
		t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), org) })
		return org
	}
	gate := directread.NewSubjectGate(adapter, nil)
	grants := directread.NewGrantedRepositories(directread.NewSubjectLookup(adapter, gate, nil))

	t.Run("grant covers one of three repositories", func(t *testing.T) {
		org := newOrg("one")
		ids := projectLiveRepositories(t, ctx, adapter, org, "batch_forced_scope_one_00000001", []string{"acme/a", "acme/b", "acme/c"})
		principal := storage.Principal{OrgID: org, Subject: "user-r", CredentialID: "cred-r", RepositoryScopes: []string{"acme/b"}}

		refs, err := grants.GrantedRepositories(ctx, principal)
		require.NoError(t, err)
		require.Equal(t, []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: ids[1]}}, refs)

		upstream := newLiveScopeUpstream(t)
		resp := runLiveHotspots(t, liveScopeRunner(t, gate, grants, upstream), principal)
		require.Equal(t, directread.CallServed, resp.Call, "%+v", resp.Refusal)
		reqs := upstream.requests()
		require.Len(t, reqs, 1)
		vars, _ := reqs[0]["variables"].(map[string]any)
		input, _ := vars["input"].(map[string]any)
		require.Equal(t, []any{bareLiveUUID(ids[1])}, input["repoIds"], "forced repoIds on the wire")
	})

	t.Run("grant matches no projected repository", func(t *testing.T) {
		org := newOrg("none")
		projectLiveRepositories(t, ctx, adapter, org, "batch_forced_scope_none_0000001", []string{"acme/a", "acme/b", "acme/c"})
		principal := storage.Principal{OrgID: org, Subject: "user-r", CredentialID: "cred-r", RepositoryScopes: []string{"acme/not-projected"}}

		refs, err := grants.GrantedRepositories(ctx, principal)
		require.NoError(t, err)
		require.Empty(t, refs)

		upstream := newLiveScopeUpstream(t)
		resp := runLiveHotspots(t, liveScopeRunner(t, gate, grants, upstream), principal)
		require.Equal(t, directread.CallRefused, resp.Call)
		require.NotNil(t, resp.Refusal)
		require.Equal(t, directread.RefusalNoGrantedScope, resp.Refusal.Code)
		require.Empty(t, upstream.requests(), "no_granted_scope must end before dispatch")
	})

	t.Run("grant over the cap is incomplete and exactly the cap passes", func(t *testing.T) {
		org := newOrg("cap")
		total := directread.MaxGrantedRepositories + 1
		slugs := make([]string, total)
		for i := range slugs {
			slugs[i] = liveScopeSlug(i)
		}
		ids := projectLiveRepositories(t, ctx, adapter, org, "batch_forced_scope_cap_00000001", slugs)
		over := storage.Principal{OrgID: org, Subject: "user-r", CredentialID: "cred-r", RepositoryScopes: slugs}

		_, err := grants.GrantedRepositories(ctx, over)
		require.True(t, errors.Is(err, directread.ErrGrantedRepositoriesIncomplete), "201 granted repositories: err = %v", err)

		upstream := newLiveScopeUpstream(t)
		resp := runLiveHotspots(t, liveScopeRunner(t, gate, grants, upstream), over)
		require.Equal(t, directread.CallRefused, resp.Call)
		require.NotNil(t, resp.Refusal)
		require.Equal(t, directread.RefusalScopeRequired, resp.Refusal.Code)
		require.Empty(t, upstream.requests(), "scope_required must end before dispatch")

		exact := storage.Principal{OrgID: org, Subject: "user-r", CredentialID: "cred-r", RepositoryScopes: slugs[:directread.MaxGrantedRepositories]}
		refs, err := grants.GrantedRepositories(ctx, exact)
		require.NoError(t, err)
		require.Len(t, refs, directread.MaxGrantedRepositories)
		got := map[string]bool{}
		for _, ref := range refs {
			got[ref.CanonicalID] = true
		}
		for _, id := range ids[:directread.MaxGrantedRepositories] {
			require.True(t, got[id], "granted repository %s missing", id)
		}
		require.False(t, got[ids[directread.MaxGrantedRepositories]], "the ungranted 201st repository was listed")

		upstream = newLiveScopeUpstream(t)
		resp = runLiveHotspots(t, liveScopeRunner(t, gate, grants, upstream), exact)
		require.Equal(t, directread.CallServed, resp.Call, "%+v", resp.Refusal)
		reqs := upstream.requests()
		require.Len(t, reqs, 1)
		vars, _ := reqs[0]["variables"].(map[string]any)
		input, _ := vars["input"].(map[string]any)
		list, _ := input["repoIds"].([]any)
		require.Len(t, list, directread.MaxGrantedRepositories)
	})

	t.Run("unrestricted caller lists its own graph only", func(t *testing.T) {
		orgA, orgB := newOrg("unrestricted-a"), newOrg("unrestricted-b")
		idsA := projectLiveRepositories(t, ctx, adapter, orgA, "batch_forced_scope_unra_0000001", []string{"acme/a", "acme/b", "acme/c"})
		idsB := projectLiveRepositories(t, ctx, adapter, orgB, "batch_forced_scope_unrb_0000001", []string{"other/d"})
		refs, err := grants.GrantedRepositories(ctx, storage.Principal{OrgID: orgA, Subject: "user-u", CredentialID: "cred-u"})
		require.NoError(t, err)
		got := map[string]bool{}
		for _, ref := range refs {
			got[ref.CanonicalID] = true
		}
		require.Len(t, refs, len(idsA))
		for _, id := range idsA {
			require.True(t, got[id], "own repository %s missing", id)
		}
		require.False(t, got[idsB[0]], "a repository of another organization graph was returned")
	})
}
