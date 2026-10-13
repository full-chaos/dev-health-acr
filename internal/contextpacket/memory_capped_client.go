package contextpacket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
)

// APIStatementMaxMemoryUsage is the per-statement ClickHouse memory default of
// every read the API binary sends. It is a runaway guard, not a tuning knob:
// it sits above every live statement seen (prod 104.48 MiB org investment mix
// after rev 84; CI scope expander 148 MB at 300k work items) and is tightened
// per statement by the lanes that cut those statements.
//
// memory-limit-measured: 148 MB 2026-10-13 CI scope expander at 300k work items (prod peak 104.48 MiB org investment mix)
const APIStatementMaxMemoryUsage uint64 = 256 << 20

// NearCapRatio is the share of a statement's cap above which the observer is told.
const NearCapRatio = 0.5

type statementMemoryCapKey struct{}

// StatementMemoryCapFromContext reports the cap NewMemoryCappedQueryClient put
// on ctx. It exists so a test double behind the decorator can observe the cap
// (the driver keeps its own settings unexported).
func StatementMemoryCapFromContext(ctx context.Context) (uint64, bool) {
	capBytes, ok := ctx.Value(statementMemoryCapKey{}).(uint64)
	return capBytes, ok
}

// MemoryNearCap describes a finished statement that used more than the near-cap
// share of its memory cap.
type MemoryNearCap struct {
	Statement string
	PeakBytes uint64
	CapBytes  uint64
}

// MemoryNearCapObserver is called once per finished statement above the
// near-cap share. It must not block.
type MemoryNearCapObserver func(context.Context, MemoryNearCap)

// MemoryPeakUnobservedObserver is called once per finished statement for which
// the server sent no peak-memory value, so a silent near-cap check is visible.
type MemoryPeakUnobservedObserver func(ctx context.Context, statement string)

// WithMemoryPeakUnobservedObserver reports statements that finish with no peak
// memory value from the server.
func WithMemoryPeakUnobservedObserver(observer MemoryPeakUnobservedObserver) MemoryCapOption {
	return func(c *memoryCappedQueryClient) { c.unobserved = observer }
}

// MemoryCapOption configures NewMemoryCappedQueryClient.
type MemoryCapOption func(*memoryCappedQueryClient)

// WithMemoryNearCapObserver reports statements that finish above the near-cap
// share of the cap, from the peak memory the server sends in its profile events.
func WithMemoryNearCapObserver(observer MemoryNearCapObserver) MemoryCapOption {
	return func(c *memoryCappedQueryClient) { c.observer = observer }
}

// WithMemoryNearCapRatio replaces NearCapRatio (tests only).
func WithMemoryNearCapRatio(ratio float64) MemoryCapOption {
	return func(c *memoryCappedQueryClient) { c.ratio = ratio }
}

type memoryCappedQueryClient struct {
	inner      ClickHouseQueryClient
	capBytes   uint64
	observer   MemoryNearCapObserver
	unobserved MemoryPeakUnobservedObserver
	ratio      float64
}

// NewMemoryCappedQueryClient wraps inner so every statement is sent with
// max_memory_usage = capBytes. A SETTINGS clause inside a statement still
// overrides it. This is the one place the default is applied: a new call site
// built on a wrapped client cannot skip it.
func NewMemoryCappedQueryClient(inner ClickHouseQueryClient, capBytes uint64, options ...MemoryCapOption) ClickHouseQueryClient {
	if inner == nil || capBytes == 0 {
		return inner
	}
	client := memoryCappedQueryClient{inner: inner, capBytes: capBytes, ratio: NearCapRatio}
	for _, option := range options {
		option(&client)
	}
	return client
}

