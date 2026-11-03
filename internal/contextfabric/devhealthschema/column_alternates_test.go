package devhealthschema_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

func TestChangeFailureRateAcceptsFloat64AndNullableFloat64(t *testing.T) {
	for _, typ := range []string{"Float64", "Nullable(Float64)"} {
		if !devhealthschema.AcceptsColumnType("repo_metrics_daily", "change_failure_rate", typ) {
			t.Fatalf("change_failure_rate must accept %s", typ)
		}
		ddl := devhealthschema.DDLWithColumnType("", "repo_metrics_daily", "change_failure_rate", typ)
		if !strings.Contains(ddl, "change_failure_rate "+typ+",") {
			t.Fatalf("DDL for %s does not carry the type: %s", typ, ddl)
		}
	}
	if devhealthschema.AcceptsColumnType("repo_metrics_daily", "change_failure_rate", "String") {
		t.Fatal("an undeclared type must be rejected")
	}
	if devhealthschema.AcceptsColumnType("repo_metrics_daily", "commits_count", "Nullable(UInt32)") {
		t.Fatal("an alternate must not leak to another column")
	}
}

// ops migration 124 widens the team counters to Float64; a deployment may carry
// either type, and a fixture is rendered for each.
func TestTeamMetricsDailyCountersAcceptUInt32AndFloat64(t *testing.T) {
	counters := []string{"commits_count", "after_hours_commits_count", "weekend_commits_count"}
	for _, typ := range []string{"UInt32", "Float64"} {
		types := map[string]string{}
		for _, column := range counters {
			if !devhealthschema.AcceptsColumnType("team_metrics_daily", column, typ) {
				t.Fatalf("%s must accept %s", column, typ)
			}
			types[column] = typ
		}
		ddl := devhealthschema.DDLWithColumnTypes("", "team_metrics_daily", types)
		for _, column := range counters {
			if !strings.Contains(ddl, column+" "+typ+",") {
				t.Fatalf("DDL for %s does not carry %s: %s", column, typ, ddl)
			}
		}
	}
	if devhealthschema.AcceptsColumnType("team_metrics_daily", "commits_count", "UInt64") {
		t.Fatal("an undeclared type must be rejected")
	}
	if devhealthschema.AcceptsColumnType("repo_metrics_daily", "commits_count", "Float64") {
		t.Fatal("the repository counter stays an integer: the alternate must not leak")
	}
}

// ops migration 146 makes the landscape cycle Nullable(Float64): an identity
// with no merged pull request in its window holds NULL. acr serves the column
// before and after the migration, and a fixture is rendered for each type.
func TestLandscapeCycleAcceptsFloat64AndNullableFloat64(t *testing.T) {
	for _, typ := range []string{"Float64", "Nullable(Float64)"} {
		if !devhealthschema.AcceptsColumnType("ic_landscape_rolling_30d", "cycle_p50_30d_hours", typ) {
			t.Fatalf("cycle_p50_30d_hours must accept %s", typ)
		}
		ddl := devhealthschema.DDLWithColumnType("", "ic_landscape_rolling_30d", "cycle_p50_30d_hours", typ)
		if !strings.Contains(ddl, "cycle_p50_30d_hours "+typ+",") {
			t.Fatalf("DDL for %s does not carry the type: %s", typ, ddl)
		}
	}
	if devhealthschema.AcceptsColumnType("ic_landscape_rolling_30d", "x_raw", "Nullable(Float64)") {
		t.Fatal("the alternate must not leak to another landscape column")
	}
}
