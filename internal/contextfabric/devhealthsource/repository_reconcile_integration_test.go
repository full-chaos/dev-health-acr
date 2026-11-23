package devhealthsource_test

// A repos row can land long after its last_synced stamp (a sync unit stamps
// at attempt start and a retry keeps that stamp). The shared keyset cursor and
// its overlap window re-read only rows that land within about overlap + slack
// of their stamp, so such a repository is never read by the paged walk. The
// repository reconcile pass re-reads the organization's current repos in id
// order at most once per period and emits what the walk missed, without moving
// the cursor position. On a real ClickHouse.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

const repositoryReconcilePeriod = devhealthsource.RepositoryReconcilePeriodForTest

func TestRepositoryLandedBehindTheWindowIsReconciled(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	const orgID = "72950000-0000-4000-8000-000000000001"
	const repoA, repoB = "72950000-0000-4000-8000-0000000000a1", "72950000-0000-4000-8000-0000000000b1"

	src, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	src.SetClockForTest(func() time.Time { return clock })
	h := &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.SourceName, orgID: orgID, repo: repoA}

	insertRepo(t, ctx, h, repoA, "acme/landed-on-time", "github", now.Add(-10*time.Minute))
	first := h.drain("")
	if _, ok := first.all[contextfabric.RepositoryCanonicalID(repoA)]; !ok {
		t.Fatalf("precondition: repository A was not projected by the first drain: %v", first.all)
	}

	// Stamped five hours ago, landing now: far outside the overlap window.
	insertRepo(t, ctx, h, repoB, "acme/landed-late", "github", now.Add(-5*time.Hour))
	clock = now.Add(time.Minute)
	inside := h.drain(first.cursor)
	if _, ok := inside.all[contextfabric.RepositoryCanonicalID(repoB)]; ok {
		t.Fatalf("repository B was projected inside the reconcile period: the overlap window is wider than documented")
	}

	clock = now.Add(repositoryReconcilePeriod)
	reconciled := h.drain(inside.cursor)
	got, ok := reconciled.all[contextfabric.RepositoryCanonicalID(repoB)]
	if !ok {
		t.Fatalf("repository B, which landed behind the window, was not projected once the reconcile period elapsed: %v", reconciled.all)
	}
	if got.Subject.Label != "acme/landed-late" || got.ProviderIDs["github"] != repoB {
		t.Fatalf("repository B was reconciled as %+v, want the entity the paged read builds", got)
	}
	if synced := got.Properties[contextfabric.ProjectionSyncedAtProperty].Integer; synced == nil || *synced != now.Add(-5*time.Hour).UnixNano() {
		t.Fatalf("repository B was reconciled without its sync stamp property: %+v", got.Properties)
	}
	if keysetPosition(t, reconciled.cursor) != keysetPosition(t, first.cursor) {
		t.Fatalf("the reconcile pass moved the cursor position (%q -> %q)", first.cursor, reconciled.cursor)
	}
	if _, ok := reconciled.all[contextfabric.RepositoryCanonicalID(repoA)]; ok {
		t.Fatalf("the reconcile pass wrote repository A again, which the paged read had already emitted unchanged")
	}

	clock = now.Add(repositoryReconcilePeriod + time.Minute)
	if again := h.drain(reconciled.cursor); len(again.batches) != 0 {
		t.Fatalf("a drain inside the next period emitted %d batches after the pass ended", len(again.batches))
	}
}

// The pass reads every provider's repos the same way: one GitHub and one
// GitLab repository, both landed behind the window, are both reconciled.
func TestLateRepositoriesOfEveryProviderAreReconciled(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	const orgID = "72950000-0000-4000-8000-000000000002"
	const anchor = "72950000-0000-4000-8000-0000000000a2"
	late := map[string]struct{ id, slug string }{
		"github": {"72950000-0000-4000-8000-0000000000c2", "acme/late-on-github"},
		"gitlab": {"72950000-0000-4000-8000-0000000000d2", "acme-group/late-on-gitlab"},
	}

	src, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	src.SetClockForTest(func() time.Time { return clock })
	h := &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.SourceName, orgID: orgID, repo: anchor}

	insertRepo(t, ctx, h, anchor, "acme/anchor", "github", now.Add(-10*time.Minute))
	first := h.drain("")
	insertRepo(t, ctx, h, late["github"].id, late["github"].slug, "github", now.Add(-5*time.Hour))
	insertRepo(t, ctx, h, late["gitlab"].id, late["gitlab"].slug, "gitlab", now.Add(-6*time.Hour))

	clock = now.Add(repositoryReconcilePeriod)
	reconciled := h.drain(first.cursor)
	for provider, repo := range late {
		got, ok := reconciled.all[contextfabric.RepositoryCanonicalID(repo.id)]
		if !ok {
			t.Errorf("the late %s repository was not reconciled: %v", provider, reconciled.all)
			continue
		}
		if got.Subject.Label != repo.slug || got.ProviderIDs[provider] != repo.id {
			t.Errorf("the late %s repository was reconciled as %+v", provider, got)
		}
	}
	if keysetPosition(t, reconciled.cursor) != keysetPosition(t, first.cursor) {
		t.Fatalf("the reconcile pass moved the cursor position (%q -> %q)", first.cursor, reconciled.cursor)
	}
}

// A projector process that starts with a saved cursor runs the pass at once,
// so a restart heals a repository that landed behind the window.
func TestARestartedProjectorReconcilesRepositoriesAtOnce(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	const orgID = "72950000-0000-4000-8000-000000000003"
	const repoA, repoB = "72950000-0000-4000-8000-0000000000a3", "72950000-0000-4000-8000-0000000000b3"

	src, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	src.SetClockForTest(func() time.Time { return now })
	h := &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.SourceName, orgID: orgID, repo: repoA}

	insertRepo(t, ctx, h, repoA, "acme/before-restart", "github", now.Add(-10*time.Minute))
	first := h.drain("")
	insertRepo(t, ctx, h, repoB, "acme/landed-while-down", "github", now.Add(-5*time.Hour))

	restarted, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	restarted.SetClockForTest(func() time.Time { return now.Add(time.Minute) })
	h.src = restarted
	second := h.drain(first.cursor)
	if _, ok := second.all[contextfabric.RepositoryCanonicalID(repoB)]; !ok {
		t.Fatalf("a restarted projector with a saved cursor did not reconcile the late repository at once: %v", second.all)
	}
	if keysetPosition(t, second.cursor) != keysetPosition(t, first.cursor) {
		t.Fatalf("the reconcile pass moved the cursor position (%q -> %q)", first.cursor, second.cursor)
	}
}

func insertRepo(t *testing.T, ctx context.Context, h *ingestHarness, id, slug, provider string, stamp time.Time) {
	t.Helper()
	mustExec(t, ctx, h.direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`,
		id, slug, nil, stamp, nil, stamp, h.orgID, provider)
}
