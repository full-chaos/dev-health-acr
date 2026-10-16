package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

func TestContextPacketItemCauseEventIsOptionalAndBounded(t *testing.T) {
	at := time.Date(2026, 9, 12, 9, 30, 0, 0, time.UTC)
	item := ContextPacketItem{
		SchemaVersion: ContextPacketItemSchema, PacketItemID: "item-0001", Category: CategoryCause, ClaimKind: ClaimObserved,
		Title: "t", Summary: "s", WhyIncluded: "w", RuleID: "evidence.observed.cause.v1", Confidence: 1, Severity: SeverityWarning,
		RelatedEntities: []RelatedEntity{}, EvidenceRefIDs: []string{"acr:v1:pull_request:1"},
	}
	if err := item.Validate(); err != nil {
		t.Fatalf("item without cause_event: %v", err)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "cause_event") {
		t.Fatalf("an item without a cause event serializes one: %s", raw)
	}
	item.CauseEvent = CauseEvent{EntityType: "deployment", EntityID: "d-1", OccurredAt: at}
	if err := item.Validate(); err != nil {
		t.Fatalf("item with cause_event: %v", err)
	}
	item.CauseEvent = CauseEvent{EntityType: "deployment", EntityID: "d-1", OccurredAt: at}
	item.Category = CategoryState
	if err := item.Validate(); err == nil {
		t.Errorf("a cause_event on a state item validates")
	}
	item.Category = CategoryCause
	for name, event := range map[string]*CauseEvent{
		"no type":   {EntityID: "d-1", OccurredAt: at},
		"no id":     {EntityType: "deployment", OccurredAt: at},
		"zero time": {EntityType: "deployment", EntityID: "d-1"},
	} {
		item.CauseEvent = *event
		if err := item.Validate(); err == nil {
			t.Errorf("%s: a cause_event that violates the bounds validates", name)
		}
	}
}

func TestContextPacketItemCauseEventSchemaMatchesValidator(t *testing.T) {
	at := time.Date(2026, 9, 12, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		name     string
		category PacketCategory
		event    CauseEvent
		valid    bool
	}{
		{"cause deployment", CategoryCause, CauseEvent{EntityType: "deployment", EntityID: "d-1", OccurredAt: at}, true},
		{"cause repository", CategoryCause, CauseEvent{EntityType: "repository", EntityID: "r-1", OccurredAt: at}, false},
		{"state deployment", CategoryState, CauseEvent{EntityType: "deployment", EntityID: "d-1", OccurredAt: at}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := validItem()
			item.Category = tc.category
			item.CauseEvent = tc.event
			goErr := item.Validate()
			encoded, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_packet_item.v1.schema.json", encoded)
			if (goErr == nil) != tc.valid || (schemaErr == nil) != tc.valid {
				t.Fatalf("valid=%v want %v: go=%v schema=%v", tc.valid, tc.name, goErr, schemaErr)
			}
		})
	}
}

func TestMCPResponseSchemasPinCauseEventType(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "mcp_context_for_task_response.v1.schema.json"),
		filepath.Join("..", "..", "mcp", "schemas", "mcp_context_for_task_response.v1.schema.json"),
	} {
		raw, err := os.ReadFile(rel)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		found, conditionals := 0, 0
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				if ce, ok := x["cause_event"].(map[string]any); ok {
					found++
					props, _ := ce["properties"].(map[string]any)
					et, _ := props["entity_type"].(map[string]any)
					if et["const"] != "deployment" {
						t.Errorf("%s: cause_event.entity_type not pinned to deployment: %v", rel, et)
					}
				}
				if cond, ok := x["if"].(map[string]any); ok {
					if req, _ := cond["required"].([]any); len(req) == 1 && req[0] == "cause_event" {
						conditionals++
						then, _ := x["then"].(map[string]any)
						props, _ := then["properties"].(map[string]any)
						cat, _ := props["category"].(map[string]any)
						if cat["const"] != "cause" {
							t.Errorf("%s: cause_event conditional does not require category cause: %v", rel, cat)
						}
					}
				}
				for _, c := range x {
					walk(c)
				}
			case []any:
				for _, c := range x {
					walk(c)
				}
			}
		}
		walk(doc)
		if conditionals != 1 {
			t.Errorf("%s: cause_event conditionals = %d, want 1", rel, conditionals)
		}
		if found != 1 {
			t.Errorf("%s: cause_event definitions = %d, want 1", rel, found)
		}
	}
}

func TestContextPacketItemCauseEventDecodeAgreesWithSchema(t *testing.T) {
	item := validItem()
	item.Category = CategoryCause
	base, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		event string
		valid bool
	}{
		{"object", `{"entity_type":"deployment","entity_id":"d-1","occurred_at":"2026-09-12T09:30:00Z"}`, true},
		{"null", `null`, false},
		{"empty object", `{}`, false},
		{"missing time", `{"entity_type":"deployment","entity_id":"d-1"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(base, &doc); err != nil {
				t.Fatal(err)
			}
			doc["cause_event"] = json.RawMessage(tc.event)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			var got ContextPacketItem
			goErr := json.Unmarshal(raw, &got)
			if goErr == nil {
				goErr = got.ValidateRead()
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_packet_item.v1.schema.json", raw)
			if (goErr == nil) != tc.valid || (schemaErr == nil) != tc.valid {
				t.Fatalf("want valid=%v: go=%v schema=%v", tc.valid, goErr, schemaErr)
			}
		})
	}
}
