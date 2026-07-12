package sidecar

import (
	"log/slog"
	"testing"
)

// TestLoadConfigDefaultsToInfoLogLevel: Given ACR_LOG_LEVEL is unset, When
// LoadConfig runs, Then LogLevel defaults to slog.LevelInfo.
func TestLoadConfigDefaultsToInfoLogLevel(t *testing.T) {
	cfg, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "https://acr.example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("expected default log level info, got: %v", cfg.LogLevel)
	}
}

// TestLoadConfigAcceptsEveryDocumentedLogLevel: Given each of the four
// documented ACR_LOG_LEVEL values (case-insensitive), When LoadConfig
// runs, Then it resolves to the matching slog.Level.
func TestLoadConfigAcceptsEveryDocumentedLogLevel(t *testing.T) {
	cases := []struct {
		value string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
	}
	for _, tc := range cases {
		cfg, err := loadConfig(lookupFromMap(map[string]string{
			APIURLEnvironment:   "https://acr.example.com",
			LogLevelEnvironment: tc.value,
		}))
		if err != nil {
			t.Fatalf("ACR_LOG_LEVEL=%q: unexpected error: %v", tc.value, err)
		}
		if cfg.LogLevel != tc.want {
			t.Fatalf("ACR_LOG_LEVEL=%q: got level %v, want %v", tc.value, cfg.LogLevel, tc.want)
		}
	}
}

// TestLoadConfigRejectsUnrecognizedLogLevel: Given an unrecognized
// ACR_LOG_LEVEL value, When LoadConfig runs, Then it fails closed with a
// *ConfigError instead of silently falling back to the default level.
func TestLoadConfigRejectsUnrecognizedLogLevel(t *testing.T) {
	_, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:   "https://acr.example.com",
		LogLevelEnvironment: "verbose",
	}))
	if err == nil {
		t.Fatal("expected an unrecognized ACR_LOG_LEVEL to be rejected")
	}
	var configErr *ConfigError
	if configErr, _ = err.(*ConfigError); configErr == nil {
		t.Fatalf("expected a *ConfigError, got: %v (%T)", err, err)
	}
	if configErr.Field != LogLevelEnvironment {
		t.Fatalf("expected the error to name %s, got: %s", LogLevelEnvironment, configErr.Field)
	}
}
