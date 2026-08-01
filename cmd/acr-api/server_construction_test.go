package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
)

func TestPrepareServer_does_not_create_server_when_runtime_open_fails(t *testing.T) {
	// Given
	cfg := validServeConfig(t)
	created := false
	request := serverBuildRequest{
		config: cfg,
		logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
		openRuntime: func(context.Context, hosted.Options) (*hosted.Runtime, error) {
			return nil, errors.New("runtime unavailable")
		},
		newServer: func(api.ServerConfig, http.Handler, *slog.Logger) (serverRunner, error) {
			created = true
			return nil, errors.New("must not be called")
		},
	}

	// When
	server, closeRuntime, err := prepareServer(context.Background(), request)

	// Then
	if err == nil || server != nil || closeRuntime != nil {
		t.Fatalf("server = %#v, closer present = %t, error = %v; want runtime failure", server, closeRuntime != nil, err)
	}
	if created {
		t.Fatal("server was created after hosted runtime construction failed")
	}
}

func validServeConfig(t *testing.T) config.Config {
	t.Helper()
	for key, value := range map[string]string{
		"ACR_ENVIRONMENT":                       "test",
		"ACR_REQUIRE_BACKING_STORES":            "true",
		"ACR_CLICKHOUSE_DSN":                    "clickhouse://configured",
		"ACR_POSTGRES_DSN":                      "postgres://configured",
		"ACR_EVIDENCE_ID_ACTIVE_KID":            "current",
		"ACR_EVIDENCE_ID_KEYS":                  "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        "https://ops.example.test",
		"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "/run/secrets/ops-token",
		"ACR_DEVICE_VERIFICATION_URL":           "https://verify.example.test/device",
		"ACR_POSTGRES_CONNECTION_KIND":          "direct",
	} {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
