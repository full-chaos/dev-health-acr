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
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgclarification"
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
}

type Runtime struct {
	Dependencies      api.Dependencies
	closeMu           sync.Mutex
	closeErr          error
	closers           []func() error
	usageTelemetry    *auth.UsageTelemetry
	clarificationSink *pgclarification.Sink
	postgresClose     func() error
	independentClosed bool
	postgresClosed    bool
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
	if r.usageTelemetry != nil {
		if err := r.usageTelemetry.Close(); errors.Is(err, auth.ErrUsageTelemetryShutdownTimeout) {
			r.closeErr = errors.Join(r.closeErr, r.closeIndependentLocked())
			return errors.Join(r.closeErr, err)
		}
	}
	// CHAOS-3859: same ordering discipline as usageTelemetry immediately
	// above -- stop the worker before postgresClose() tears down the pool
	// it writes through. A bounded 5s shutdown timeout, not an unbounded
	// wait: this is a best-effort capture sink, not a delivery-guaranteed
	// one, and Close's own doc comment says so.
	if r.clarificationSink != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		closeErr := r.clarificationSink.Close(closeCtx)
		cancel()
		r.closeErr = errors.Join(r.closeErr, closeErr)
	}
	r.closeErr = errors.Join(r.closeErr, r.closeIndependentLocked())
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
