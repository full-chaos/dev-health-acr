package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

type closingScanner struct{ contextpacket.ClickHouseRowScanner }

func (closingScanner) Close() error { return nil }

type scannerClient struct{}

func (scannerClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return closingScanner{}, nil
}

func TestProjectorCountsStatementsThatFinishWithNoPeakMemoryValue(t *testing.T) {
	t.Parallel()
	metrics, collect := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	client := capProjectorQueries(scannerClient{}, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics)
	rows, err := client.Query(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if got := collect()[hostedmetrics.ClickHousePeakUnobservedName+"{}"]; got != 1 {
		t.Fatalf("%s = %d, want 1; cells %v", hostedmetrics.ClickHousePeakUnobservedName, got, collect())
	}
}
