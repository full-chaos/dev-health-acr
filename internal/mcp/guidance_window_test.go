package mcp

import (
	"encoding/json"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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

func TestReadFactsRequestSchemaDaysPointsAtCatalogNotNumbers(t *testing.T) {
	raw, err := schemaFiles.ReadFile("schemas/mcp_read_facts_request.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties struct {
			Window struct {
				Properties struct {
					Days struct {
						Description string `json:"description"`
						Maximum     int    `json:"maximum"`
					} `json:"days"`
				} `json:"properties"`
			} `json:"window"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if got, want := doc.Properties.Window.Properties.Days.Maximum, contractsv1.WidestRangeDays(); got != want {
		t.Fatalf("schema days maximum %d differs from the widest window in the table %d", got, want)
	}
	text := doc.Properties.Window.Properties.Days.Description
	if !strings.Contains(text, "max_window_days") {
		t.Fatalf("days description does not point at data_catalog max_window_days: %q", text)
	}
	for _, copy := range []string{"365", "60 days"} {
		if strings.Contains(text, copy) {
			t.Fatalf("days description holds a second copy of the window table: %q", copy)
		}
	}
}
