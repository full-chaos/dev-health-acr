package config

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-11914: the shared Ask Dev limits load with the plan's defaults, the
// same numbers contextfabric falls back to, and refuse out-of-bound values.
func TestAskDevLimitsDefaultsAndBounds(t *testing.T) {
	cfg, err := load(mapLookup(completeRuntimeEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AskDevMaxRunningPerPrincipal != 2 || cfg.AskDevMaxRunningPerOrg != 4 || cfg.AskDevMonthlyPlatformRunAllowance != 1000 {
		t.Fatalf("defaults = %d/%d/%d, want 2/4/1000", cfg.AskDevMaxRunningPerPrincipal, cfg.AskDevMaxRunningPerOrg, cfg.AskDevMonthlyPlatformRunAllowance)
	}
	if defaultAskDevMaxRunningPerPrincipal != contextfabric.DefaultMaxRunningPerPrincipal || defaultAskDevMaxRunningPerOrg != contextfabric.DefaultMaxRunningPerOrg ||
		defaultAskDevMonthlyRunAllowance != contextfabric.DefaultMonthlyPlatformRunAllowance || maxAskDevMonthlyRunAllowance != contextfabric.MaxMonthlyPlatformRunAllowance ||
		maxAskDevMaxRunningPerPrincipal != contextfabric.MaxRunningPerPrincipalLimit || maxAskDevMaxRunningPerOrg != contextfabric.MaxRunningPerOrgLimit {
		t.Fatal("config and contextfabric disagree on the Ask Dev limit defaults or bounds")
	}
	for name, env := range map[string]map[string]string{
		"principal zero":         {"ACR_ASK_DEV_MAX_RUNNING_PER_PRINCIPAL": "0"},
		"principal over":         {"ACR_ASK_DEV_MAX_RUNNING_PER_PRINCIPAL": "17"},
		"org below principal":    {"ACR_ASK_DEV_MAX_RUNNING_PER_PRINCIPAL": "3", "ACR_ASK_DEV_MAX_RUNNING_PER_ORG": "2"},
		"org over":               {"ACR_ASK_DEV_MAX_RUNNING_PER_ORG": "65"},
		"allowance zero":         {"ACR_ASK_DEV_MONTHLY_RUN_ALLOWANCE": "0"},
		"allowance over the max": {"ACR_ASK_DEV_MONTHLY_RUN_ALLOWANCE": "5001"},
		"not a number":           {"ACR_ASK_DEV_MONTHLY_RUN_ALLOWANCE": "lots"},
	} {
		values := completeRuntimeEnvironment()
		for key, value := range env {
			values[key] = value
		}
		if _, err := load(mapLookup(values)); err == nil || !strings.Contains(err.Error(), "ACR_ASK_DEV_") {
			t.Errorf("%s: load() error = %v, want an ACR_ASK_DEV_ refusal", name, err)
		}
	}
	values := completeRuntimeEnvironment()
	values["ACR_ASK_DEV_MAX_RUNNING_PER_PRINCIPAL"], values["ACR_ASK_DEV_MAX_RUNNING_PER_ORG"], values["ACR_ASK_DEV_MONTHLY_RUN_ALLOWANCE"] = "3", "8", "5000"
	if cfg, err := load(mapLookup(values)); err != nil || cfg.AskDevMaxRunningPerPrincipal != 3 || cfg.AskDevMaxRunningPerOrg != 8 || cfg.AskDevMonthlyPlatformRunAllowance != 5000 {
		t.Fatalf("operator values = %+v err %v", cfg, err)
	}
}