func (c memoryCappedQueryClient) Query(ctx context.Context, statement string, bindings []ClickHouseBinding) (ClickHouseRowScanner, error) {
	ctx = context.WithValue(ctx, statementMemoryCapKey{}, c.capBytes)
	var peak atomic.Uint64
	driverOptions := []clickhousedriver.QueryOption{clickhousedriver.WithSettings(clickhousedriver.Settings{"max_memory_usage": c.capBytes})}
	if c.observer != nil || c.unobserved != nil {
		driverOptions = append(driverOptions, clickhousedriver.WithProfileEvents(func(events []clickhousedriver.ProfileEvent) {
			for _, event := range events {
				if event.Name != "MemoryTrackerPeakUsage" || event.Value <= 0 {
					continue
				}
				for value := uint64(event.Value); ; {
					current := peak.Load()
					if value <= current || peak.CompareAndSwap(current, value) {
						break
					}
				}
			}
		}))
	}
	rows, err := c.inner.Query(clickhousedriver.Context(ctx, driverOptions...), statement, bindings)
	if err != nil || rows == nil || (c.observer == nil && c.unobserved == nil) {
		return rows, err
	}
	return &memoryObservedRows{ClickHouseRowScanner: rows, ctx: ctx, statement: statement, client: c, peak: &peak}, nil
}

var statementMemorySetting = regexp.MustCompile(`(?i)\bmax_memory_usage\s*=\s*([0-9]+)`)

// effectiveStatementCap is the cap the server enforces for statement: its own
// SETTINGS max_memory_usage when present (it wins over the client default),
// otherwise fallback.
func effectiveStatementCap(statement string, fallback uint64) uint64 {
	matches := statementMemorySetting.FindAllStringSubmatch(statement, -1)
	if len(matches) == 0 {
		return fallback
	}
	value, err := strconv.ParseUint(matches[len(matches)-1][1], 10, 64)
	if err != nil || value == 0 {
		return fallback
	}
	return value
}

type memoryObservedRows struct {
	ClickHouseRowScanner
	ctx       context.Context
	statement string
	client    memoryCappedQueryClient
	peak      *atomic.Uint64
	reported  atomic.Bool
}

func (r *memoryObservedRows) Close() error {
	err := r.ClickHouseRowScanner.Close()
	if r.reported.CompareAndSwap(false, true) {
		peak := r.peak.Load()
		if peak == 0 && r.client.unobserved != nil {
			r.client.unobserved(r.ctx, r.statement)
		}
		capBytes := effectiveStatementCap(r.statement, r.client.capBytes)
		if r.client.observer != nil && float64(peak) > r.client.ratio*float64(capBytes) {
			r.client.observer(r.ctx, MemoryNearCap{Statement: r.statement, PeakBytes: peak, CapBytes: capBytes})
		}
	}
	return err
}

// NewSlogMemoryNearCapObserver logs a WARN for each statement above the
// near-cap share and calls count (when set). The statement is named by a hash
// and its first words; statements are templates, bindings are never logged.
func NewSlogMemoryNearCapObserver(logger *slog.Logger, count func(context.Context)) MemoryNearCapObserver {
	return func(ctx context.Context, near MemoryNearCap) {
		if count != nil {
			count(ctx)
		}
		if logger == nil {
			return
		}
		sum := sha256.Sum256([]byte(near.Statement))
		head := strings.Join(strings.Fields(near.Statement), " ")
		if len(head) > 80 {
			head = head[:80]
		}
		logger.WarnContext(ctx, "clickhouse statement memory near cap",
			"statement_sha", hex.EncodeToString(sum[:6]), "statement_head", head,
			"peak_bytes", near.PeakBytes, "cap_bytes", near.CapBytes)
	}
}

// NewSlogMemoryPeakUnobservedObserver counts every statement that finished with
// no peak-memory value and logs one WARN per statement (by hash) per process.
func NewSlogMemoryPeakUnobservedObserver(logger *slog.Logger, count func(context.Context)) MemoryPeakUnobservedObserver {
	var warned sync.Map
	return func(ctx context.Context, statement string) {
		if count != nil {
			count(ctx)
		}
		if logger == nil {
			return
		}
		sum := sha256.Sum256([]byte(statement))
		key := hex.EncodeToString(sum[:6])
		if _, seen := warned.LoadOrStore(key, struct{}{}); seen {
			return
		}
		logger.WarnContext(ctx, "clickhouse statement sent no peak memory value; the near-cap check is blind for it", "statement_sha", key)
	}
}
