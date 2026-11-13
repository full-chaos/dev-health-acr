package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func credentialEnv(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	values := completeRuntimeEnvironment()
	for k, v := range extra {
		if v == "-" {
			delete(values, k)
			continue
		}
		values[k] = v
	}
	return values
}

func withoutWorkloadClient(extra map[string]string) map[string]string {
	out := map[string]string{"ACR_AUTH_WORKLOAD_EXCHANGE_URL": "-", "ACR_AUTH_WORKLOAD_ISSUER": "-", "ACR_AUTH_WORKLOAD_SUBJECT_FILE": "-"}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// The domain of the entitlement credential settings (CHAOS-13600): the workload token is the one credential and an
// unset credential selects it. The retired credential value stops the start with a message that names the setting,
// never its value. The retired token file setting does not stop the start (product rule 3): only the missing-config
// checks refuse a deployment that sets it.
func TestEntitlementCredentialDomain(t *testing.T) {
	const (
		credential = "ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL"
		tokenFile  = RetiredEntitlementTokenFileEnv
		retired    = credential + "=" + RetiredEntitlementServiceToken + " is retired"
	)
	localDevelopment := map[string]string{"ACR_ENVIRONMENT": "development", "ACR_DEV_HEALTH_ENTITLEMENT_URL": "-", "ACR_POSTGRES_DSN": "postgres://configured?sslmode=verify-full"}
	with := func(base map[string]string, extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	tests := map[string]struct {
		extra map[string]string
		want  string          // "" = loads
		mode  EntitlementMode // the mode of a cell that loads; "" = remote
	}{
		"credential absent":                     {nil, "", ""},
		"credential empty":                      {map[string]string{credential: ""}, "", ""},
		"credential workload":                   {map[string]string{credential: "workload"}, "", ""},
		"credential workload with spaces":       {map[string]string{credential: " workload "}, "", ""},
		"credential service_token":              {map[string]string{credential: RetiredEntitlementServiceToken}, retired, ""},
		"credential unknown":                    {map[string]string{credential: "svc_acr"}, credential + " must be workload or unset", ""},
		"credential mixed case Workload":        {map[string]string{credential: "Workload"}, credential + " must be workload or unset", ""},
		"credential mixed case Service_Token":   {map[string]string{credential: "Service_Token"}, credential + " must be workload or unset", ""},
		"token file set alone":                  {map[string]string{tokenFile: "/run/secrets/ops-token"}, "", ""},
		"token file set empty":                  {map[string]string{tokenFile: ""}, "", ""},
		"token file with credential workload":   {map[string]string{tokenFile: "/run/secrets/ops-token", credential: "workload"}, "", ""},
		"token file with service_token":         {map[string]string{tokenFile: "/run/secrets/ops-token", credential: RetiredEntitlementServiceToken}, retired, ""},
		"token file in local development":       {with(localDevelopment, map[string]string{tokenFile: "/run/secrets/ops-token"}), "", EntitlementModeLocal},
		"token file without the auth client":    {withoutWorkloadClient(map[string]string{tokenFile: "/run/secrets/ops-token"}), "ACR_AUTH_WORKLOAD_* must be configured", ""},
		"token file in production without URL":  {map[string]string{tokenFile: "/run/secrets/ops-token", "ACR_ENVIRONMENT": "production", "ACR_DEV_HEALTH_ENTITLEMENT_URL": "-"}, "remote entitlement configuration is required in staging and production", ""},
		"URL without the auth workload client":  {withoutWorkloadClient(nil), "ACR_AUTH_WORKLOAD_* must be configured", ""},
		"workload without the auth client":      {withoutWorkloadClient(map[string]string{credential: "workload"}), "ACR_AUTH_WORKLOAD_* must be configured", ""},
		"workload without the entitlement URL":  {map[string]string{credential: "workload", "ACR_DEV_HEALTH_ENTITLEMENT_URL": "-"}, "ACR_DEV_HEALTH_ENTITLEMENT_URL is required", ""},
		"service_token without the auth client": {withoutWorkloadClient(map[string]string{credential: RetiredEntitlementServiceToken}), retired, ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := load(mapLookup(credentialEnv(t, test.extra)))
			t.Logf("cell %q: err=%v", name, err)
			if test.want == "" {
				if err != nil {
					t.Fatalf("load() error = %v, want it to load", err)
				}
				want := test.mode
				if want == "" {
					want = EntitlementModeRemote
				}
				if cfg.EntitlementMode() != want {
					t.Fatalf("mode = %q, want %q", cfg.EntitlementMode(), want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load() error = %v, want it to contain %q", err, test.want)
			}
			if strings.Contains(err.Error(), tokenFile) {
				t.Fatalf("the error names the ignored token file setting: %v", err)
			}
			for _, value := range []string{"svc_acr", "/run/secrets/ops-token", "Service_Token"} {
				if strings.Contains(err.Error(), value) {
					t.Fatalf("the error echoes the configured value %q: %v", value, err)
				}
			}
		})
	}
}

func TestValidateRefusesTheRetiredCredentialOnABuiltConfig(t *testing.T) {
	cfg, err := load(mapLookup(credentialEnv(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DevHealthEntitlementCredential = "service_token"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "is retired") {
		t.Fatalf("Validate() error = %v, want the retired credential refused", err)
	}
}

func TestCheckEntitlementCredentialSettingsIgnoresTheTokenFile(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == RetiredEntitlementTokenFileEnv {
			return "", true
		}
		return "", false
	}
	if err := CheckEntitlementCredentialSettings(lookup); err != nil {
		t.Fatalf("a present but empty token file setting stopped the start: %v", err)
	}
	if err := CheckEntitlementCredentialSettings(func(string) (string, bool) { return "", false }); err != nil {
		t.Fatalf("no entitlement setting: %v", err)
	}
}

// The retired token file setting writes one Warn line that names it, never its value; an unset one writes nothing.
func TestWarnRetiredEntitlementSettingsWritesOneLine(t *testing.T) {
	for name, test := range map[string]struct {
		env   map[string]string
		lines int
	}{
		"unset":             {nil, 0},
		"set":               {map[string]string{RetiredEntitlementTokenFileEnv: "/run/secrets/ops-token"}, 1},
		"set empty":         {map[string]string{RetiredEntitlementTokenFileEnv: ""}, 1},
		"set with workload": {map[string]string{RetiredEntitlementTokenFileEnv: "/run/secrets/ops-token", "ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "workload"}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			WarnRetiredEntitlementSettings(slog.New(slog.NewJSONHandler(&out, nil)), func(k string) (string, bool) { v, ok := test.env[k]; return v, ok })
			lines := retiredEntitlementWarnLines(t, out.String())
			if lines != test.lines {
				t.Fatalf("%d Warn lines, want %d: %s", lines, test.lines, out.String())
			}
			if strings.Contains(out.String(), "/run/secrets/ops-token") {
				t.Fatalf("the Warn line echoes the value: %s", out.String())
			}
		})
	}
}

// retiredEntitlementWarnLines counts the Warn lines of a JSON log and fails on a Warn line that is not the retired
// token file line.
func retiredEntitlementWarnLines(t *testing.T, log string) int {
	t.Helper()
	count := 0
	for _, raw := range strings.Split(strings.TrimSpace(log), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, raw)
		}
		if line["level"] != "WARN" {
			continue
		}
		if line["msg"] != RetiredEntitlementTokenFileWarning || line["setting"] != RetiredEntitlementTokenFileEnv {
			t.Fatalf("unexpected Warn line: %s", raw)
		}
		count++
	}
	return count
}
