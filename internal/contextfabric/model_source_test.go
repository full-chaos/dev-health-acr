package contextfabric

import (
	"context"
	"errors"
	"sync"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestStampModelSource(t *testing.T) {
	record := func(ctx context.Context, sources ...contractsv1.ContextFabricModelSource) context.Context {
		ctx = WithModelSourceRecorder(ctx)
		for _, source := range sources {
			RecordModelSource(ctx, source)
		}
		return ctx
	}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		stored  contractsv1.ContextFabricModelSource
		want    contractsv1.ContextFabricModelSource
		wantErr error
	}{
		{name: "no recorder leaves a result as it is", ctx: context.Background()},
		{name: "no model call leaves no label", ctx: record(context.Background())},
		{name: "a reused result keeps the stored label", ctx: record(context.Background()), stored: contractsv1.ContextFabricModelSourcePlatform, want: contractsv1.ContextFabricModelSourcePlatform},
		{name: "one own-model call", ctx: record(context.Background(), contractsv1.ContextFabricModelSourceOrganization), want: contractsv1.ContextFabricModelSourceOrganization},
		{name: "several platform calls", ctx: record(context.Background(), contractsv1.ContextFabricModelSourcePlatform, contractsv1.ContextFabricModelSourcePlatform), want: contractsv1.ContextFabricModelSourcePlatform},
		{name: "mixed sources", ctx: record(context.Background(), contractsv1.ContextFabricModelSourceOrganization, contractsv1.ContextFabricModelSourcePlatform), wantErr: ErrModelSourceMixed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := InvestigationResult{ModelSource: tc.stored}
			got, err := stampModelSource(tc.ctx, result)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && got.ModelSource != tc.want {
				t.Fatalf("model source = %q, want %q", got.ModelSource, tc.want)
			}
		})
	}
}

func TestModelSourceRecorderIsSafeForTheConcurrentEnsemble(t *testing.T) {
	ctx := WithModelSourceRecorder(context.Background())
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordModelSource(ctx, contractsv1.ContextFabricModelSourceOrganization)
		}()
	}
	wg.Wait()
	if got, err := ModelSourceOf(ctx); err != nil || got != contractsv1.ContextFabricModelSourceOrganization {
		t.Fatalf("model source = %q, %v", got, err)
	}
}

// finalizeServed is the one point every serving path goes through: it stamps
// the turn's source, and refuses a mixed turn instead of serving it.
func TestFinalizeServedStampsOrRefusesTheModelSource(t *testing.T) {
	engine := &Engine{}
	for _, tc := range []struct {
		name    string
		sources []contractsv1.ContextFabricModelSource
		want    contractsv1.ContextFabricModelSource
		wantErr error
	}{
		{name: "own model", sources: []contractsv1.ContextFabricModelSource{contractsv1.ContextFabricModelSourceOrganization}, want: contractsv1.ContextFabricModelSourceOrganization},
		{name: "platform model", sources: []contractsv1.ContextFabricModelSource{contractsv1.ContextFabricModelSourcePlatform}, want: contractsv1.ContextFabricModelSourcePlatform},
		{name: "mixed", sources: []contractsv1.ContextFabricModelSource{contractsv1.ContextFabricModelSourceOrganization, contractsv1.ContextFabricModelSourcePlatform}, wantErr: ErrModelSourceMixed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithModelSourceRecorder(context.Background())
			for _, source := range tc.sources {
				RecordModelSource(ctx, source)
			}
			served, err := engine.finalizeServed(ctx, storage.Principal{}, BudgetAssertWindowVeto, InvestigationResult{}, nil, ResponseBudget{})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && served.ModelSource != tc.want {
				t.Fatalf("served model source = %q, want %q", served.ModelSource, tc.want)
			}
		})
	}
}
