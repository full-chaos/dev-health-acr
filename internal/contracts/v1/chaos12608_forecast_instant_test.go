package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

// chaos12608_forecast_instant_test.go holds the lead's ruling on CHAOS-12608:
// the forecast summary's computed_at is an RFC 3339 UTC instant and every date
// an RFC 3339 full-date, in the Go validator and in every published schema.
// ClickHouse's own text ("2026-11-02 04:00:00.000") names no zone, and a
// browser reads it as local time.

func instantSummaryClaim(computedAt, p50Date string) ContextFabricClaimedFact {
	fact := claimedFactWithTable(ContextFabricFactTableShapeDistribution, distributionRows(3))
	fact.Kind = ContextFabricFactWorkload
	fact.CompletionForecast = &ContextFabricClaimedCompletionForecast{
		State: ContextFabricCompletionDistributionAvailable, ComputedAt: computedAt, P50Date: p50Date,
	}
	return fact
}

func TestTheForecastSummaryStatesAUTCInstantAndFullDates(t *testing.T) {
	for _, tc := range []struct {
		name, computedAt, p50Date string
		valid                     bool
	}{
		{"a UTC instant", "2026-11-02T04:00:00Z", "2026-11-20", true},
		{"a UTC instant with a fraction", "2026-11-02T04:00:00.123Z", "", true},
		{"ClickHouse's own text", "2026-11-02 04:00:00.000", "", false},
		{"an instant with an offset", "2026-11-02T04:00:00+02:00", "", false},
		{"an instant with no zone", "2026-11-02T04:00:00", "", false},
		{"a date for an instant", "2026-11-02", "", false},
		{"no real month", "2026-13-02T04:00:00Z", "", false},
		{"no instant", "", "", false},
		{"a date with a time", "2026-11-02T04:00:00Z", "2026-11-20T00:00:00Z", false},
		{"ClickHouse's date and time", "2026-11-02T04:00:00Z", "2026-11-20 00:00:00", false},
		{"no real day", "2026-11-02T04:00:00Z", "2026-02-30", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := instantSummaryClaim(tc.computedAt, tc.p50Date).Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("Validate() = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}

// TestEverySchemaStatesTheSummaryForms walks every published copy of the
// summary definitions: computed_at is format date-time with the Go pattern,
// and every date carries the Go full-date pattern.
func TestEverySchemaStatesTheSummaryForms(t *testing.T) {
	files := []string{
		filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "context_fabric_common.v1.schema.json"),
		filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "context_fabric_answer_projection.v1.schema.json"),
		filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "mcp_investigate_question_response.v1.schema.json"),
		filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "mcp_investigation_result_response.v1.schema.json"),
		filepath.Join("..", "..", "mcp", "schemas", "mcp_investigate_question_response.v1.schema.json"),
		filepath.Join("..", "..", "mcp", "schemas", "mcp_investigation_result_response.v1.schema.json"),
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		found := 0
		var walk func(node any)
		walk = func(node any) {
			switch value := node.(type) {
			case map[string]any:
				for _, name := range []string{"ClaimedCompletionForecast", "ProjectedCompletionForecast"} {
					definition, ok := value[name].(map[string]any)
					if !ok {
						continue
					}
					properties, _ := definition["properties"].(map[string]any)
					if properties == nil {
						continue
					}
					found++
					computedAt, _ := properties["computed_at"].(map[string]any)
					if computedAt["format"] != "date-time" || computedAt["pattern"] != ContextFabricForecastComputedAtPattern {
						t.Errorf("%s %s.computed_at = %v, want format date-time and the Go pattern", file, name, computedAt)
					}
					for _, field := range []string{"forecast_p50_date", "forecast_p85_date", "forecast_p95_date"} {
						date, _ := properties[field].(map[string]any)
						if date["pattern"] != ContextFabricForecastDatePattern {
							t.Errorf("%s %s.%s = %v, want the Go full-date pattern", file, name, field, date)
						}
					}
				}
				for _, child := range value {
					walk(child)
				}
			case []any:
				for _, child := range value {
					walk(child)
				}
			}
		}
		walk(document)
		if found == 0 {
			t.Errorf("%s: no summary definition found: the walk measured nothing", file)
		}
	}
}

// TestThePublishedSchemaRefusesAZonelessInstant validates claimed facts
// against the published common schema itself.
func TestThePublishedSchemaRefusesAZonelessInstant(t *testing.T) {
	common, err := filepath.Abs(filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "context_fabric_common.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	loader := gojsonschema.NewSchemaLoader()
	if err := loader.AddSchemas(gojsonschema.NewReferenceLoader("file://" + common)); err != nil {
		t.Fatalf("add common schema: %v", err)
	}
	schema, err := loader.Compile(gojsonschema.NewGoLoader(map[string]any{"$ref": "https://contracts.fullchaos.dev/acr/v1/context_fabric_common.v1.schema.json#/$defs/ClaimedFact"}))
	if err != nil {
		t.Fatalf("compile ClaimedFact: %v", err)
	}
	valid := func(fact ContextFabricClaimedFact) bool {
		t.Helper()
		encoded, err := json.Marshal(fact)
		if err != nil {
			t.Fatal(err)
		}
		report, err := schema.Validate(gojsonschema.NewBytesLoader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		return report.Valid()
	}
	if !valid(instantSummaryClaim("2026-11-02T04:00:00Z", "2026-11-20")) {
		t.Error("a UTC instant and a full-date refused by the schema")
	}
	for _, tc := range []struct{ computedAt, p50Date string }{
		{"2026-11-02 04:00:00.000", ""},
		{"2026-11-02T04:00:00+02:00", ""},
		{"2026-11-02T04:00:00Z", "2026-11-20 00:00:00"},
	} {
		if valid(instantSummaryClaim(tc.computedAt, tc.p50Date)) {
			t.Errorf("computed_at %q, p50 date %q admitted by the schema", tc.computedAt, tc.p50Date)
		}
	}
}

// CHAOS-12634: the item target is a count: 0 and up, and absent is absent.
func TestTheSummaryItemTargetIsACount(t *testing.T) {
	fact := instantSummaryClaim("2026-11-02T04:00:00Z", "")
	for _, target := range []int64{0, 40} {
		value := target
		fact.CompletionForecast.TargetItems = &value
		if err := fact.Validate(); err != nil {
			t.Fatalf("target_items %d: Validate() = %v", target, err)
		}
	}
	negative := int64(-1)
	fact.CompletionForecast.TargetItems = &negative
	if err := fact.Validate(); err == nil {
		t.Fatal("target_items -1 validated")
	}
}
