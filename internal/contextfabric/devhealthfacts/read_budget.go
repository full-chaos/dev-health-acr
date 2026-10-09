package devhealthfacts

import (
	"context"
	"errors"
	"fmt"
	"sync"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// ReadStats is what the server reported it read for the statements of one
// query scope (progress packets: read rows and bytes, summed).
type ReadStats struct {
	mu    sync.Mutex
	rows  uint64
	bytes uint64
}

func (s *ReadStats) add(rows, bytes uint64) {
	s.mu.Lock()
	s.rows += rows
	s.bytes += bytes
	s.mu.Unlock()
}

// Snapshot returns the rows and bytes read so far.
func (s *ReadStats) Snapshot() (rows, bytes uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows, s.bytes
}

type readStatsKey struct{}

func contextWithReadStats(ctx context.Context, stats *ReadStats) context.Context {
	return context.WithValue(ctx, readStatsKey{}, stats)
}

func readStatsFromContext(ctx context.Context) *ReadStats {
	stats, _ := ctx.Value(readStatsKey{}).(*ReadStats)
	return stats
}

// BudgetExceededError is a ClickHouse read-budget refusal (code 307 bytes or
// 158 rows) that carries the measured facts: the configured byte cap and the
// rows and bytes the server had read when it refused. Only closed numeric
// values are carried; the server's exception text (query fragments) stays in
// Cause and never reaches a caller-visible string.
type BudgetExceededError struct {
	Code      int32
	CapBytes  uint64
	ReadRows  uint64
	ReadBytes uint64
	Cause     error
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("devhealthfacts: read budget exceeded (code %d)", e.Code)
}

func (e *BudgetExceededError) Unwrap() error { return e.Cause }

// budgetRefusalReason is the caller-visible text of a budget refusal: it names
// the action, the cap and what was measured, and says what to change.
func budgetRefusalReason(action string, budget *BudgetExceededError) string {
	switch {
	case budget.CapBytes > 0 && budget.ReadBytes > 0:
		return fmt.Sprintf("devhealthfacts: %s exceeded the read budget (limit %d bytes; the server had read %d rows and %d bytes); narrow the window or the subject",
			action, budget.CapBytes, budget.ReadRows, budget.ReadBytes)
	case budget.CapBytes > 0:
		return fmt.Sprintf("devhealthfacts: %s exceeded the read budget (limit %d bytes); narrow the window or the subject", action, budget.CapBytes)
	default:
		return fmt.Sprintf("devhealthfacts: %s exceeded the read budget; narrow the window or the subject", action)
	}
}

// NewMeasuredQueryClient wraps inner so every statement reports the rows and
// bytes the server read into the ReadStats on its context (when one is
// wired), and a read-budget exception comes back as a *BudgetExceededError
// carrying capBytes and what was measured. This is the one seam: no reader
// repeats it.
func NewMeasuredQueryClient(inner contextpacket.ClickHouseQueryClient, capBytes uint64) contextpacket.ClickHouseQueryClient {
	if inner == nil {
		return nil
	}
	return measuredQueryClient{inner: inner, capBytes: capBytes}
}

type measuredQueryClient struct {
	inner    contextpacket.ClickHouseQueryClient
	capBytes uint64
}

func (c measuredQueryClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	stats := readStatsFromContext(ctx)
	if stats == nil {
		stats = &ReadStats{}
	}
	ctx = clickhousedriver.Context(ctx, clickhousedriver.WithProgress(func(p *clickhousedriver.Progress) {
		stats.add(p.Rows, p.Bytes)
	}))
	rows, err := c.inner.Query(ctx, statement, bindings)
	if err != nil {
		return nil, c.classify(err, stats)
	}
	return &measuredRows{inner: rows, client: c, stats: stats}, nil
}

func (c measuredQueryClient) classify(err error, stats *ReadStats) error {
	if err == nil {
		return nil
	}
	var existing *BudgetExceededError
	if errors.As(err, &existing) {
		return err
	}
	code, exceeded := runtimeclickhouse.QueryBudgetExceededCode(err)
	if !exceeded {
		return err
	}
	rows, bytes := stats.Snapshot()
	return &BudgetExceededError{Code: code, CapBytes: c.capBytes, ReadRows: rows, ReadBytes: bytes, Cause: err}
}

type measuredRows struct {
	inner  contextpacket.ClickHouseRowScanner
	client measuredQueryClient
	stats  *ReadStats
}

func (r *measuredRows) Next() bool          { return r.inner.Next() }
func (r *measuredRows) Scan(d ...any) error { return r.client.classify(r.inner.Scan(d...), r.stats) }
func (r *measuredRows) Err() error          { return r.client.classify(r.inner.Err(), r.stats) }
func (r *measuredRows) Close() error        { return r.client.classify(r.inner.Close(), r.stats) }
