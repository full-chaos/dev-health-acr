package hosted

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// TestOpen_wiresRealEpisodeReaderIntoDependenciesRuntimeEpisodeReader is the
// regression test for review finding NEW-1: open() type-asserts
// episodeReader off the UNDECORATED creator, before it gets wrapped by
// newCohortScopedEpisodeCreator (cohortScopedEpisodeCreator does not itself
// implement api.EpisodeReader) -- deliberately, since CHAOS-3565's cohort
// gate governs writes only (ruling (a): reads are never cohort-restricted).
// But nothing previously exercised this wiring: RuntimeDependencies.validate
// permits a nil EpisodeReader, so if the two statements were ever reordered
// (assert-after-wrap instead of assert-before-wrap), the type assertion
// would silently fail, EpisodeReader would stay nil, and every production
// episode GET would 503 -- the exact B1 symptom, reintroduced with the
// entire suite green, because every other cohort test builds
// cohortScopedEpisodeCreator by hand or reads through the raw underlying
// service directly (episode_cohort_readback_test.go), never through
// Dependencies.Runtime.EpisodeReader the way open() actually assigns it.
//
// This is the read-path twin of TestOpen_wiresCohortGateIntoDependenciesRuntimeEpisodes:
// it opens a real runtime via open() and asserts the reached state -- a
// production-shaped read through Dependencies.Runtime.EpisodeReader
// actually reaches the store and returns what was written through
// Dependencies.Runtime.Episodes (the cohort-gated write path) -- rather
// than inspecting open()'s internals.
//
// RED: confirmed by swapping the order of the two statements in open.go
// (assigning episodeReader from episodeCreator AFTER
// newCohortScopedEpisodeCreator wraps it, instead of before) and observing
// this test fail with EpisodeReader == nil; reverted, GREEN with the real
// wiring.
func TestOpen_wiresRealEpisodeReaderIntoDependenciesRuntimeEpisodeReader(t *testing.T) {
	// Given a hosted runtime opened with writeback enabled for a cohort that
	// includes the requesting org, backed by a real episode.Service (which
	// implements both api.EpisodeCreator and api.EpisodeReader) so a read
	// can prove it reaches the same store a write went through.
	events := []string{}
	request := testBuildRequest(t, &events, "")
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	service, err := episode.NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), episode.ServiceOptions{
		Now: func() time.Time { return now }, PacketStore: readbackPacketStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request.factories.newEpisode = func(episodeServiceRequest) (api.EpisodeCreator, error) {
		return service, nil
	}
	request.config.EnableEpisodeWriteback = true
	request.config.EpisodeWritebackCohortOrgIDs = []string{"org_in_cohort"}

	runtime, err := open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	if runtime.Dependencies.Runtime.EpisodeReader == nil {
		t.Fatal("open() did not wire Dependencies.Runtime.EpisodeReader -- every production episode GET would 503")
	}

	// When a cohort org writes through the opened runtime's cohort-gated
	// write path...
	writer := storage.Principal{
		OrgID: "org_in_cohort", RepositoryScopes: []string{"owner/repo"},
		Permissions: []string{auth.ScopeEpisodeWrite}, ProductEntitlements: []string{"agent_context_runtime"},
	}
	created, _, err := runtime.Dependencies.Runtime.Episodes.Create(context.Background(), writer, readbackEpisodeCreate())
	if err != nil {
		t.Fatalf("create through opened runtime: %v", err)
	}

	// Then a read through the opened runtime's own EpisodeReader -- not the
	// raw service, not a hand-built decorator -- must reach the same store
	// and return what was just written.
	reader := storage.Principal{
		OrgID: "org_in_cohort", RepositoryScopes: []string{"owner/repo"},
		Permissions: []string{auth.ScopeEpisodeRead}, ProductEntitlements: []string{"agent_context_runtime"},
	}
	got, err := runtime.Dependencies.Runtime.EpisodeReader.GetByID(context.Background(), reader, created.EpisodeID)
	if err != nil || got.EpisodeID != created.EpisodeID {
		t.Fatalf("read through opened runtime's EpisodeReader = (%#v, %v), want the episode just created (%q)", got, err, created.EpisodeID)
	}
	listed, err := runtime.Dependencies.Runtime.EpisodeReader.List(context.Background(), reader, "owner/repo", 10)
	if err != nil || len(listed) != 1 || listed[0].EpisodeID != created.EpisodeID {
		t.Fatalf("list through opened runtime's EpisodeReader = (%#v, %v), want exactly the created episode", listed, err)
	}
}
