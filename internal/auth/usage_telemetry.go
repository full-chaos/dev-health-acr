package auth

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	defaultUsageQueueCapacity   = 256
	maximumUsageQueueCapacity   = 4096
	defaultUsageFlushInterval   = time.Second
	defaultUsageDeliveryTimeout = time.Second
	defaultUsageShutdownTimeout = 5 * time.Second
)

var (
	ErrUsageTelemetryClosed          = errors.New("credential usage telemetry is closed")
	ErrUsageTelemetryShutdownTimeout = errors.New("credential usage telemetry shutdown timed out")
)

// UsageRecord carries the small, non-secret subset of an authenticated request
// that is eventually persisted as best-effort credential telemetry.
type UsageRecord struct {
	OrgID        string
	CredentialID string
	ActorType    string
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Metadata     map[string]any
	ClientIP     string
	UserAgent    string
	RequestID    string
	UsedAt       time.Time
}

// UsageTelemetryOptions bounds every in-memory stage of the usage pipeline.
// Successful-use records are intentionally lossy during a process crash or a
// full queue; authorization and credential lifecycle mutation never depend on
// delivery of this best-effort telemetry.
type UsageTelemetryOptions struct {
	QueueCapacity   int
	FlushInterval   time.Duration
	DeliveryTimeout time.Duration
	ShutdownTimeout time.Duration
	Logger          *slog.Logger
}

// UsageTelemetryStats exposes low-cardinality counters for queue saturation
// and delivery health. Callers can export these values through their metrics
// pipeline without attaching credential, organization, or request identifiers.
type UsageTelemetryStats struct {
	QueueCapacity    int
	Enqueued         int64
	Coalesced        int64
	Dropped          int64
	Delivered        int64
	DeliveryFailures int64
	ShutdownDropped  int64
}

type usageMetrics struct {
	enqueued         atomic.Int64
	coalesced        atomic.Int64
	dropped          atomic.Int64
	delivered        atomic.Int64
	deliveryFailures atomic.Int64
	shutdownDropped  atomic.Int64
}

// UsageTelemetry is a single-worker, lifecycle-owned usage coalescer. Its
// queue and pending map are both bounded by QueueCapacity; it never creates a
// goroutine per request or per write.
type UsageTelemetry struct {
	store storage.CredentialStore
	audit storage.AuditStore

	queue           chan UsageRecord
	flushRequests   chan flushRequest
	stop            chan struct{}
	done            chan struct{}
	workerContext   context.Context
	cancelWorker    context.CancelFunc
	queueCapacity   int
	flushInterval   time.Duration
	deliveryTimeout time.Duration
	shutdownTimeout time.Duration
	logger          *slog.Logger
	metrics         usageMetrics
	lifecycleMu     sync.RWMutex
	state           usageTelemetryState
	shutdownBy      time.Time
	terminalErr     error
}

func NewUsageTelemetry(store storage.CredentialStore, audit storage.AuditStore, options UsageTelemetryOptions) (*UsageTelemetry, error) {
	if storage.IsNil(store) {
		return nil, errors.New("credential usage store is required")
	}
	if storage.IsNil(audit) && audit != nil {
		return nil, errors.New("credential usage audit store must not be typed nil")
	}
	if options.QueueCapacity == 0 {
		options.QueueCapacity = defaultUsageQueueCapacity
	}
	if options.QueueCapacity < 1 || options.QueueCapacity > maximumUsageQueueCapacity {
		return nil, errors.New("credential usage queue capacity is invalid")
	}
	if options.FlushInterval <= 0 {
		options.FlushInterval = defaultUsageFlushInterval
	}
	if options.DeliveryTimeout <= 0 {
		options.DeliveryTimeout = defaultUsageDeliveryTimeout
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultUsageShutdownTimeout
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	workerContext, cancelWorker := context.WithCancel(context.Background())
	telemetry := &UsageTelemetry{
		store: store, audit: audit,
		queue: make(chan UsageRecord, options.QueueCapacity), flushRequests: make(chan flushRequest, 1),
		stop: make(chan struct{}), done: make(chan struct{}), workerContext: workerContext, cancelWorker: cancelWorker,
		queueCapacity: options.QueueCapacity, flushInterval: options.FlushInterval,
		deliveryTimeout: options.DeliveryTimeout, shutdownTimeout: options.ShutdownTimeout, logger: options.Logger,
	}
	go telemetry.run()
	return telemetry, nil
}

// Enqueue never waits for storage or a worker. A full queue drops the newest
// successful-use record and increments a low-cardinality operational counter.
func (u *UsageTelemetry) Enqueue(record UsageRecord) {
	if u == nil || record.OrgID == "" || usageActorID(record) == "" || record.UsedAt.IsZero() {
		return
	}
	u.lifecycleMu.RLock()
	defer u.lifecycleMu.RUnlock()
	if u.state != usageTelemetryOpen {
		u.metrics.dropped.Add(1)
		return
	}
	select {
	case u.queue <- record:
		u.metrics.enqueued.Add(1)
	default:
		u.metrics.dropped.Add(1)
		u.logger.Warn("credential usage telemetry dropped", "reason", "queue_full")
	}
}

// Flush synchronously drains the currently queued work. It is intended for
// lifecycle tests and controlled shutdown paths; request handlers use Enqueue.
func (u *UsageTelemetry) Flush(ctx context.Context) error {
	if u == nil {
		return ErrUsageTelemetryClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	u.lifecycleMu.RLock()
	open := u.state == usageTelemetryOpen
	u.lifecycleMu.RUnlock()
	if !open {
		return ErrUsageTelemetryClosed
	}
	done := make(chan error, 1)
	request := flushRequest{context: ctx, done: done}
	select {
	case <-u.done:
		return ErrUsageTelemetryClosed
	case <-u.stop:
		return ErrUsageTelemetryClosed
	case <-ctx.Done():
		return ctx.Err()
	case u.flushRequests <- request:
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-u.done:
		return ErrUsageTelemetryClosed
	}
}

func (u *UsageTelemetry) Stats() UsageTelemetryStats {
	if u == nil {
		return UsageTelemetryStats{}
	}
	return UsageTelemetryStats{
		QueueCapacity: u.queueCapacity, Enqueued: u.metrics.enqueued.Load(), Coalesced: u.metrics.coalesced.Load(),
		Dropped: u.metrics.dropped.Load(), Delivered: u.metrics.delivered.Load(),
		DeliveryFailures: u.metrics.deliveryFailures.Load(), ShutdownDropped: u.metrics.shutdownDropped.Load(),
	}
}
