package sidecar

import (
	"strings"
	"testing"
)

// CHAOS-7148: the catalog summary names the fact kinds read_facts serves, and
// says so plainly when it is not available.
func TestRenderDataCatalogSummaryFactsSection(t *testing.T) {
	served := RenderDataCatalogSummary([]byte(`{"contract_version":"acr-data.v1","sections":["facts"],"facts":{"served":true,"note":"n","kinds":[{"kind":"health","subject_kinds":["repository"],"fields":["repo_count"]},{"kind":"status","subject_kinds":["work_item"],"fields":["state"]}]},"caller":{"scopes":["context:read"],"grant_class":"restricted"}}`), DataTextMaxBytes)
	if !strings.Contains(served, "read_facts serves 2 fact kinds: health, status.") {
		t.Fatalf("served summary:\n%s", served)
	}
	if strings.Contains(served, "not available") {
		t.Fatalf("served summary says not available:\n%s", served)
	}
	unserved := RenderDataCatalogSummary([]byte(`{"contract_version":"acr-data.v1","sections":["facts"],"facts":{"served":false,"note":"n"},"caller":{"scopes":["context:read"],"grant_class":"restricted"}}`), DataTextMaxBytes)
	if !strings.Contains(unserved, "Facts: read_facts is not available in this deployment.") || strings.Contains(unserved, "serves") {
		t.Fatalf("unserved summary:\n%s", unserved)
	}
}
