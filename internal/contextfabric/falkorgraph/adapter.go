package falkorgraph

import (
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Adapter implements contextfabric.ProjectionBackend and
// contextfabric.GraphReader on FalkorDB. See the package doc comment
// (config.go) for the client boundary this adapter maintains, and
// docs/design/context-fabric-falkordb-adapter.md / ADR 0009 for the
// tenancy, schema, and retrieval design this implements.
type Adapter struct {
	api    conn
	config Config
	now    func() time.Time

	bootstrapMu   sync.RWMutex
	bootstrapDone map[string]bool
}

func New(config Config) (*Adapter, error) {
	client, err := newSDKAPI(config)
	if err != nil {
		return nil, err
	}
	return newWithAPI(config, client)
}

func newWithAPI(config Config, client conn) (*Adapter, error) {
	if client == nil {
		return nil, errAdapterRequiresConn
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 3
	}
	if config.MaxResults == 0 {
		config.MaxResults = 25
	}
	if config.PoolSize == 0 {
		config.PoolSize = 10
	}
	if strings.TrimSpace(config.GraphPrefix) == "" {
		config.GraphPrefix = "acr-cf"
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Adapter{api: client, config: config, now: time.Now, bootstrapDone: make(map[string]bool)}, nil
}

var _ contextfabric.ProjectionBackend = (*Adapter)(nil)
var _ contextfabric.GraphReader = (*Adapter)(nil)
