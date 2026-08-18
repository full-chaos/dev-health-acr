package pglifecycle

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Env* names the environment variables both acr-api (internal/runtime/hosted)
// and acr-projector (cmd/acr-projector) read for the CHAOS-3898 S2a-2
// build-aside-and-swap conversion, centralized here (not duplicated per
// binary) so the two processes -- which MUST agree on whether the lifecycle
// machinery is live, since one derives graph keys for reads and the other
// for writes -- cannot drift on spelling or defaults.
const (
	// EnvEnabled is the master switch (default false): an explicit
	// operator opt-in, independent of everything S2a shipped inert-by-default
	// (nil EpochResolver). Deliberately NOT inferred from any other flag --
	// this conversion changes the actual rebuild/CHAOS-3882-recovery
	// mechanism in production, so it gets its own dedicated, off-by-default
	// switch rather than turning on the moment this code merges.
	EnvEnabled = "ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED"
	// EnvLease bounds CachedResolver's per-organization cache TTL (design
	// brief §3.5: "KeyResolver refuses unbounded leases"). Must be positive
	// and at most MaxCachedResolverLease.
	EnvLease = "ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_LEASE"
	// EnvRequestDeadline is "D", the enforced per-investigation-request
	// binding deadline the drain-bound argument (GRAPH.DELETE issued no
	// earlier than drain_start + lease + deadline) depends on. Operators
	// MUST keep this >= the hosted API's own ACR_REQUEST_TIMEOUT (the
	// actual enforced per-request context deadline, internal/api/app.go's
	// requestTimeoutMiddleware) -- acr-projector has no visibility into
	// acr-api's configured value (a separate process/binary), so this is a
	// declared upper bound the operator is responsible for keeping sound,
	// not something derived automatically.
	EnvRequestDeadline = "ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_REQUEST_DEADLINE"
	// EnvGraceWindow is design brief D11's operator-set retention window.
	EnvGraceWindow = "ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_GRACE_WINDOW"
)

const (
	// defaultLease is well inside MaxCachedResolverLease while still being
	// long enough that steady-state KeyResolver reads rarely hit Postgres.
	defaultLease = time.Minute
	// defaultRequestDeadline matches internal/config's own
	// defaultRequestTimeout (15s) doubled, a conservative margin given
	// acr-projector cannot read acr-api's actual configured value directly.
	defaultRequestDeadline = 30 * time.Second
	// defaultGraceWindowEnv mirrors coordinator.go's own defaultGraceWindow
	// constant (kept independent, not imported, to avoid a dependency from
	// this package onto projectionrun -- the two defaults are pinned equal
	// by EnvConfig_test.go).
	defaultGraceWindowEnv = 24 * time.Hour
)

// EnvConfig is the CHAOS-3898 S2a-2 lifecycle machinery's resolved
// environment configuration, shared verbatim by both composition roots.
type EnvConfig struct {
	Enabled         bool
	Lease           time.Duration
	RequestDeadline time.Duration
	GraceWindow     time.Duration
}

// ConfigFromEnv reads EnvConfig from the process environment. Enabled=false
// (the default) means every OTHER field is irrelevant -- neither
// composition root wires EpochResolver/Lifecycle/RetireScheduler when this
// is false, so the whole conversion stays byte-identical to pre-CHAOS-3898
// behavior.
func ConfigFromEnv(lookup func(string) (string, bool)) (EnvConfig, error) {
	enabled := envBool(lookup, EnvEnabled, false)
	lease, err := envDuration(lookup, EnvLease, defaultLease)
	if err != nil {
		return EnvConfig{}, err
	}
	if enabled && (lease <= 0 || lease > MaxCachedResolverLease) {
		return EnvConfig{}, fmt.Errorf("%s must be a positive duration at most %s", EnvLease, MaxCachedResolverLease)
	}
	deadline, err := envDuration(lookup, EnvRequestDeadline, defaultRequestDeadline)
	if err != nil {
		return EnvConfig{}, err
	}
	if enabled && deadline <= 0 {
		return EnvConfig{}, fmt.Errorf("%s must be a positive duration", EnvRequestDeadline)
	}
	graceWindow, err := envDuration(lookup, EnvGraceWindow, defaultGraceWindowEnv)
	if err != nil {
		return EnvConfig{}, err
	}
	if enabled && graceWindow <= 0 {
		return EnvConfig{}, fmt.Errorf("%s must be a positive duration", EnvGraceWindow)
	}
	return EnvConfig{Enabled: enabled, Lease: lease, RequestDeadline: deadline, GraceWindow: graceWindow}, nil
}

func envBool(lookup func(string) (string, bool), key string, fallback bool) bool {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
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
