package memoryinvestigation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestStore_parity runs the shared contextfabric.InvestigationResultStore
// behavior table (save/get roundtrip, org scoping, immutability) against
// the in-memory store. pginvestigation's integration test runs the exact
// same table against Postgres, so the two implementations cannot silently
// drift apart.
func TestStore_parity(t *testing.T) {
	paritytest.RunSuite(t,
		func(t *testing.T) contextfabric.InvestigationResultStore { return memoryinvestigation.NewStore() },
		func(err error) bool { return errors.Is(err, memoryinvestigation.ErrNotFound) },
	)
}

func TestStore_getDefensiveCopyDoesNotLeakStoredState(t *testing.T) {
	ctx := context.Background()
	store := memoryinvestigation.NewStore()
	principal := storage.Principal{OrgID: "org-1"}
	original := contextfabric.InvestigationResult{
		SchemaVersion:       contextfabric.InvestigationResultSchemaV1,
		ResultID:            "result-defensive-copy",
		RequestID:           "request-defensive-copy",
		GeneratedAt:         time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		Status:              contextfabric.InvestigationComplete,
		Question:            "original question",
		DirectJudgment:      "original judgment",
		DeterministicAnswer: "original deterministic answer",
		StrongestPressures:  []string{"pressure-1"},
	}
	if err := store.Save(ctx, principal, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	first, err := store.Get(ctx, principal, original.ResultID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	first.StrongestPressures[0] = "mutated"
	first.Question = "mutated question"

	second, err := store.Get(ctx, principal, original.ResultID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if second.Question != "original question" {
		t.Fatalf("stored question mutated: got %q", second.Question)
	}
	if second.StrongestPressures[0] != "pressure-1" {
		t.Fatalf("stored strongest_pressures mutated: got %q", second.StrongestPressures[0])
	}
}

func TestStore_saveRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := memoryinvestigation.NewStore()
	err := store.Save(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationResult{ResultID: "result-cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("save: want context.Canceled, got %v", err)
	}
}
