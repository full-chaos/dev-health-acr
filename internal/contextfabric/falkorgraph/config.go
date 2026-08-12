// Package falkorgraph implements the Context Fabric graph backend
// (contextfabric.ProjectionBackend + contextfabric.GraphReader) on FalkorDB,
// a self-hosted, SSPL-licensed graph database consumed here as a deployment
// dependency (a container image), not as linked code -- the same posture as
// this repository's Postgres and ClickHouse dependencies. See ADR 0009 and
// docs/design/context-fabric-falkordb-adapter.md.
//
// Client boundary: github.com/FalkorDB/falkordb-go/v2 is used ONLY as a
// compact-protocol result decoder (sdkConn in client.go), never through its
// high-level Graph.Query/CallProcedure methods. That client has no
// context.Context support anywhere in its high-level API (every call goes
// through a package-level context.Background()), ToString panics on several
// common Go scalar types, CallProcedure silently returns empty results
// instead of an error when called with arguments, and GraphSchema has no
// mutex despite being mutated during result parsing -- all independently
// verified against the pinned v2.1.0. sdkConn calls db.Conn.Do(ctx, ...)
// directly (a real go-redis call that DOES accept a context) and only reuses
// the client's exported, I/O-free QueryResultNew decoder and
// BuildParamsHeader helper. Do not "simplify" this back to the client's
// high-level API -- that would reintroduce every defect above.
package falkorgraph

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	acrconfig "github.com/full-chaos/dev-health-acr/internal/config"
)

const (
	SDKModule  = "github.com/FalkorDB/falkordb-go/v2"
	SDKVersion = "v2.1.0"
)

var (
	ErrNotFound     = errors.New("context fabric graph record not found")
	ErrUnauthorized = errors.New("context fabric graph request unauthorized")
	ErrRateLimited  = errors.New("context fabric graph request rate limited")
	// ErrConstraintViolation classifies a FalkorDB unique-constraint
	// rejection (verified error text: "unique constraint violation on node
	// of type X"). FalkorDB's constraint violation messages carry no
	// property name or value, so the adapter must already know which
	// constraint it created to say anything more specific.
	ErrConstraintViolation = errors.New("context fabric graph unique constraint violation")
	// errAlreadyExists classifies FalkorDB's "already indexed" /
	// "already exists" schema-object errors -- index and constraint
	// creation are NOT idempotent server-side (verified), so bootstrap
	// treats this as success for the concurrent-bootstrap race rather than
	// as a failure.
	errAlreadyExists               = errors.New("context fabric graph schema object already exists")
	errConstraintBootstrapFailed   = errors.New("context fabric graph constraint bootstrap failed")
	errConstraintBootstrapTimedOut = errors.New("context fabric graph constraint bootstrap timed out waiting for OPERATIONAL status")
	errAdapterRequiresConn         = errors.New("falkordb graph connection is required")
)

// Config configures the FalkorDB adapter. Mirrors zepgraph.Config's shape
// and validation posture so the two backends are operationally
// interchangeable at the config layer.
type Config struct {
	Addr           string // host:port, e.g. "falkordb:6379"
	Password       string
	TLS            bool
	GraphPrefix    string
	RequestTimeout time.Duration
	MaxAttempts    uint
	MaxResults     int
	PoolSize       int
	AllowInsecure  bool // permit TLS=false outside development; see validate()
	// Telemetry is optional (nil-safe), same contract as
	// zepgraph.GraphTelemetry.
	Telemetry GraphTelemetry
}

// GraphTelemetry mirrors zepgraph.GraphTelemetry's contract exactly, so a
// single telemetry implementation serves either backend.
type GraphTelemetry interface {
	RecordObservationTraversalDegraded(ctx context.Context, orgID string, count int)
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return errors.New("falkordb address is required")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return errors.New("falkordb address must be host:port")
	}
	if !c.TLS && !c.AllowInsecure {
		return errors.New("falkordb connection must use TLS")
	}
	if c.RequestTimeout < time.Second || c.RequestTimeout > 2*time.Minute {
		return errors.New("falkordb request timeout must be between one second and two minutes")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 5 {
		return errors.New("falkordb max attempts must be between one and five")
	}
	if c.MaxResults < 1 || c.MaxResults > 50 {
		return errors.New("falkordb max results must be between one and fifty")
	}
	if strings.TrimSpace(c.GraphPrefix) == "" || len(c.GraphPrefix) > 32 {
		return errors.New("falkordb graph prefix is required and must be bounded")
	}
	if c.PoolSize < 1 || c.PoolSize > 100 {
		return errors.New("falkordb pool size must be between one and one hundred")
	}
	return nil
}

