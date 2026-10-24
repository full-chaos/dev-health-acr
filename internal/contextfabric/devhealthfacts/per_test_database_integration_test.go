package devhealthfacts_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

var (
	perTestDatabaseSeq      atomic.Int64
	perTestDatabasesMade    atomic.Int64
	perTestDatabasesDropped atomic.Int64
)

// newCHAOS3780IntegrationClient returns clients on the package's one shared
// ClickHouse container, inside a database of its own that is dropped when the
// test ends. A test that reads system tables, runs SYSTEM or KILL statements or
// measures memory or rows read needs a server to itself and uses
// newDedicatedClickHouseIntegrationClient; sharedDatabaseGuard_test enforces it.
func newCHAOS3780IntegrationClient(t *testing.T, ctx context.Context) (query *runtimeclickhouse.Client, direct clickhousedriver.Conn) {
	t.Helper()
	return newCHAOS3780IntegrationClientWithOptions(t, ctx, nil)
}

// newCHAOS3780IntegrationClientWithOptions is newCHAOS3780IntegrationClient with
// tune applied to the production query client's Options.
func newCHAOS3780IntegrationClientWithOptions(t *testing.T, ctx context.Context, tune func(*runtimeclickhouse.Options)) (query *runtimeclickhouse.Client, direct clickhousedriver.Conn) {
	t.Helper()
	_, admin := sharedClickHouseFixture(t)
	addr := sharedClickHouseAddr
	database := fmt.Sprintf("p%d", perTestDatabaseSeq.Add(1))
	if err := admin.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create per-test database %s: %v", database, err)
	}
	perTestDatabasesMade.Add(1)
	t.Cleanup(func() {
		if err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" SYNC"); err != nil {
			t.Errorf("drop per-test database %s: %v", database, err)
			return
		}
		perTestDatabasesDropped.Add(1)
	})

	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: database, Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil {
			t.Errorf("close native ClickHouse connection: %v", err)
		}
	})
	query, err = runtimeclickhouse.NewClickHouseQueryClientWithOptions(perTestQueryOptions(addr, database, tune))
	if err != nil {
		t.Fatalf("open production ClickHouse query client: %v", err)
	}
	t.Cleanup(func() {
		if err := query.Close(); err != nil {
			t.Errorf("close production ClickHouse query client: %v", err)
		}
	})
	return query, direct
}

// perTestQueryOptions is the production query client's Options for a per-test
// database on the shared server: its DSN names that database, and the caller's
// tune hook is applied last.
func perTestQueryOptions(addr, database string, tune func(*runtimeclickhouse.Options)) runtimeclickhouse.Options {
	options := runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second,
	}
	if tune != nil {
		tune(&options)
	}
	return options
}
