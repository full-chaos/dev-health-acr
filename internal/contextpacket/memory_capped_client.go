package contextpacket

import (
	"context"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
)

// APIStatementMaxMemoryUsage is the per-statement ClickHouse memory cap of
// every read the API binary sends. It is the single definition: the work-item
// reader settings use it too.
const APIStatementMaxMemoryUsage uint64 = 64 << 20

type statementMemoryCapKey struct{}

// StatementMemoryCapFromContext reports the cap NewMemoryCappedQueryClient put
// on ctx. It exists so a test double behind the decorator can observe the cap
// (the driver keeps its own settings unexported).
func StatementMemoryCapFromContext(ctx context.Context) (uint64, bool) {
	capBytes, ok := ctx.Value(statementMemoryCapKey{}).(uint64)
	return capBytes, ok
}

type memoryCappedQueryClient struct {
	inner    ClickHouseQueryClient
	capBytes uint64
}

// NewMemoryCappedQueryClient wraps inner so every statement is sent with
// max_memory_usage = capBytes. A SETTINGS clause inside a statement still
// overrides it. This is the one place the cap is applied: a new call site
// built on a wrapped client cannot skip it.
func NewMemoryCappedQueryClient(inner ClickHouseQueryClient, capBytes uint64) ClickHouseQueryClient {
	if inner == nil || capBytes == 0 {
		return inner
	}
	return memoryCappedQueryClient{inner: inner, capBytes: capBytes}
}

func (c memoryCappedQueryClient) Query(ctx context.Context, statement string, bindings []ClickHouseBinding) (ClickHouseRowScanner, error) {
	ctx = context.WithValue(ctx, statementMemoryCapKey{}, c.capBytes)
	ctx = clickhousedriver.Context(ctx, clickhousedriver.WithSettings(clickhousedriver.Settings{"max_memory_usage": c.capBytes}))
	return c.inner.Query(ctx, statement, bindings)
}
