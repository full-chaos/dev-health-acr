package config

import (
	"strings"
	"testing"
)

func projectorStoresEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"ACR_POSTGRES_DSN":             "postgres://configured",
		"ACR_POSTGRES_CONNECTION_KIND": "direct",
		"ACR_CLICKHOUSE_DSN":           "clickhouse://configured",
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

func textPtr(value string) *string { return &value }

func TestLoadProjectorStatementMaxMemoryOverride(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *string
		set   bool
		want  uint64
	}{
		{name: "unset keeps the default", set: false},
		{name: "four GiB", value: textPtr("4294967296"), set: true, want: 4294967296},
		{name: "surrounding space", value: textPtr(" 268435456 "), set: true, want: 268435456},
		{name: "blank keeps the default", value: textPtr("  "), set: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]string{}
			if tc.value != nil {
				extra[EnvProjectorStatementMaxMemory] = *tc.value
			}
			cfg, err := loadProjector(mapLookup(projectorStoresEnv(extra)), requiredStoresAll)
			if err != nil {
				t.Fatalf("loadProjector error = %v", err)
			}
			if cfg.StatementMaxMemoryBytesSet != tc.set || cfg.StatementMaxMemoryBytes != tc.want {
				t.Fatalf("override = (%d, set=%v), want (%d, set=%v)", cfg.StatementMaxMemoryBytes, cfg.StatementMaxMemoryBytesSet, tc.want, tc.set)
			}
		})
	}
}

func TestLoadProjectorStatementMaxMemoryRefusesZeroAndNonNumbers(t *testing.T) {
	for _, value := range []string{"0", "abc", "-1", "1.5"} {
		_, err := loadProjector(mapLookup(projectorStoresEnv(map[string]string{EnvProjectorStatementMaxMemory: value})), requiredStoresAll)
		if err == nil || !strings.Contains(err.Error(), EnvProjectorStatementMaxMemory) {
			t.Fatalf("%s=%q: error = %v, want a refusal naming the variable", EnvProjectorStatementMaxMemory, value, err)
		}
	}
}

// priors opens no ClickHouse client, so a shared environment's override is
// neither read nor validated there.
func TestLoadProjectorPriorsIgnoresStatementMaxMemory(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_POSTGRES_DSN":             "postgres://configured",
		"ACR_POSTGRES_CONNECTION_KIND": "direct",
		EnvProjectorStatementMaxMemory: "0",
	}), requiredStoresPostgresOnly)
	if err != nil {
		t.Fatalf("loadProjector(requiredStoresPostgresOnly) error = %v, want success", err)
	}
	if cfg.StatementMaxMemoryBytesSet {
		t.Fatal("priors read the ClickHouse statement cap override")
	}
}
