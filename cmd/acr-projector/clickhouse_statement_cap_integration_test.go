package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/chserver"
)

// Against a real server, a statement sent through the production projector
// client carries the 128 MiB memory cap, and a statement's own SETTINGS wins.
func TestOpenProjectorClickHouseSendsTheStatementMemoryCapToTheServer(t *testing.T) {
	dsn := chserver.StartDSN(t, context.Background())
	client, err := openProjectorClickHouse(config.ProjectorConfig{ClickHouseDSN: dsn, ClickHouseMaxBytesToRead: 256 << 20}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
