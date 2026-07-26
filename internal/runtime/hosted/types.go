package hosted

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type Options struct {
	ServiceVersion string
	Logger         *slog.Logger
	Now            func() time.Time
}

type Runtime struct {
	Dependencies      api.Dependencies
	closeMu           sync.Mutex
	closeErr          error
	closers           []func() error
	usageTelemetry    *auth.UsageTelemetry
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
	check       func(context.Context) error
	close       func() error
}

type clickHouseComponents struct {
	evidence storage.EvidenceStore
	factory  contextpacket.EvidenceStoreFactory
	check    func(context.Context) error
	close    func() error
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
