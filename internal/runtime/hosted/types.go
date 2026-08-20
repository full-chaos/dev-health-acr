package hosted

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgclarification"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgstructureselection"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type Options struct {
	ServiceVersion string
	Logger         *slog.Logger
	Now            func() time.Time
	// ModelRuntimeOverride, when set, REPLACES the env-driven deployment
	// model runtime buildContextFabricInvestigator would otherwise build
	// from ACR_CONTEXT_FABRIC_MODEL* -- everything else in composition
	// (graph, canonical facts, receipt sink, reuse wiring) is unaffected.
	// nil (the zero value) for every real caller, including
	// cmd/acr-api/main.go -- this exists solely for CHAOS-3742's
	// file-exchange diagnostic arm (a test-only ModelRuntime that swaps
	// the generative stage while measuring the identical pipeline), never
	// referenced by any production composition path.
	ModelRuntimeOverride contextfabric.ModelRuntime
	// RawSignalObserver (CHAOS-3858, measurement-only), when set, is
	// threaded onto the graph adapter's Config.RawSignalObserver -- see
	// that field's own doc comment and graphrank.ResolveDeps.RawSignalObserver
	// for the full scope (post-authorization only, never a production
	// consumer). nil (the zero value) for every real caller, same
	// discipline as ModelRuntimeOverride above.
	RawSignalObserver graphrank.RawSignalObserver
	// ResolutionTracer (CHAOS-3742 acceptance debt follow-up), when set,
	// REPLACES the default graphrank.NewSlogResolutionTracer(Logger) this
	// package otherwise wires unconditionally (buildContextFabricGraphReader) --
	// letting a caller capture ResolutionTraceEvent values in-process (e.g.
	// the "evidence_round" shadow stage's ShadowOutcome, which reports
	// kindInsensitivityProof's own verdict) instead of only reaching them by
	// parsing slog output at Debug level. nil (the zero value) for every
	// real caller, including cmd/acr-api/main.go -- same
	// test-only-hook discipline as ModelRuntimeOverride/RawSignalObserver
	// above; a nil tracer here changes nothing (the SlogResolutionTracer
	// default still wires exactly as before).
	ResolutionTracer graphrank.ResolutionTracer
}

