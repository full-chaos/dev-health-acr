package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestCountKindTotalCountsEveryNodeOfTheKindWithoutTheOwnedFilter(t *testing.T) {
	var captured []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
		captured = append(captured, q)
		return []row{{"total": int64(7)}}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	total, err := adapter.CountKindTotal(context.Background(), "org-1", contextfabric.SubjectPullRequest)
	if err != nil || total != 7 {
		t.Fatalf("CountKindTotal = %d, %v; want 7", total, err)
	}
	if len(captured) != 1 || strings.Contains(captured[0], propProjectionOwned) || !strings.Contains(captured[0], "count(n)") {
		t.Fatalf("CountKindTotal must be one aggregate with no owned filter: %v", captured)
	}
}
