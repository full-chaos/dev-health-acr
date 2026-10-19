package contextpacket_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestCIRunLabelRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, run, want string }{
		{"build", "7", "CI build #7"},
		{"", "7", "CI 7"},
		{"  \t\n", "7", "CI 7"},
		{"  build  ", "7", "CI build #7"},
		{strings.Repeat("a", 81), "7", "CI " + strings.Repeat("a", 80) + " #7"},
		{strings.Repeat("é", 81), "7", "CI " + strings.Repeat("é", 80) + " #7"},
		{"CI", "7", "CI #7"},
		{"ci", "7", "ci #7"},
		{"Ci", "7", "Ci #7"},
		{"  CI  ", "7", "CI #7"},
		{"CI build", "7", "CI build #7"},
		{"ci\tbuild", "7", "ci\tbuild #7"},
		{"CIX", "7", "CI CIX #7"},
		{"CI-build", "7", "CI CI-build #7"},
		{"build CI", "7", "CI build CI #7"},
		{"CI\u00a0build", "7", "CI\u00a0build #7"},
		{"CI\u2028build", "7", "CI\u2028build #7"},
		{"ci\u0085build", "7", "ci\u0085build #7"},
		{"CI\u3000build", "7", "CI\u3000build #7"},
		{"\u00a0", "7", "CI 7"},
		{"\u2028\u0085\u3000", "7", "CI 7"},
		{"\u00a0CI\u00a0", "7", "CI #7"},
		{"\u00a0build\u00a0", "7", "CI build #7"},
		{"CI\u200bbuild", "7", "CI CI\u200bbuild #7"},
	} {
		if got := contextpacket.CIRunLabel(tc.name, tc.run); got != tc.want {
			t.Errorf("CIRunLabel(%q, %q) = %q, want %q", tc.name, tc.run, got, tc.want)
		}
	}
}

func TestCIRunLabelSQLIsTheCatalogsLabel(t *testing.T) {
	t.Parallel()
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == "ci_pipeline_runs.v1" {
			if !strings.Contains(query.Statement, contextpacket.CIRunLabelSQL("c.pipeline_name", "c.run_id")+" display_label") {
				t.Fatal("ci_pipeline_runs.v1 does not build display_label from CIRunLabelSQL")
			}
			return
		}
	}
	t.Fatal("ci_pipeline_runs.v1 missing from the catalog")
}
