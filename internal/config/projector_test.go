package config

import (
	"strings"
	"testing"

	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
)

func TestLoadProjectorDefaults(t *testing.T) {
	cfg, err := loadProjector(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != defaultProjectorListenAddress {
		t.Fatalf("listen address = %q", cfg.ListenAddress)
	}
	if cfg.ProjectionEnabled {
		t.Fatal("projection must be disabled by default")
	}
	if cfg.PollInterval != defaultProjectionPollInterval || cfg.Concurrency != defaultProjectionConcurrency {
		t.Fatalf("unexpected scheduling defaults: %#v", cfg)
	}
	if cfg.DrainBatchBudget != defaultProjectionDrainBudget {
		t.Fatalf("DrainBatchBudget = %d, want default %d", cfg.DrainBatchBudget, defaultProjectionDrainBudget)
	}
	if cfg.RequireBackingStores {
		t.Fatal("development must not require backing stores by default")
	}
	// CHAOS-3848: acr-projector is the binary that was actually wedged --
	// it must inherit the same raised default acr-api does, via the shared
	// loadHostedRuntimeValues path.
	if cfg.ClickHouseMaxBytesToRead != runtimeclickhouse.DefaultMaxBytesToRead {
		t.Fatalf("ClickHouseMaxBytesToRead = %d, want default %d", cfg.ClickHouseMaxBytesToRead, runtimeclickhouse.DefaultMaxBytesToRead)
	}
}

func TestLoadProjector_appliesConfiguredClickHouseMaxBytesToRead(t *testing.T) {
	// Given
	// When
	cfg, err := loadProjector(mapLookup(map[string]string{"ACR_CLICKHOUSE_MAX_BYTES_TO_READ": "33554432"}))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClickHouseMaxBytesToRead != 32<<20 {
		t.Fatalf("ClickHouseMaxBytesToRead = %d, want %d", cfg.ClickHouseMaxBytesToRead, uint64(32<<20))
	}
}

func TestLoadProjector_rejectsInvalidClickHouseMaxBytesToRead(t *testing.T) {
	for _, value := range []string{"0", "-1", "garbage"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadProjector(mapLookup(map[string]string{"ACR_CLICKHOUSE_MAX_BYTES_TO_READ": value}))
			if err == nil || !strings.Contains(err.Error(), "ACR_CLICKHOUSE_MAX_BYTES_TO_READ") {
				t.Fatalf("loadProjector() error = %v, want ACR_CLICKHOUSE_MAX_BYTES_TO_READ rejection", err)
			}
		})
	}
}

func TestLoadProjectorProductionRequiresBackingStores(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{"ACR_ENVIRONMENT": "production"}))
	if err == nil {
		t.Fatal("expected an error: production requires ACR_CLICKHOUSE_DSN/ACR_POSTGRES_DSN")
	}
}

func TestLoadProjectorEnabledProductionRequiresOrgAllowlist(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{
		"ACR_ENVIRONMENT": "production", "ACR_CLICKHOUSE_DSN": "https://clickhouse.internal", "ACR_POSTGRES_DSN": "postgres://db/acr",
		"ACR_POSTGRES_CONNECTION_KIND": "direct", "ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": "true",
	}))
	if err == nil {
		t.Fatal("expected an error: enabling projection without an organization allowlist")
	}
}

func TestLoadProjectorParsesOrgAllowlistAndScheduling(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED":            "true",
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS":             "org-1, org-2 ,org-3",
		"ACR_CONTEXT_FABRIC_PROJECTION_POLL_INTERVAL":      "30s",
		"ACR_CONTEXT_FABRIC_PROJECTION_CONCURRENCY":        "10",
		"ACR_CONTEXT_FABRIC_PROJECTION_DRAIN_BATCH_BUDGET": "50",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OrgIDs) != 3 || cfg.OrgIDs[0] != "org-1" || cfg.OrgIDs[2] != "org-3" {
		t.Fatalf("org ids = %#v", cfg.OrgIDs)
	}
	if cfg.PollInterval.String() != "30s" || cfg.Concurrency != 10 || cfg.DrainBatchBudget != 50 {
		t.Fatalf("scheduling = %#v", cfg)
	}
}

func TestLoadProjectorRejectsInvalidEnvironment(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{"ACR_ENVIRONMENT": "sandbox"}))
	if err == nil {
		t.Fatal("expected an error for an invalid environment")
	}
}

// TestProjectionEnablementIsIndependentOfTheGraphReadsFlag proves the two
// enablement levers required by the ticket (independent disablement of
// projection and reads) don't interact on the projector side: toggling
// ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED -- the flag Reset 1B/1C's
// GraphReader/hosted composition owns, reserved here as
// GraphReadsEnabledEnvVar -- has zero effect on ProjectionEnabled in either
// direction. This package intentionally never reads that variable itself;
// the assertion is that setting it can't accidentally flip projection on
// or off. The read side's own independent enablement is Reset 1B/1C's to
// prove once GraphReader exists.
func TestProjectionEnablementIsIndependentOfTheGraphReadsFlag(t *testing.T) {
	enabled, err := loadProjector(mapLookup(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": "true", "ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS": "org-1",
		GraphReadsEnabledEnvVar: "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.ProjectionEnabled {
		t.Fatal("projection must stay enabled regardless of the graph-reads flag's value")
	}

	disabled, err := loadProjector(mapLookup(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": "false",
		GraphReadsEnabledEnvVar:                 "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if disabled.ProjectionEnabled {
		t.Fatal("the graph-reads flag must never turn projection on")
	}
}

func TestProjectorConfigSafeAttributesOmitDSNs(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{"ACR_POSTGRES_DSN": "postgres://secret@db/acr"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, attr := range cfg.SafeAttributes() {
		if text, ok := attr.(string); ok && text == "postgres://secret@db/acr" {
			t.Fatal("SafeAttributes leaked a DSN")
		}
	}
}

// TestTeamsProjectsDefaultsToEnabled is CHAOS-3802 D2. The flag defaulted to
// false only because teams_projects.go was a stub that failed loudly when
// switched on; with a real source behind it, a default-off feature whose
// acceptance criterion ("an org rebuild picks the new kinds up") requires it
// on is a dead guard, not a safety measure. An operator can still turn it off
// explicitly -- which the second half of this test proves is a real, reachable
// choice and not a value the loader ignores.
func TestTeamsProjectsDefaultsToEnabled(t *testing.T) {
	t.Parallel()
	cfg, err := loadProjector(mapLookup(nil))
	if err != nil {
		t.Fatalf("loadProjector: %v", err)
	}
	if !cfg.TeamsProjectsEnabled {
		t.Fatal("ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED must default to true now that the source is implemented")
	}
	off, err := loadProjector(mapLookup(map[string]string{envContextFabricTeamsProjects: "false"}))
	if err != nil {
		t.Fatalf("loadProjector: %v", err)
	}
	if off.TeamsProjectsEnabled {
		t.Fatal("an explicit false must still disable team/project projection")
	}
}
