package sidecar

import (
	"strings"
	"testing"
)

func waitConfigLookup(extra map[string]string) lookupEnv {
	values := map[string]string{APIURLEnvironment: "https://acr.example.test"}
	for key, value := range extra {
		values[key] = value
	}
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestInvestigationWaitMustClearTheCallTimeoutByTheReserve(t *testing.T) {
	cases := []struct {
		name    string
		timeout string
		wait    string
		wantErr string
	}{
		{"off", "20s", "", ""},
		{"fits exactly", "50s", "45s", ""},
		{"one second short", "49s", "45s", InvestigationWaitEnvironment},
		{"timeout equals wait", "45s", "45s", InvestigationWaitEnvironment},
		{"minimum", "20s", "10s", ""},
		{"below minimum", "20s", "9s", InvestigationWaitEnvironment},
		{"fractional seconds", "30s", "10500ms", InvestigationWaitEnvironment},
		{"negative", "30s", "-10s", InvestigationWaitEnvironment},
		{"not a duration", "30s", "ten", InvestigationWaitEnvironment},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{TimeoutEnvironment: tc.timeout}
			if tc.wait != "" {
				env[InvestigationWaitEnvironment] = tc.wait
			}
			cfg, err := loadConfig(waitConfigLookup(env))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.wait == "" && cfg.InvestigationWait != 0 {
					t.Fatalf("wait = %s, want off", cfg.InvestigationWait)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one naming %s", err, tc.wantErr)
			}
		})
	}
}
