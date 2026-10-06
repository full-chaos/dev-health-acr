package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// driverLikeClient refuses any statement whose "ids" binding carries a
// backslash, the way the real ClickHouse client does for the whole batch.
type driverLikeClient struct{ inner *fakeClient }

func (c driverLikeClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	for _, b := range bindings {
		if ids, ok := b.Value.([]string); ok {
			for _, id := range ids {
				if strings.Contains(id, `\`) {
					return nil, errors.New("binding value: clickhouse runtime: binding value cannot be safely encoded")
				}
			}
		}
	}
	return c.inner.Query(ctx, statement, bindings)
}

func TestWorkItemBatchIsolatesAnUnsafeIDFromItsSiblings(t *testing.T) {
	inner := &fakeClient{tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{
			{"WIDGET-1", "in_progress", "repo-1", ""},
			{"WIDGET-3", "in_progress", "repo-1", ""},
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(driverLikeClient{inner}), contextfabric.FactStatus)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactStatus,
		Subjects: []contextfabric.SubjectRef{
			workItemSubject("repo-1", "WIDGET-1"),
			workItemSubject("repo-1", `back\slash-2`),
			workItemSubject("repo-1", "WIDGET-3"),
		},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v: one unsafe id must not fail the batch", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("Facts = %d, want 2: the safe siblings must be served", len(result.Facts))
	}
	if result.State != contextfabric.SourceTruncated || result.OmittedCount != 1 {
		t.Fatalf("State=%q Omitted=%d, want truncated with 1 omitted (the unsafe id alone)", result.State, result.OmittedCount)
	}
	if !strings.Contains(result.Reason, "subject_id_shape_rejected") {
		t.Fatalf("Reason = %q, want it to name the unsafe-id cause", result.Reason)
	}
}
