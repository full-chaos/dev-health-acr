package main

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/chserver"
)

// Against a real server, a statement sent through the production projector
// client carries the 192 MiB memory cap, and a statement's own SETTINGS wins.
func TestOpenProjectorClickHouseSendsTheStatementMemoryCapToTheServer(t *testing.T) {
	dsn := chserver.StartDSN(t, context.Background())
	client, err := openProjectorClickHouse(config.ProjectorConfig{ClickHouseDSN: dsn, ClickHouseMaxBytesToRead: 256 << 20}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.close() })
	read := func(statement string) string {
		rows, err := client.query.Query(context.Background(), statement, nil)
		if err != nil {
			t.Fatalf("query %q: %v", statement, err)
		}
		defer func() { _ = rows.Close() }()
		var value string
		if !rows.Next() {
			t.Fatalf("query %q returned no row (err=%v)", statement, rows.Err())
		}
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if got := read("SELECT toString(getSetting('max_memory_usage'))"); got != "201326592" {
		t.Fatalf("server saw max_memory_usage %s through the projector client, want 201326592 (192 MiB)", got)
	}
	if got := read("SELECT toString(getSetting('max_memory_usage')) SETTINGS max_memory_usage = 12345678"); got != "12345678" {
		t.Fatalf("server saw max_memory_usage %s with a statement-level SETTINGS, want 12345678", got)
	}
}

// Against a real server, a statement that finishes above half of its effective
// cap, without a 241, is counted by the projector's near-cap counter. The cap
// is found by doubling until the statement first passes, so its peak is above
// half of that cap.
func TestProjectorCountsStatementsAboveHalfOfTheirMemoryCap(t *testing.T) {
	dsn := chserver.StartDSN(t, context.Background())
	const body = "SELECT toString(count()) FROM (SELECT number FROM numbers(2000000) GROUP BY number) SETTINGS max_memory_usage = "
	for capBytes := uint64(1 << 20); capBytes <= 256<<20; capBytes *= 2 {
		metrics, collect := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
		client, err := openProjectorClickHouse(config.ProjectorConfig{ClickHouseDSN: dsn, ClickHouseMaxBytesToRead: 256 << 20}, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := client.query.Query(context.Background(), body+strconv.FormatUint(capBytes, 10), nil)
		passed := err == nil
		if passed {
			var value string
			passed = rows.Next() && rows.Scan(&value) == nil && rows.Err() == nil
			_ = rows.Close()
		}
		_ = client.close()
		if !passed {
			continue
		}
		if got := collect()[hostedmetrics.ClickHouseNearMemoryCapName+"{}"]; got != 1 {
			t.Fatalf("%s = %d at cap %d, want 1; cells %v", hostedmetrics.ClickHouseNearMemoryCapName, got, capBytes, collect())
		}
		return
	}
	t.Fatal("the statement never passed under any cap up to 256 MiB")
}

// Against a real server, ACR_PROJECTOR_STATEMENT_MAX_MEMORY_BYTES reaches every
// statement the production projector client sends, and the open logs it.
func TestOpenProjectorClickHouseSendsTheOverriddenStatementMemoryCap(t *testing.T) {
	dsn := chserver.StartDSN(t, context.Background())
	var logs strings.Builder
	cfg := config.ProjectorConfig{ClickHouseDSN: dsn, ClickHouseMaxBytesToRead: 256 << 20, StatementMaxMemoryBytes: 4 << 30, StatementMaxMemoryBytesSet: true}
	client, err := openProjectorClickHouse(cfg, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.close() })
	rows, err := client.query.Query(context.Background(), "SELECT toString(getSetting('max_memory_usage'))", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var value string
	if !rows.Next() {
		t.Fatalf("no row (err=%v)", rows.Err())
	}
	if err := rows.Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "4294967296" {
		t.Fatalf("server saw max_memory_usage %s through the projector client, want 4294967296 (the override)", value)
	}
	if !strings.Contains(logs.String(), "max_memory_usage_bytes=4294967296") || !strings.Contains(logs.String(), "source="+config.EnvProjectorStatementMaxMemory) {
		t.Fatalf("open did not log the effective cap and its source: %q", logs.String())
	}
}