type Runtime struct {
	Dependencies      api.Dependencies
	closeMu           sync.Mutex
	closeErr          error
	closers           []func() error
	usageTelemetry    *auth.UsageTelemetry
	clarificationSink *pgclarification.Sink
	// structureSelectionSink (CHAOS-3927 P4) is clarificationSink's own
	// structure-offer twin -- SAME pool, SAME worker-timeout-before-
	// postgresClose discipline, see Close's own comment for why both must
	// get an unconditional, independent shutdown attempt.
	structureSelectionSink *pgstructureselection.Sink
	postgresClose          func() error
	independentClosed      bool
	postgresClosed         bool
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	if r.postgresClosed {
		return r.closeErr
	}
	// CHAOS-3859 sol review F6: usageTelemetry and clarificationSink both
	// write through the SAME Postgres pool postgresClose() tears down
	// below, so BOTH must get an unconditional, independent shutdown
	// attempt before that happens -- one timing out must never skip the
	// other's attempt entirely. (The bug this fixes: usageTelemetry timing
	// out used to return immediately, before clarificationSink.Close was
	// ever called at all.) A worker that does not finish draining in time
	// might still be mid-write through the pool -- closing it now would
	// race that write -- so a timeout from EITHER one skips postgresClose
	// this round (leaving postgresClosed false so a LATER Close() call
	// retries it), exactly the protection this function already gave
	// usageTelemetry alone. clarificationSink.Close cancels its own
	// worker's context on ITS timeout (see Sink.Close's doc comment), so
	// even the "we gave up waiting" case degrades safely instead of
	// leaking a goroutine that might still touch the pool later.
	// usageErr and sinkErr are THIS round's worker-timeout errors only --
	// deliberately never assigned into the persistent r.closeErr field. A
	// timed-out round leaves postgresClosed false so a LATER Close() call
	// retries both workers; if that retry succeeds, it must report clean
	// rather than replaying a stale error from a round that already ended.
	// (This mirrors the pre-F6 code, which joined its own timeout err only
	// into the transient return value, never into r.closeErr.)
	var usageErr, sinkErr, structureSinkErr error
	var workerTimedOut bool
	if r.usageTelemetry != nil {
		if err := r.usageTelemetry.Close(); errors.Is(err, auth.ErrUsageTelemetryShutdownTimeout) {
			usageErr = err
			workerTimedOut = true
		}
	}
	if r.clarificationSink != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		sinkErr = r.clarificationSink.Close(closeCtx)
		cancel()
		if sinkErr != nil {
			workerTimedOut = true
		}
	}
	// CHAOS-3927 P4: structureSelectionSink is a THIRD worker writing
	// through the identical pool, so it gets the SAME unconditional,
	// independent shutdown attempt as clarificationSink immediately above
	// -- never skipped just because usageTelemetry or clarificationSink
	// already timed out this round.
	if r.structureSelectionSink != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		structureSinkErr = r.structureSelectionSink.Close(closeCtx)
		cancel()
		if structureSinkErr != nil {
			workerTimedOut = true
		}
	}
	// The OTHER, unrelated closers (clickhouse, entitlement, ...) share no
	// worker with the pool, so they run regardless of the outcome above.
	// closeIndependentLocked is itself idempotent, so persisting its result
	// across retries is safe (unlike usageErr/sinkErr/structureSinkErr above).
	r.closeErr = errors.Join(r.closeErr, r.closeIndependentLocked())
	if workerTimedOut {
		return errors.Join(r.closeErr, usageErr, sinkErr, structureSinkErr)
	}
	if r.postgresClose != nil {
		r.closeErr = errors.Join(r.closeErr, r.postgresClose())
	}
	r.postgresClosed = true
	return r.closeErr
}

func (r *Runtime) closeIndependentLocked() error {
	if r.independentClosed {
		return nil
	}
	r.independentClosed = true
	var errs []error
	for _, closeResource := range r.closers {
		if closeResource != nil {
			errs = append(errs, closeResource())
		}
	}
	return errors.Join(errs...)
}

type postgresComponents struct {
	credentials *storage.CredentialLifecycle
	devices     storage.DeviceAuthorizationStore
	audit       storage.AuditStore
	packets     storage.PacketStore
	episodes    storage.EpisodeStore
	// db is the raw pool, exposed so hosted.open can build additional
	// caller-owned adapters (e.g. pginvestigation.Store) on it directly --
	// this package never opens a second PostgreSQL connection.
	db    *sql.DB
	check func(context.Context) error
	close func() error
}

type clickHouseComponents struct {
	evidence storage.EvidenceStore
	factory  contextpacket.EvidenceStoreFactory
	// queryClient is the raw ClickHouse query boundary, exposed so
	// hosted.open can build additional caller-owned adapters (e.g.
	// devhealthfacts providers) on it directly -- this package never opens
	// a second ClickHouse connection.
	queryClient contextpacket.ClickHouseQueryClient
	check       func(context.Context) error
	close       func() error
}

type entitlementChecker interface {
	api.EntitlementProvider
	Check(context.Context) error
	Close() error
}

type clickHouseOpenRequest struct {
	config            config.Config
	assemblyObserver  contextpacket.AssemblyObserver
	expansionObserver contextpacket.EvidenceExpansionObserver
}

type episodeServiceRequest struct {
	postgres postgresComponents
	options  Options
	hooks    observability.Hooks
}

type componentFactories struct {
	openPostgres   func(context.Context, config.Config, *slog.Logger) (postgresComponents, error)
	openClickHouse func(context.Context, clickHouseOpenRequest) (clickHouseComponents, error)
	newEntitlement func(config.Config) (entitlementChecker, error)
	newEpisode     func(episodeServiceRequest) (api.EpisodeCreator, error)
}

type buildRequest struct {
	config    config.Config
	options   Options
	factories componentFactories
}
