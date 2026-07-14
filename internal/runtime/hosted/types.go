package hosted

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
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
	Dependencies api.Dependencies
	closeOnce    sync.Once
	closeErr     error
	closers      []func() error
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		var errs []error
		for _, closeResource := range r.closers {
			if closeResource != nil {
				errs = append(errs, closeResource())
			}
		}
		r.closeErr = errors.Join(errs...)
	})
	return r.closeErr
}

type postgresComponents struct {
	credentials storage.CredentialStore
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
