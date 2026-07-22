package sidecar

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	localIndexProviderEnvironment           = "ACR_LOCAL_INDEX_PROVIDER"
	localIndexExecutableEnvironment         = "ACR_CODEGRAPH_EXECUTABLE"
	localIndexTimeoutEnvironment            = "ACR_LOCAL_INDEX_TIMEOUT"
	localIndexMaxItemsEnvironment           = "ACR_LOCAL_INDEX_MAX_ITEMS"
	localIndexMaxOutputTokensEnvironment    = "ACR_LOCAL_INDEX_MAX_OUTPUT_TOKENS"
	localIndexMaxSerializedBytesEnvironment = "ACR_LOCAL_INDEX_MAX_SERIALIZED_BYTES"
	localIndexStalePolicyEnvironment        = "ACR_LOCAL_INDEX_STALE_POLICY"

	minLocalIndexTimeout         = 100 * time.Millisecond
	maxLocalIndexTimeout         = 15 * time.Second
	minLocalIndexItems           = 1
	maxLocalIndexItems           = 12
	minLocalIndexOutputTokens    = 125
	maxLocalIndexOutputTokens    = 4000
	minLocalIndexSerializedBytes = 2048
	maxLocalIndexSerializedBytes = 262144
)

// LocalIndexProviderMode selects an optional local evidence source.
type LocalIndexProviderMode string

const (
	LocalIndexProviderAuto      LocalIndexProviderMode = "auto"
	LocalIndexProviderDisabled  LocalIndexProviderMode = "disabled"
	LocalIndexProviderCodeGraph LocalIndexProviderMode = "codegraph"
)

// LocalIndexStalePolicy determines how callers handle stale local evidence.
type LocalIndexStalePolicy string

const (
	LocalIndexStaleGraceful LocalIndexStalePolicy = "graceful"
	LocalIndexStaleStrict   LocalIndexStalePolicy = "strict"
)

// LocalIndexConfig is intentionally separate from Config: local-index failures
// are optional and must never prevent the hosted sidecar from bootstrapping.
type LocalIndexConfig struct {
	Provider           LocalIndexProviderMode
	Executable         string
	Timeout            time.Duration
	MaxItems           int
	MaxOutputTokens    int
	MaxSerializedBytes int64
	StalePolicy        LocalIndexStalePolicy
	Err                error
}

// LoadLocalIndexConfig parses optional local-index settings. Invalid settings
// become a disabled local state rather than an error for the hosted bootstrap.
func LoadLocalIndexConfig() LocalIndexConfig {
	return loadLocalIndexConfig(os.LookupEnv)
}

func loadLocalIndexConfig(lookup lookupEnv) LocalIndexConfig {
	cfg := LocalIndexConfig{
		Provider:           LocalIndexProviderAuto,
		Executable:         strings.TrimSpace(firstOrEmpty(lookup, localIndexExecutableEnvironment)),
		Timeout:            3 * time.Second,
		MaxItems:           5,
		MaxOutputTokens:    1000,
		MaxSerializedBytes: 65536,
		StalePolicy:        LocalIndexStaleGraceful,
	}
	if err := cfg.parse(lookup); err != nil {
		cfg.Provider = LocalIndexProviderDisabled
		cfg.Err = err
	}
	return cfg
}

func (c *LocalIndexConfig) parse(lookup lookupEnv) error {
	if raw := strings.TrimSpace(firstOrEmpty(lookup, localIndexProviderEnvironment)); raw != "" {
		c.Provider = LocalIndexProviderMode(raw)
		if c.Provider != LocalIndexProviderAuto && c.Provider != LocalIndexProviderDisabled && c.Provider != LocalIndexProviderCodeGraph {
			return localIndexConfigError(localIndexProviderEnvironment, "must be auto, disabled, or codegraph")
		}
	}
	if c.Provider == LocalIndexProviderDisabled {
		return nil
	}
	if value, err := localIndexDuration(lookup, localIndexTimeoutEnvironment, c.Timeout, minLocalIndexTimeout, maxLocalIndexTimeout); err != nil {
		return err
	} else {
		c.Timeout = value
	}
	if value, err := localIndexInt(lookup, localIndexMaxItemsEnvironment, c.MaxItems, minLocalIndexItems, maxLocalIndexItems); err != nil {
		return err
	} else {
		c.MaxItems = value
	}
	if value, err := localIndexInt(lookup, localIndexMaxOutputTokensEnvironment, c.MaxOutputTokens, minLocalIndexOutputTokens, maxLocalIndexOutputTokens); err != nil {
		return err
	} else {
		c.MaxOutputTokens = value
	}
	if value, err := localIndexInt64(lookup, localIndexMaxSerializedBytesEnvironment, c.MaxSerializedBytes, minLocalIndexSerializedBytes, maxLocalIndexSerializedBytes); err != nil {
		return err
	} else {
		c.MaxSerializedBytes = value
	}
	if raw := strings.TrimSpace(firstOrEmpty(lookup, localIndexStalePolicyEnvironment)); raw != "" {
		c.StalePolicy = LocalIndexStalePolicy(raw)
		if c.StalePolicy != LocalIndexStaleGraceful && c.StalePolicy != LocalIndexStaleStrict {
			return localIndexConfigError(localIndexStalePolicyEnvironment, "must be graceful or strict")
		}
	}
	return nil
}

func localIndexDuration(lookup lookupEnv, field string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(firstOrEmpty(lookup, field))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, localIndexConfigError(field, fmt.Sprintf("must be between %s and %s", minimum, maximum))
	}
	return value, nil
}

func localIndexInt(lookup lookupEnv, field string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(firstOrEmpty(lookup, field))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, localIndexConfigError(field, fmt.Sprintf("must be between %d and %d", minimum, maximum))
	}
	return value, nil
}

func localIndexInt64(lookup lookupEnv, field string, fallback, minimum, maximum int64) (int64, error) {
	raw := strings.TrimSpace(firstOrEmpty(lookup, field))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum {
		return 0, localIndexConfigError(field, fmt.Sprintf("must be between %d and %d", minimum, maximum))
	}
	return value, nil
}

func localIndexConfigError(field, detail string) error {
	return &ConfigError{Field: field, Detail: detail}
}
