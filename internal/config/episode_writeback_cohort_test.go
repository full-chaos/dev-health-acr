package config

import (
	"strings"
	"testing"
)

// CHAOS-3565: episode writeback must be config-only enablement scoped to
// dev/acceptance (this codebase's fullstack acceptance stack runs with
// ACR_ENVIRONMENT=development -- see scripts/e2e/compose.sh) and to an
// explicitly configured design-partner cohort of org IDs. Never production.

func TestLoad_episodeWritebackRejectsNonDevelopmentEnvironments(t *testing.T) {
	for _, environment := range []string{"test", "staging", "production"} {
		t.Run(environment, func(t *testing.T) {
			values := completeRuntimeEnvironment()
			values["ACR_ENVIRONMENT"] = environment
			values["ACR_ENABLE_EPISODE_WRITEBACK"] = "true"
			values["ACR_EPISODE_WRITEBACK_COHORT_ORG_IDS"] = "org_design_partner_1"

			_, err := load(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), "ACR_ENABLE_EPISODE_WRITEBACK") || !strings.Contains(err.Error(), "development") {
				t.Fatalf("load() error = %v, want an ACR_ENABLE_EPISODE_WRITEBACK/development rejection for %s", err, environment)
			}
		})
	}
}

func TestLoad_episodeWritebackRequiresNonEmptyCohort(t *testing.T) {
	values := completeRuntimeEnvironment()
	values["ACR_ENVIRONMENT"] = "development"
	values["ACR_ENABLE_EPISODE_WRITEBACK"] = "true"
	delete(values, "ACR_EPISODE_WRITEBACK_COHORT_ORG_IDS")

	_, err := load(mapLookup(values))
	if err == nil || !strings.Contains(err.Error(), "ACR_EPISODE_WRITEBACK_COHORT_ORG_IDS") {
		t.Fatalf("load() error = %v, want an ACR_EPISODE_WRITEBACK_COHORT_ORG_IDS rejection", err)
	}
}

func TestLoad_episodeWritebackAcceptsDevelopmentWithCohort(t *testing.T) {
	values := completeRuntimeEnvironment()
	values["ACR_ENVIRONMENT"] = "development"
	values["ACR_ENABLE_EPISODE_WRITEBACK"] = "true"
	values["ACR_EPISODE_WRITEBACK_COHORT_ORG_IDS"] = " org_design_partner_1 , org_design_partner_2 "

	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatalf("load(): %v", err)
	}
	if !cfg.EnableEpisodeWriteback {
		t.Fatal("episode writeback was not enabled")
	}
	if len(cfg.EpisodeWritebackCohortOrgIDs) != 2 || cfg.EpisodeWritebackCohortOrgIDs[0] != "org_design_partner_1" || cfg.EpisodeWritebackCohortOrgIDs[1] != "org_design_partner_2" {
		t.Fatalf("cohort org ids = %#v, want trimmed [org_design_partner_1 org_design_partner_2]", cfg.EpisodeWritebackCohortOrgIDs)
	}
}

func TestLoad_episodeWritebackCohortIsIgnoredWhenDisabled(t *testing.T) {
	values := completeRuntimeEnvironment()
	values["ACR_ENVIRONMENT"] = "production"
	delete(values, "ACR_ENABLE_EPISODE_WRITEBACK")
	delete(values, "ACR_EPISODE_WRITEBACK_COHORT_ORG_IDS")

	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatalf("load(): %v", err)
	}
	if cfg.EnableEpisodeWriteback || len(cfg.EpisodeWritebackCohortOrgIDs) != 0 {
		t.Fatalf("cfg = %#v, want writeback disabled and no cohort by default", cfg)
	}
}
