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

// ResolveSubjects remains available for the existing live authorization check.
// The reuse recheck reads the graph once for a stored answer that committed a
// team, to learn whether the team is removed at its source; that read finds
// none here and nothing else of the answer is rediscovered, like the
// fact/model spies.
type retainedRankingReuseGraph struct {
	surfaceGraph
	t *testing.T
}

func (g retainedRankingReuseGraph) DiscoverContext(context.Context, storage.Principal, cf.GraphDiscoveryRequest) (cf.GraphContext, error) {
	return cf.GraphContext{}, nil
}