// Environment variable names for ConfigFromEnv, matching the ACR_<COMPONENT>_
// naming and KEY/KEY_FILE secret convention used by internal/config, and the
// contract this repository's compose/Helm wiring targets (see the design
// doc's environment table).
const (
	EnvAddr           = "ACR_CONTEXT_FABRIC_FALKOR_ADDR"
	EnvPassword       = "ACR_CONTEXT_FABRIC_FALKOR_PASSWORD"
	EnvTLS            = "ACR_CONTEXT_FABRIC_FALKOR_TLS"
	EnvGraphPrefix    = "ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX"
	EnvRequestTimeout = "ACR_CONTEXT_FABRIC_FALKOR_REQUEST_TIMEOUT"
	EnvMaxAttempts    = "ACR_CONTEXT_FABRIC_FALKOR_MAX_ATTEMPTS"
	EnvMaxResults     = "ACR_CONTEXT_FABRIC_FALKOR_MAX_RESULTS"
	EnvPoolSize       = "ACR_CONTEXT_FABRIC_FALKOR_POOL_SIZE"
	EnvAllowInsecure  = "ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE"
)

// Configured reports whether ACR_CONTEXT_FABRIC_FALKOR_ADDR is set at all, so
// a deployment that has not opted into Context Fabric never constructs the
// adapter and never fails closed over a dependency it did not choose.
// Mirrors zepgraph.Configured.
func Configured(lookup func(string) (string, bool)) bool {
	value, ok := lookup(EnvAddr)
	return ok && strings.TrimSpace(value) != ""
}

// ConfigFromEnv builds a Config from environment lookups, following the same
// KEY/KEY_FILE secret convention as every other ACR secret
// (internal/config.SecretValue). FalkorDB needs no external credential to
// deploy locally (no API key, unlike Zep) -- ACR_CONTEXT_FABRIC_FALKOR_PASSWORD
// is optional and empty by default, matching FalkorDB's own no-auth default.
func ConfigFromEnv(lookup func(string) (string, bool)) (Config, error) {
	password, err := acrconfig.SecretValue(lookup, EnvPassword)
	if err != nil {
		return Config{}, err
	}
	timeout, err := envDuration(lookup, EnvRequestTimeout, 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxAttempts, err := envUint(lookup, EnvMaxAttempts, 3)
	if err != nil {
		return Config{}, err
	}
	maxResults, err := envInt(lookup, EnvMaxResults, 25)
	if err != nil {
		return Config{}, err
	}
	poolSize, err := envInt(lookup, EnvPoolSize, 10)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Addr:           envString(lookup, EnvAddr, ""),
		Password:       password,
		TLS:            envBool(lookup, EnvTLS, true),
		GraphPrefix:    envString(lookup, EnvGraphPrefix, "acr-cf"),
		RequestTimeout: timeout,
		MaxAttempts:    maxAttempts,
		MaxResults:     maxResults,
		PoolSize:       poolSize,
		AllowInsecure:  envBool(lookup, EnvAllowInsecure, false),
	}, nil
}

func envString(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func envBool(lookup func(string) (string, bool), key string, fallback bool) bool {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func envInt(lookup func(string) (string, bool), key string, fallback int) (int, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, errors.New(key + " must be an integer")
	}
	return parsed, nil
}

func envUint(lookup func(string) (string, bool), key string, fallback uint) (uint, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 8)
	if err != nil {
		return 0, errors.New(key + " must be a non-negative integer")
	}
	return uint(parsed), nil
}

func envDuration(lookup func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, errors.New(key + " must be a valid Go duration")
	}
	return parsed, nil
}
