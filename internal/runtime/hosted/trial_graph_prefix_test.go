package hosted_test

import (
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
)

func stubTrialInputsForPrefix(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"ACR_TEST_TRIAL_POSTGRES_DSN":    "postgres://stub/stub",
		"ACR_TEST_TRIAL_CLICKHOUSE_DSN":  "clickhouse://stub/stub",
		"ACR_TEST_TRIAL_FALKOR_ADDR":     "stub:6379",
		"ACR_TEST_TRIAL_EMBED_MODEL":     "text-embedding-3-large",
		"ACR_TEST_TRIAL_EMBED_DIMENSION": "3072",
		"ACR_TEST_TRIAL_EMBED_API_KEY":   "stub-key",
	} {
		t.Setenv(key, value)
	}
}

func TestWireProductionEnv_FalkorGraphPrefix_TrialPrefixedInputReachesGraphPrefix(t *testing.T) {
	t.Run("trial-prefixed input reaches the graph prefix key and beats an ambient leak", func(t *testing.T) {
		stubTrialInputsForPrefix(t)
		t.Setenv(falkorgraph.EnvGraphPrefix, "acr-cf-ambient")
		t.Setenv("ACR_TEST_TRIAL_FALKOR_GRAPH_PREFIX", "acr-cf-probe-")

		wireProductionEnv(t, true)

		if got := os.Getenv(falkorgraph.EnvGraphPrefix); got != "acr-cf-probe-" {
			t.Fatalf("after wireProductionEnv, %s = %q, want %q", falkorgraph.EnvGraphPrefix, got, "acr-cf-probe-")
		}
	})
	t.Run("unset input leaves the key unset so the config default applies", func(t *testing.T) {
		stubTrialInputsForPrefix(t)
		t.Setenv(falkorgraph.EnvGraphPrefix, "acr-cf-ambient")

		wireProductionEnv(t, true)

		if got := os.Getenv(falkorgraph.EnvGraphPrefix); got != "" {
			t.Fatalf("after wireProductionEnv with no trial-prefixed input, %s = %q, want empty", falkorgraph.EnvGraphPrefix, got)
		}
	})
}

func TestTrialGraphPrefixLine(t *testing.T) {
	cases := []struct {
		name, requested, resolved, wantLine string
		wantErr                             bool
	}{
		{"default is named, never silent", "", "", "TRIAL GRAPH PREFIX read: acr-cf (default)", false},
		{"requested and resolved agree", "acr-cf-x-", "acr-cf-x-", "TRIAL GRAPH PREFIX read: acr-cf-x-", false},
		{"requested but not resolved fails", "acr-cf-x-", "", "", true},
		{"requested differs from resolved fails", "acr-cf-x-", "acr-cf", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line, err := trialGraphPrefixLine(tc.requested, tc.resolved)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !strings.HasPrefix(line, tc.wantLine) {
				t.Fatalf("line = %q, want prefix %q", line, tc.wantLine)
			}
		})
	}
}
