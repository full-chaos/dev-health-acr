package falkorgraph

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveSubjectsAndDiscoverContextRejectAlreadyCancelledContext is the
// falkorgraph twin of zepgraph's TestResolveSubjectsAndDiscoverContextRejectAlreadyCancelledContext.
// falkorgraph had no context-cancellation test anywhere: this proves
// ResolveSubjects/DiscoverContext reject an already-cancelled context.Context
// before doing any work (never a panic or a hang, and never a real backend
// call), using the same fakeConn double codex_round1_fake_test.go's
// TestBootstrapSchema*/TestDiscoverContext* tests already establish.
func TestResolveSubjectsAndDiscoverContextRejectAlreadyCancelledContext(t *testing.T) {
	t.Parallel()
	calls := 0
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		calls++
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	request := fakeDiscoveryRequest(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}, 10).Request
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Ask Dev"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	if _, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: "org_1"}, request, interpreted); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveSubjects() error = %v, want context.Canceled", err)
	}

	discovery := contextfabric.GraphDiscoveryRequest{Request: request, Interpretation: interpreted, Resolution: contextfabric.SubjectResolution{}}
	if _, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: "org_1"}, discovery); !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverContext() error = %v, want context.Canceled", err)
	}

	if calls != 0 {
		t.Fatalf("query() calls = %d, want zero backend calls for an already-cancelled context", calls)
	}
}
