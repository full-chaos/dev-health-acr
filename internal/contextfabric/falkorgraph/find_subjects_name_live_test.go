package falkorgraph_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type nameSeed struct {
	id, label, slug string
	aliases         []string
	providerAliases []string
}

// projectNameSeeds projects the seeds as nodes of kind into orgID, in batches
// within the contract bound.
func projectNameSeeds(t *testing.T, ctx context.Context, adapter *falkorgraph.Adapter, orgID, tag string, kind contextfabric.SubjectKind, seeds []nameSeed) {
	t.Helper()
	observed := time.Now().UTC().Add(-time.Minute)
	cursor := ""
	for start, n := 0, 0; start < len(seeds); start, n = start+500, n+1 {
		end := min(start+500, len(seeds))
		entities := make([]contextfabric.EntityProjection, 0, end-start)
		for _, seed := range seeds[start:end] {
			aliases, providerAliases := seed.aliases, seed.providerAliases
			if aliases == nil {
				aliases = []string{}
			}
			if providerAliases == nil {
				providerAliases = []string{}
			}
			entities = append(entities, contextfabric.EntityProjection{
				Subject: contextfabric.SubjectRef{Kind: kind, CanonicalID: seed.id, Label: seed.label},
				Aliases: aliases, ProviderAliases: providerAliases, PreviousNames: []string{}, ProviderIDs: map[string]string{},
				Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{seed.slug}},
				EvidenceRefIDs: []string{fmt.Sprintf("evidence_%s_%06d", tag, start+len(entities))}, ObservedAt: observed, SourceVersion: "v1",
			})
		}
		next := fmt.Sprintf("cursor-%d", n+1)
		_, err := adapter.ApplyProjectionBatch(ctx, contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_name_%s_%03d", tag, n), OrgID: orgID, Source: "live-test",
			SourceVersion: "v1", Cursor: cursor, NextCursor: next, GeneratedAt: observed,
			Entities:      entities,
			Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
			Tombstones: []contextfabric.ProjectionTombstone{},
		})
		require.NoError(t, err, "ApplyProjectionBatch")
		cursor = next
	}
}

func fillerSeeds(prefix string, count int, slug string) []nameSeed {
	seeds := make([]nameSeed, 0, count+1)
	for i := 0; i < count; i++ {
		seeds = append(seeds, nameSeed{id: fmt.Sprintf("%s:a%06d", prefix, i), label: fmt.Sprintf("Filler %06d", i), slug: slug})
	}
	return seeds
}

