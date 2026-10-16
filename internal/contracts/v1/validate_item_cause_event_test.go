package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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
	item.CauseEvent = &CauseEvent{EntityType: "deployment", EntityID: "d-1", OccurredAt: at}
	if err := item.Validate(); err != nil {
		t.Fatalf("item with cause_event: %v", err)
	}
	for name, event := range map[string]*CauseEvent{
		"no type":   {EntityID: "d-1", OccurredAt: at},
		"no id":     {EntityType: "deployment", OccurredAt: at},
		"zero time": {EntityType: "deployment", EntityID: "d-1"},
	} {
		item.CauseEvent = event
		if err := item.Validate(); err == nil {
			t.Errorf("%s: a cause_event that violates the bounds validates", name)
		}
	}
}
