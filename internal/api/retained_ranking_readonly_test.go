package api

import (
	"context"
	"errors"
	"testing"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Reads must leave the retained carrier untouched even at the port boundary:
// a no-op Save fake would hide an unwanted persistence attempt.
type retainedRankingReadOnlyStore struct {
	retainedRankingSnapshotStore
	t *testing.T
}

func (s retainedRankingReadOnlyStore) Save(context.Context, storage.Principal, cf.InvestigationResult, cf.SourceWatermarkSnapshot, cf.RebuildEpoch, string, cf.ReuseRetrievalIdentity, cf.ReusePromptVersions, cf.ReuseVersionAuthorities, int64, string, cf.SemanticStateWrite) error {
	s.t.Error("retained ranking serving attempted to write storage")
	return errors.New("unexpected retained result write")
}

// ResolveSubjects remains available for the existing live authorization check;
// fresh discovery is forbidden on a reuse hit, like the fact/model spies.
type retainedRankingReuseGraph struct {
	surfaceGraph
	t *testing.T
}

func (g retainedRankingReuseGraph) DiscoverContext(context.Context, storage.Principal, cf.GraphDiscoveryRequest) (cf.GraphContext, error) {
	g.t.Error("retained ranking reuse attempted fresh discovery")
	return cf.GraphContext{}, errors.New("unexpected retained result discovery")
}
