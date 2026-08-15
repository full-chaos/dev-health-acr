package embedcache

import (
	"errors"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Environment variable names, following the ACR_<COMPONENT>_ naming this
// repository's context-fabric config uses throughout (embedprovider,
// falkorgraph).
const (
	// EnvEnabled turns the cache on. Unset or false means OFF: a deployment
	// that has not opted in constructs no cache and its embedder is wrapped
	// with nothing, exactly as before this ticket. Defaulting OFF matches
	// this package's own reset posture (CHAOS-3778's embedder, CHAOS-3782's
	// answer reuse, and CHAOS-3781's historical axis are all opt-in): every
	// other optional layer here is explicit-on, and an operator should be
	// able to read the env and know a process is holding a query cache.
	EnvEnabled = "ACR_CONTEXT_FABRIC_EMBED_QUERY_CACHE_ENABLED"
	// EnvMaxEntries bounds distinct (identity, query text) pairs held at
	// once. Only consulted when EnvEnabled is true.
	EnvMaxEntries = "ACR_CONTEXT_FABRIC_EMBED_QUERY_CACHE_SIZE"
)

// Config is the cache's env-derived configuration.
type Config struct {
	Enabled    bool
	MaxEntries int
}

// ConfigFromEnv builds a Config from environment lookups. It never errors on
// an unset EnvMaxEntries (DefaultMaxEntries fills in); it errors only on a
// value that parses to something out of bounds, the same posture
// embedprovider.ConfigFromEnv and falkorgraph.ConfigFromEnv take for their
// own bounded integers.
func ConfigFromEnv(lookup func(string) (string, bool)) (Config, error) {
	maxEntries, err := envInt(lookup, EnvMaxEntries, DefaultMaxEntries)
	if err != nil {
		return Config{}, err
	}
	if maxEntries < 1 || maxEntries > 65536 {
		return Config{}, errors.New(EnvMaxEntries + " must be between one and sixty-five thousand five hundred thirty-six")
	}
	return Config{
		Enabled:    envBool(lookup, EnvEnabled, false),
		MaxEntries: maxEntries,
	}, nil
}

// Wrap returns embedder wrapped in a Cache when cfg.Enabled is true, and
// embedder UNCHANGED otherwise -- a disabled or nil embedder pays nothing,
// not even the map allocation. This is the single call site every
// construction path should use so "the cache is off" and "the cache was
// never wired" stay indistinguishable to a deployment that did not opt in.
func Wrap(embedder contextfabric.Embedder, cfg Config) contextfabric.Embedder {
	if embedder == nil || !cfg.Enabled {
		return embedder
	}
	return New(embedder, cfg.MaxEntries)
}

func envBool(lookup func(string) (string, bool), key string, fallback bool) bool {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
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
