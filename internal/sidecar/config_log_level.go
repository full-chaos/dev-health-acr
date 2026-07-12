package sidecar

import (
	"log/slog"
	"strings"
)

// logLevelOrDefault parses ACR_LOG_LEVEL (case-insensitive "debug", "info",
// "warn"/"warning", or "error") into a slog.Level, defaulting to fallback
// when unset or blank. An unrecognized value fails closed with a
// *ConfigError rather than silently falling back to the default level,
// so a typo in an operator's configuration is surfaced at LoadConfig time
// instead of silently running at an unexpected verbosity.
func logLevelOrDefault(lookup lookupEnv, key string, fallback slog.Level) (slog.Level, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, &ConfigError{Field: key, Detail: "must be one of \"debug\", \"info\", \"warn\", or \"error\""}
	}
}
