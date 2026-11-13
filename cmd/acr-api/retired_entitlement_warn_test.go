package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

// acr-api starts with the retired token file setting present and writes one Warn line that names it, never its value
// (CHAOS-13600, product rule 3).
func TestStartupWarnsOnceOnTheRetiredTokenFileSetting(t *testing.T) {
	for name, test := range map[string]struct {
		env   map[string]string
		warns int
	}{
		"unset":     {nil, 0},
		"set":       {map[string]string{config.RetiredEntitlementTokenFileEnv: "/run/secrets/ops-token"}, 1},
		"set empty": {map[string]string{config.RetiredEntitlementTokenFileEnv: ""}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			logStarting(slog.New(slog.NewJSONHandler(&out, nil)), version.Info{}, config.Config{}, func(key string) (string, bool) {
				value, ok := test.env[key]
				return value, ok
			})
			warns := 0
			for _, raw := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				var line map[string]any
				if err := json.Unmarshal([]byte(raw), &line); err != nil {
					t.Fatalf("log line is not JSON: %v: %s", err, raw)
				}
				if line["level"] != "WARN" {
					continue
				}
				if line["msg"] != config.RetiredEntitlementTokenFileWarning || line["setting"] != config.RetiredEntitlementTokenFileEnv {
					t.Fatalf("unexpected Warn line: %s", raw)
				}
				warns++
			}
			if warns != test.warns {
				t.Fatalf("%d Warn lines, want %d: %s", warns, test.warns, out.String())
			}
			if strings.Contains(out.String(), "/run/secrets/ops-token") {
				t.Fatalf("the start log echoes the value: %s", out.String())
			}
		})
	}
}
