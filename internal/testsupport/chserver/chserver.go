// Package chserver starts a real ClickHouse server for a test. A container
// that does not start fails the test; nothing here skips.
package chserver

import (
	"context"
	"net"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartDSN starts the pinned ClickHouse image and returns a DSN for the
// user "acr" once the server answers a ping.
func StartDSN(tb testing.TB, ctx context.Context) string {
	tb.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		tb.Fatalf("start ClickHouse container: %v", err)
	}
	tb.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			tb.Errorf("terminate ClickHouse container: %v", err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		tb.Fatal(err)
	}
	addr := net.JoinHostPort(host, port.Port())
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second})
	if err != nil {
		tb.Fatalf("open native ClickHouse connection: %v", err)
	}
	defer func() { _ = direct.Close() }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pingErr := direct.Ping(ctx)
		if pingErr == nil {
			break
		}
		if time.Now().After(deadline) {
			tb.Fatalf("clickhouse not ready: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "clickhouse://acr:acr@" + addr + "/default"
}
