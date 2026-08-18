package pglifecycle_test

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/stretchr/testify/require"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestConfigFromEnv_DefaultsToDisabled(t *testing.T) {
	cfg, err := pglifecycle.ConfigFromEnv(lookupFrom(nil))
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.Equal(t, 24*time.Hour, cfg.GraceWindow, "the env default must match coordinator.go's own defaultGraceWindow")
}

func TestConfigFromEnv_RefusesAnUnboundedLeaseWhenEnabled(t *testing.T) {
	_, err := pglifecycle.ConfigFromEnv(lookupFrom(map[string]string{
		pglifecycle.EnvEnabled: "true",
		pglifecycle.EnvLease:   "24h", // exceeds MaxCachedResolverLease (10m)
	}))
	require.Error(t, err)
}

func TestConfigFromEnv_ParsesExplicitOverrides(t *testing.T) {
	cfg, err := pglifecycle.ConfigFromEnv(lookupFrom(map[string]string{
		pglifecycle.EnvEnabled:         "true",
		pglifecycle.EnvLease:           "2m",
		pglifecycle.EnvRequestDeadline: "45s",
		pglifecycle.EnvGraceWindow:     "72h",
	}))
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, 2*time.Minute, cfg.Lease)
	require.Equal(t, 45*time.Second, cfg.RequestDeadline)
	require.Equal(t, 72*time.Hour, cfg.GraceWindow)
}

func TestConfigFromEnv_RejectsGarbageDuration(t *testing.T) {
	_, err := pglifecycle.ConfigFromEnv(lookupFrom(map[string]string{
		pglifecycle.EnvLease: "not-a-duration",
	}))
	require.Error(t, err)
}