// TestLiveFindSubjectsNameLookupReachesBeyondTheScanWindow proves, on a real
// graph store through the real subject gate, that a name lookup finds a
// subject wherever it sorts in canonical-id order, for two kinds, by label,
// alias and provider alias; that a caller who may not see the subject gets the
// same answer as for an absent name; that a name with more matches than the
// bound reports the cut; and that an absent name is a clean complete empty.
func TestLiveFindSubjectsNameLookupReachesBeyondTheScanWindow(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	gate := directread.NewSubjectGate(adapter, nil)
	lookup := directread.NewSubjectLookup(adapter, gate, nil)
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	newOrg := func(name string) string {
		org := "live-name-" + name + "-" + stamp
		t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), org) })
		return org
	}
	window := directread.MaxFindScanNodes + 100
	unrestricted := func(org string) storage.Principal {
		return storage.Principal{OrgID: org, Subject: "user-u", CredentialID: "cred-u"}
	}

	for _, tc := range []struct {
		kind   contextfabric.SubjectKind
		prefix string
		label  string
	}{
		{contextfabric.SubjectRepository, "repository", "zeta/last-repo"},
		{contextfabric.SubjectWorkItem, "work_item", "Release Zeta"},
	} {
		t.Run(string(tc.kind)+" sorted last past the window", func(t *testing.T) {
			org := newOrg("last-" + tc.prefix)
			seeds := fillerSeeds(tc.prefix, window, "acme/pub")
			target := nameSeed{id: tc.prefix + ":zzzzzz", label: tc.label, slug: "acme/pub", aliases: []string{"ZETA-ALIAS", "Ünï Älias"}, providerAliases: []string{"github:zeta/provider"}}
			seeds = append(seeds, target)
			projectNameSeeds(t, ctx, adapter, org, tc.prefix, tc.kind, seeds)

			list, err := lookup.Find(ctx, unrestricted(org), directread.FindRequest{Kind: string(tc.kind), Limit: 5})
			require.NoError(t, err)
			require.True(t, list.Population.Truncated, "list mode keeps its disclosed cut")
			require.Equal(t, directread.MaxFindScanNodes, list.Population.TotalKnown)
			for _, subject := range list.Subjects {
				require.NotEqual(t, target.id, subject.CanonicalID, "the target lies outside the list window")
			}

			for _, probe := range []struct{ name, query, match string }{
				{"label", tc.label, "exact"}, {"label folded", strings.ToUpper(tc.label), "exact"},
				{"alias folded", "zeta-alias", "alias"}, {"alias exact case non-ascii", "Ünï Älias", "alias"},
				{"provider alias folded", "GITHUB:ZETA/PROVIDER", "provider_key"},
			} {
				t.Run(probe.name, func(t *testing.T) {
					resp, err := lookup.Find(ctx, unrestricted(org), directread.FindRequest{Query: probe.query, Kinds: []string{string(tc.kind)}})
					require.NoError(t, err)
					require.Len(t, resp.Subjects, 1, "query %q: %+v", probe.query, resp)
					require.Equal(t, target.id, resp.Subjects[0].CanonicalID)
					require.Equal(t, probe.match, resp.Subjects[0].Match)
					require.Equal(t, directread.FindComplete, resp.Status)
					require.True(t, resp.Page.Complete)
					require.False(t, resp.Population.Truncated)
				})
			}

			absent, err := lookup.Find(ctx, unrestricted(org), directread.FindRequest{Query: "no such subject", Kinds: []string{string(tc.kind)}})
			require.NoError(t, err)
			require.Equal(t, directread.FindEmpty, absent.Status)
			require.Empty(t, absent.Subjects)
			require.True(t, absent.Page.Complete)
			require.False(t, absent.Population.Truncated, "an absent name is a clean not-found")
			require.Zero(t, absent.Population.TotalKnown)
		})
	}

	t.Run("restricted caller is not given a subject outside its grant", func(t *testing.T) {
		org := newOrg("restricted")
		seeds := fillerSeeds("repository", window, "acme/pub")
		seeds = append(seeds, nameSeed{id: "repository:zzzzzz", label: "secret/repo", slug: "acme/secret"})
		projectNameSeeds(t, ctx, adapter, org, "restricted", contextfabric.SubjectRepository, seeds)
		restricted := storage.Principal{OrgID: org, Subject: "user-r", CredentialID: "cred-r", RepositoryScopes: []string{"acme/pub"}}

		hidden, err := lookup.Find(ctx, restricted, directread.FindRequest{Query: "secret/repo", Kinds: []string{"repository"}})
		require.NoError(t, err)
		absent, err := lookup.Find(ctx, restricted, directread.FindRequest{Query: "no/such-repo", Kinds: []string{"repository"}})
		require.NoError(t, err)
		require.Empty(t, hidden.Subjects)
		require.Equal(t, absent, hidden, "a refused subject answers as an absent one, count included")

		seen, err := lookup.Find(ctx, unrestricted(org), directread.FindRequest{Query: "secret/repo", Kinds: []string{"repository"}})
		require.NoError(t, err)
		require.Len(t, seen.Subjects, 1)
		require.Equal(t, "repository:zzzzzz", seen.Subjects[0].CanonicalID)
	})

	t.Run("a visible match behind many hidden matches is found", func(t *testing.T) {
		org := newOrg("hidden-many")
		hidden := directread.MaxFindScanNodes + 300
		seeds := make([]nameSeed, 0, hidden+1)
		for i := 0; i < hidden; i++ {
			seeds = append(seeds, nameSeed{id: fmt.Sprintf("repository:h%06d", i), label: "Shared Name", slug: "acme/secret"})
		}
		seeds = append(seeds, nameSeed{id: "repository:zzzzzz", label: "Shared Name", slug: "acme/pub"})
		projectNameSeeds(t, ctx, adapter, org, "hidden", contextfabric.SubjectRepository, seeds)
		restricted := storage.Principal{OrgID: org, Subject: "user-r", CredentialID: "cred-r", RepositoryScopes: []string{"acme/pub"}}
		resp, err := lookup.Find(ctx, restricted, directread.FindRequest{Query: "shared name", Kinds: []string{"repository"}})
		require.NoError(t, err)
		require.Len(t, resp.Subjects, 1, "%+v", resp)
		require.Equal(t, "repository:zzzzzz", resp.Subjects[0].CanonicalID)
		require.Equal(t, 1, resp.Population.TotalKnown, "admitted matches only")
		require.False(t, resp.Population.Truncated)
	})

	t.Run("a name with more matches than the bound reports the cut", func(t *testing.T) {
		org := newOrg("wide")
		seeds := make([]nameSeed, 0, directread.MaxFindScanNodes+5)
		for i := 0; i < directread.MaxFindScanNodes+5; i++ {
			seeds = append(seeds, nameSeed{id: fmt.Sprintf("repository:d%06d", i), label: "Shared Name", slug: "acme/pub"})
		}
		projectNameSeeds(t, ctx, adapter, org, "wide", contextfabric.SubjectRepository, seeds)
		resp, err := lookup.Find(ctx, unrestricted(org), directread.FindRequest{Query: "shared name", Kinds: []string{"repository"}, Limit: 10})
		require.NoError(t, err)
		require.True(t, resp.Population.Truncated, "more matches than the bound: the cut is disclosed")
		require.Equal(t, directread.MaxFindScanNodes, resp.Population.TotalKnown)
		require.NotEqual(t, directread.FindComplete, resp.Status)
		require.Len(t, resp.Subjects, 10)
	})
}
