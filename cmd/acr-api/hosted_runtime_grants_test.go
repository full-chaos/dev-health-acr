package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/dbacl"
)

func fixtureDBACLFeatures() dbacl.Features {
	features := dbacl.Features{}
	for name, on := range runtimeFixtureFeatures {
		features[dbacl.Feature(name)] = on
	}
	return features
}

// The real-binary fixture grants the runtime role from internal/dbacl, the one
// source of record, so a table the runtime uses (the web assertion replay
// store, CHAOS-12394) cannot be missing from the fixture.
func TestRuntimeFixtureGrantsComeFromDBACL(t *testing.T) {
	const role = "acr_runtime"
	got := runtimeFixtureGrantStatements(role, postgresRuntimeAccessComplete)
	features := fixtureDBACLFeatures()
	for _, statement := range dbacl.GrantStatements(dbacl.QuoteIdent(role), features) {
		if !slices.Contains(got, statement.SQL) {
			t.Errorf("fixture does not apply %q", statement.SQL)
		}
	}
	for _, grant := range dbacl.RuntimeGrants {
		if !features.Enabled(grant.Feature) {
			continue
		}
		needle := "ON TABLE acr." + grant.Table + " TO "
		if !slices.ContainsFunc(got, func(sql string) bool { return strings.Contains(sql, needle) }) {
			t.Errorf("table acr.%s of dbacl.RuntimeGrants is not granted by the fixture", grant.Table)
		}
	}
}

func TestRuntimeFixtureFeaturesCoverTheFeaturesTheTestsEnable(t *testing.T) {
	// hostedRealIntegration starts a second process with ACR_ENABLE_EPISODE_WRITEBACK=true
	// against the same fixture database.
	if !fixtureDBACLFeatures().Enabled(dbacl.EpisodeWriteback) {
		t.Fatal("the fixture features do not enable episode writeback, which the hosted integration test turns on")
	}
}

func TestRuntimeFixtureMissingPacketInsertVariantRevokesInsert(t *testing.T) {
	const role = "acr_runtime"
	complete := runtimeFixtureGrantStatements(role, postgresRuntimeAccessComplete)
	variant := runtimeFixtureGrantStatements(role, postgresRuntimeAccessMissingPacketInsert)
	for _, sql := range complete {
		if strings.HasPrefix(sql, "REVOKE") {
			t.Errorf("the complete variant revokes: %q", sql)
		}
	}
	var revoked bool
	for _, sql := range variant {
		if strings.HasPrefix(sql, "REVOKE") && strings.Contains(sql, "INSERT") && strings.Contains(sql, "acr.context_packet_snapshots") {
			revoked = true
		}
	}
	if !revoked {
		t.Fatal("the missing-packet-insert variant does not revoke INSERT on acr.context_packet_snapshots")
	}
	if last := variant[len(variant)-1]; !strings.HasPrefix(last, "REVOKE") {
		t.Fatalf("the REVOKE must come after every GRANT, last statement = %q", last)
	}
}
