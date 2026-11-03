package config

import "testing"

// CHAOS-12582: the sankey switch (investment_repository_flow) is off unless a deployment
// turns it on, a value that is not a boolean is refused, and the startup line
// reports it.
func TestTheInvestmentSankeySwitchDefaultsOff(t *testing.T) {
	if ContextFabricInvestmentSankeyEnabledEnvVar != "ACR_CONTEXT_FABRIC_INVESTMENT_SANKEY_ENABLED" {
		t.Fatalf("the switch is read from %q", ContextFabricInvestmentSankeyEnabledEnvVar)
	}
	for _, tc := range []struct {
		value   string
		set     bool
		want    bool
		wantErr bool
	}{
		{set: false, want: false},
		{value: "true", set: true, want: true},
		{value: "false", set: true, want: false},
		{value: "sometimes", set: true, wantErr: true},
	} {
		lookup := func(key string) (string, bool) {
			if key == ContextFabricInvestmentSankeyEnabledEnvVar && tc.set {
				return tc.value, true
			}
			return "", false
		}
		var cfg Config
		err := loadHostedRuntimeValues(lookup, &cfg, false, false, false)
		if tc.wantErr {
			if err == nil {
				t.Errorf("value %q: no error", tc.value)
			}
			continue
		}
		if err != nil {
			t.Fatalf("value %q: %v", tc.value, err)
		}
		if cfg.ContextFabricInvestmentSankeyEnabled != tc.want {
			t.Errorf("value %q (set=%v): enabled = %v, want %v", tc.value, tc.set, cfg.ContextFabricInvestmentSankeyEnabled, tc.want)
		}
	}
	found := false
	attrs := Config{ContextFabricInvestmentSankeyEnabled: true}.SafeAttributes()
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i] == "context_fabric_investment_sankey_enabled" && attrs[i+1] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("the startup attributes do not report the switch")
	}
}
