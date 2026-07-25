package contextpacket_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestExecuteCatalog_discloses_executor_failure_phase(t *testing.T) {
	tests := []struct {
		name   string
		client *failurePhaseClient
		phase  contextpacket.SourceQueryPhase
	}{
		{name: "query", client: &failurePhaseClient{queryErr: errors.New("query failed")}, phase: contextpacket.SourceQueryPhaseQuery},
		{name: "scan", client: &failurePhaseClient{rows: &failurePhaseRows{hasRow: true, scanErr: errors.New("scan failed")}}, phase: contextpacket.SourceQueryPhaseScan},
		{name: "iteration", client: &failurePhaseClient{rows: &failurePhaseRows{iterationErr: errors.New("iteration failed")}}, phase: contextpacket.SourceQueryPhaseIteration},
		{name: "close", client: &failurePhaseClient{rows: &failurePhaseRows{closeErr: errors.New("close failed")}}, phase: contextpacket.SourceQueryPhaseClose},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			plan := contextpacket.ReadPlan{CommitSHA: "commit-1"}
			observer := &catalogObserver{}

			// When
			result, err := contextpacket.ExecuteCatalogObserved(
				context.Background(),
				contextpacket.NewClickHouseSourceExecutor(tt.client),
				plan,
				observer,
			)

			// Then
			if err != nil {
				t.Fatalf("execute catalog: %v", err)
			}
			firstSource := contextpacket.SourceQueryCatalogV1[0].ID
			if !containsUnavailable(result.Unavailable, firstSource, "source_unavailable") {
				t.Fatalf("unavailable = %#v, want stable public reason", result.Unavailable)
			}
			if len(observer.store) == 0 || observer.store[0].SourceID != firstSource || observer.store[0].SourcePhase != tt.phase {
				t.Fatalf("first observation = %#v, want source=%q phase=%q", observer.store, firstSource, tt.phase)
			}
		})
	}
}

type failurePhaseClient struct {
	rows     *failurePhaseRows
	queryErr error
}

func (c *failurePhaseClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return c.rows, nil
}

type failurePhaseRows struct {
	hasRow       bool
	scanErr      error
	iterationErr error
	closeErr     error
}

func (r *failurePhaseRows) Next() bool {
	if !r.hasRow {
		return false
	}
	r.hasRow = false
	return true
}

func (r *failurePhaseRows) Scan(...any) error { return r.scanErr }
func (r *failurePhaseRows) Err() error        { return r.iterationErr }
func (r *failurePhaseRows) Close() error      { return r.closeErr }
