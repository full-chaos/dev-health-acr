package mcp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/version"
)

// The answer a wait budget produces is written to the caller, so the HTTP
// write deadline must outlast the budget plus the composition reserve.
func TestServeRefusesAWriteTimeoutThatCutsOffTheWaitBudget(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	cases := []struct {
		name    string
		wait    time.Duration
		write   time.Duration
		wantErr bool
	}{
		{"budget off", 0, time.Second, false},
		{"write clears budget + reserve", 45 * time.Second, 50 * time.Second, false},
		{"write one second short", 45 * time.Second, 49 * time.Second, true},
		{"write below budget", 45 * time.Second, 30 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtureConfig(t, server)
			cfg.Timeout = 60 * time.Second
			cfg.InvestigationWait = tc.wait
			opts := DefaultServeOptions()
			opts.WriteTimeout = tc.write
			_, err := newServeProcessConfig(cfg, version.Info{}, io.Discard, opts)
			var invalid *ErrServeOptionInvalid
			if tc.wantErr {
				if !errors.As(err, &invalid) || invalid.Setting != HTTPWriteTimeoutEnvironment {
					t.Fatalf("error = %v, want a refusal naming %s", err, HTTPWriteTimeoutEnvironment)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
