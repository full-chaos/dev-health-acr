package hosted

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/api"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// trackingEpisodeCreator is a bare EpisodeCreator (no cohort awareness of
// its own) used to prove open() -- not the decorator's own unit tests --
// is what puts the cohort gate in front of every write. If open() ever
// stops wrapping the episode service it constructs, this creator would
// happily accept a non-cohort write, which is exactly the state this test
// asserts never happens.
type trackingEpisodeCreator struct{ called bool }

func (c *trackingEpisodeCreator) Create(context.Context, storage.Principal, contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error) {
	c.called = true
	return contractsv1.AgentEpisode{EpisodeID: "should-not-be-reached"}, false, nil
}

// TestOpen_wiresCohortGateIntoDependenciesRuntimeEpisodes is the regression
// test for review finding H2: every existing cohort test (episode_cohort_test.go,
// episode_cohort_readback_test.go) builds cohortScopedEpisodeCreator by
// hand, so none of them would notice if open() stopped installing it.
// This exercises open() itself and asserts the reached state -- a
// non-cohort write through Dependencies.Runtime.Episodes is rejected with
// ErrWritebackNotEnabledForOrg and never reaches the underlying creator --
// rather than inspecting open()'s internals. Confirmed RED (error was nil,
// trackingEpisodeCreator.called was true) against a mutant of open() with
// the `episodeCreator = newCohortScopedEpisodeCreator(...)` wrapping line
// removed; GREEN against the real code.
func TestOpen_wiresCohortGateIntoDependenciesRuntimeEpisodes(t *testing.T) {
	// Given a hosted runtime opened with writeback enabled for a cohort that
	// does not include the requesting org.
	events := []string{}
	request := testBuildRequest(t, &events, "")
	next := &trackingEpisodeCreator{}
	request.factories.newEpisode = func(episodeServiceRequest) (api.EpisodeCreator, error) {
		return next, nil
	}
	request.config.EnableEpisodeWriteback = true
	request.config.EpisodeWritebackCohortOrgIDs = []string{"org_in_cohort"}

	runtime, err := open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	// When a non-cohort org writes through the OPENED runtime's own wiring.
	_, _, err = runtime.Dependencies.Runtime.Episodes.Create(context.Background(), storage.Principal{OrgID: "org_not_in_cohort"}, contractsv1.AgentEpisodeCreate{})

	// Then the write is rejected and the underlying store is never reached.
	if !errors.Is(err, ErrWritebackNotEnabledForOrg) {
		t.Fatalf("non-cohort write through the opened runtime error = %v, want ErrWritebackNotEnabledForOrg", err)
	}
	if next.called {
		t.Fatal("non-cohort write reached the underlying episode store -- the cohort gate was not wired by open()")
	}
}
