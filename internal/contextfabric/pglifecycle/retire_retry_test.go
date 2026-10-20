package pglifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/stretchr/testify/require"
)

type retireRecordStore struct {
	contextfabric.GraphLifecycleStore
	record contextfabric.EpochRetirement
}

func (s *retireRecordStore) Get(context.Context, string) (contextfabric.OrgGraphLifecycle, bool, error) {
	return contextfabric.OrgGraphLifecycle{ActiveEpoch: 2}, true, nil
}

func (s *retireRecordStore) DrainingRetirements(_ context.Context, cutoff time.Time) ([]contextfabric.EpochRetirement, error) {
	if s.record.State == contextfabric.RetireRecordDraining && !s.record.DrainStart.After(cutoff) {
		return []contextfabric.EpochRetirement{s.record}, nil
	}
	return nil, nil
}

func (s *retireRecordStore) AdvanceRetirement(_ context.Context, _ string, _ int64, expected, next contextfabric.RetireRecordState, _ time.Time) (contextfabric.EpochRetirement, error) {
	if s.record.State != expected {
		return contextfabric.EpochRetirement{}, contextfabric.ErrLifecycleConflict
	}
	s.record.State = next
	return s.record, nil
}

type flakyGraphDeleter struct{ calls, failFirst int }

func (g *flakyGraphDeleter) DeleteEpochGraph(context.Context, string, int64, int64) error {
	g.calls++
	if g.calls <= g.failFirst {
		return errors.New("graph delete failed")
	}
	return nil
}

func TestRetireExecutor_RetriesAfterDeleteFailure(t *testing.T) {
	drainStart := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name            string
		failGraph       bool
		failCheckpoints bool
	}{
		{"graph_delete_fails_once", true, false},
		{"checkpoint_delete_fails_once", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := &retireRecordStore{record: contextfabric.EpochRetirement{
				OrgID: "org-1", Epoch: 1, DrainStart: drainStart, State: contextfabric.RetireRecordDraining,
			}}
			graph := &flakyGraphDeleter{}
			if tc.failGraph {
				graph.failFirst = 1
			}
			checkpointCalls := 0
			executor := &pglifecycle.RetireExecutor{
				Store: store, Graph: graph,
				Checkpoints: checkpointDeleterFunc(func(context.Context, string, int64) error {
					checkpointCalls++
					if tc.failCheckpoints && checkpointCalls == 1 {
						return errors.New("checkpoint delete failed")
					}
					return nil
				}),
				Lease: time.Minute, Deadline: time.Minute,
				Now: func() time.Time { return drainStart.Add(time.Hour) },
			}

			require.Error(t, executor.RunOne(ctx, "org-1", 1))
			due, err := executor.DueRetirements(ctx)
			require.NoError(t, err)
			require.Len(t, due, 1, "a failed delete leaves the retirement listed for the next tick")
			require.Equal(t, contextfabric.RetireRecordDraining, store.record.State)

			require.NoError(t, executor.RunOne(ctx, "org-1", 1), fmt.Sprintf("state %s", store.record.State))
			require.Equal(t, contextfabric.RetireRecordDeleted, store.record.State)
			due, err = executor.DueRetirements(ctx)
			require.NoError(t, err)
			require.Empty(t, due)
		})
	}
}
