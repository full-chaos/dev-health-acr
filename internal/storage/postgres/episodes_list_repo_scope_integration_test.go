package postgres

import (
	"context"
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestList_repoScopeAppliesBeforeSQLLimitNotAfter is the regression test for
// review finding M3: List's SQL applied LIMIT before scanAuthorizedEpisode's
// per-row repository-scope filter ran in Go. A credential scoped to one
// repository could receive an empty page even though its own repository had
// matching episodes, if enough newer episodes existed org-wide in OTHER
// repositories to fill the LIMIT window first.
//
// The fake sql.Driver used by every other List test in this package
// (episodes_read_test.go) cannot catch this: it hands back exactly the rows
// the test wrote, ignoring what the query's LIMIT/WHERE text would actually
// do against a real row set. This uses a real PostgreSQL container so the
// LIMIT clause's real interaction with row ordering is exercised.
func TestList_repoScopeAppliesBeforeSQLLimitNotAfter(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}

	const orgID = "00000000-0000-0000-0000-0000000000aa"
	seeder := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/repo-a", "acme/repo-b"}}

	// One older episode in repo-a -- the credential under test's own
	// repository -- created first so it is the oldest row org-wide.
	repoA, _, err := store.CreateIdempotent(ctx, seeder, repoScopeEpisodeCreate("acme/repo-a", "repo-a-only"), nil)
	if err != nil {
		t.Fatalf("seed repo-a episode: %v", err)
	}

	// Enough newer episodes in repo-b -- a DIFFERENT repository the test
	// principal is never scoped to -- to fill defaultEpisodeListLimit rows
	// org-wide, all created after (and so ordered before, newest-first) the
	// repo-a episode above.
	for i := range defaultEpisodeListLimit {
		if _, _, err := store.CreateIdempotent(ctx, seeder, repoScopeEpisodeCreate("acme/repo-b", fmt.Sprintf("repo-b-%02d", i)), nil); err != nil {
			t.Fatalf("seed repo-b episode %d: %v", i, err)
		}
	}

	// The credential under test is scoped ONLY to repo-a.
	testPrincipal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/repo-a"}}

	episodes, err := store.List(ctx, testPrincipal, "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(episodes) != 1 || episodes[0].EpisodeID != repoA.EpisodeID {
		t.Fatalf("episodes = %#v, want exactly the repo-a episode %q -- the repo-b rows filling the LIMIT window must not push it out of the page", episodes, repoA.EpisodeID)
	}
}

func repoScopeEpisodeCreate(repoSlug, key string) contractsv1.AgentEpisodeCreate {
	create := postgresEpisodeCreate()
	create.Repository = contractsv1.RepositoryRef{Slug: repoSlug}
	create.ClientEpisodeID = key
	create.IdempotencyKey = key
	return create
}
