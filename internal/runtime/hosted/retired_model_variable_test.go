package hosted

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

func TestNewContextFabricModelRuntime_warnsOnceAboutARetiredVariable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		retries  string
		wantWarn bool
	}{
		{name: "set", retries: "2", wantWarn: true},
		{name: "unset", retries: "", wantWarn: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
			env := map[string]string{modelprovider.EnvAPIKey: "sk-configured"}
			if tc.retries != "" {
				env[modelprovider.EnvMaxTransportRetries] = tc.retries
			}
			runtime, err := newContextFabricModelRuntime(context.Background(), envLookup(env), nil, logger)
			if err != nil || runtime == nil {
				t.Fatalf("runtime = %v, err = %v", runtime, err)
			}
			warned := strings.Count(logs.String(), "level=WARN msg=\"context fabric model variable no longer has an effect; ignoring\"")
			named := strings.Contains(logs.String(), modelprovider.EnvMaxTransportRetries)
			if tc.wantWarn && (warned != 1 || !named) {
				t.Fatalf("want one WARN naming %s, got: %s", modelprovider.EnvMaxTransportRetries, logs.String())
			}
			if !tc.wantWarn && warned != 0 {
				t.Fatalf("want no WARN, got: %s", logs.String())
			}
		})
	}
}
