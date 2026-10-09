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
