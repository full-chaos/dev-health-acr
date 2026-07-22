package sidecar

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadLocalIndexConfig_defaultsToBoundedAutoMode(t *testing.T) {
	// Given
	lookup := localIndexEnvironment(nil)

	// When
	cfg := loadLocalIndexConfig(lookup)

	// Then
	require.Equal(t, LocalIndexProviderAuto, cfg.Provider)
	require.Equal(t, 3*time.Second, cfg.Timeout)
	require.Equal(t, 5, cfg.MaxItems)
	require.Equal(t, 1000, cfg.MaxOutputTokens)
	require.Equal(t, int64(65536), cfg.MaxSerializedBytes)
	require.Equal(t, LocalIndexStaleGraceful, cfg.StalePolicy)
	require.NoError(t, cfg.Err)
}

func TestLoadLocalIndexConfig_disablesMalformedLocalSettingsWithoutErroringHostedConfig(t *testing.T) {
	// Given
	lookup := localIndexEnvironment(map[string]string{
		localIndexTimeoutEnvironment: "not-a-duration",
	})

	// When
	cfg := loadLocalIndexConfig(lookup)

	// Then
	require.Equal(t, LocalIndexProviderDisabled, cfg.Provider)
	require.Error(t, cfg.Err)
	require.Contains(t, cfg.Err.Error(), localIndexTimeoutEnvironment)
	require.NotContains(t, cfg.Err.Error(), "not-a-duration")
}

func TestLoadLocalIndexConfig_disablesExplicitly(t *testing.T) {
	// Given
	lookup := localIndexEnvironment(map[string]string{
		localIndexProviderEnvironment: "disabled",
	})

	// When
	cfg := loadLocalIndexConfig(lookup)

	// Then
	require.Equal(t, LocalIndexProviderDisabled, cfg.Provider)
	require.NoError(t, cfg.Err)
}

func TestLoadLocalIndexConfig_rejectsValuesOutsideContractBounds(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "timeout below minimum", values: map[string]string{localIndexTimeoutEnvironment: "99ms"}},
		{name: "items above maximum", values: map[string]string{localIndexMaxItemsEnvironment: "13"}},
		{name: "tokens below minimum", values: map[string]string{localIndexMaxOutputTokensEnvironment: "124"}},
		{name: "bytes above maximum", values: map[string]string{localIndexMaxSerializedBytesEnvironment: "262145"}},
		{name: "unknown stale policy", values: map[string]string{localIndexStalePolicyEnvironment: "later"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			lookup := localIndexEnvironment(test.values)

			// When
			cfg := loadLocalIndexConfig(lookup)

			// Then
			require.Equal(t, LocalIndexProviderDisabled, cfg.Provider)
			require.Error(t, cfg.Err)
		})
	}
}

func localIndexEnvironment(values map[string]string) lookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
