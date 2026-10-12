package mcp

import (
	"strings"
	"testing"
)

func TestGuidanceInvestmentScopesPointsAtCatalogForWindow(t *testing.T) {
	if !strings.Contains(GuidanceInvestmentScopes, "max_window_days") {
		t.Fatal("guidance does not send the client to data_catalog max_window_days")
	}
	for _, copy := range []string{"365", "stay at 60", "up to 60"} {
		if strings.Contains(GuidanceInvestmentScopes, copy) {
			t.Fatalf("guidance holds a second copy of the window table: %q", copy)
		}
	}
}
