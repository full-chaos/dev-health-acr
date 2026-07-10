package v1

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

func TestContextPacketRequestValidate_matches_v1_boundaries(t *testing.T) {
	base := validRequest()
	base.RequestID = strings.Repeat("r", 256)
	base.Goal = strings.Repeat("g", 4000)
	base.Repository.Slug = "owner/repository"
	base.Client.Name = strings.Repeat("n", 200)
	base.Client.Version = strings.Repeat("v", 200)
	base.Client.SidecarVersion = strings.Repeat("s", 200)
	if err := base.Validate(); err != nil {
		t.Fatalf("maximal valid request: %v", err)
	}
	assertSchemaParity(t, "context_packet_request.v1.schema.json", base)
	cases := []struct {
		name   string
		mutate func(*ContextPacketRequest)
	}{
		{name: "request_id", mutate: func(value *ContextPacketRequest) { value.RequestID = strings.Repeat("r", 257) }},
		{name: "goal", mutate: func(value *ContextPacketRequest) { value.Goal = strings.Repeat("g", 4001) }},
		{name: "slug", mutate: func(value *ContextPacketRequest) { value.Repository.Slug = "invalid-slug" }},
		{name: "commit", mutate: func(value *ContextPacketRequest) { value.Scope.CommitSHA = "bad" }},
		{name: "duplicate_file", mutate: func(value *ContextPacketRequest) { value.Scope.Files = []string{"a.go", "a.go"} }},
		{name: "client", mutate: func(value *ContextPacketRequest) { value.Client.Name = strings.Repeat("n", 201) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := validRequest()
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted schema-invalid request")
			}
		})
	}
}

func TestContextPacketItemValidate_matches_v1_boundaries(t *testing.T) {
	base := validItem()
	base.PacketItemID = strings.Repeat("i", 256)
	base.Title = strings.Repeat("t", 500)
	base.Summary = strings.Repeat("s", 4000)
	base.WhyIncluded = strings.Repeat("w", 2000)
	base.RuleID = strings.Repeat("r", 256)
	base.RelatedEntities[0].Label = strings.Repeat("l", 1000)
	if err := base.Validate(); err != nil {
		t.Fatalf("maximal valid item: %v", err)
	}
	assertSchemaParity(t, "context_packet_item.v1.schema.json", base)
	cases := []struct {
		name   string
		mutate func(*ContextPacketItem)
	}{
		{name: "packet_item_id", mutate: func(value *ContextPacketItem) { value.PacketItemID = strings.Repeat("i", 257) }},
		{name: "title", mutate: func(value *ContextPacketItem) { value.Title = strings.Repeat("t", 501) }},
		{name: "summary", mutate: func(value *ContextPacketItem) { value.Summary = strings.Repeat("s", 4001) }},
		{name: "rule", mutate: func(value *ContextPacketItem) { value.RuleID = strings.Repeat("r", 257) }},
		{name: "entity", mutate: func(value *ContextPacketItem) { value.RelatedEntities[0].Label = strings.Repeat("l", 1001) }},
		{name: "evidence_id", mutate: func(value *ContextPacketItem) { value.EvidenceRefIDs = []string{"evidence-1", "evidence-1"} }},
		{name: "nil_related_entities", mutate: func(value *ContextPacketItem) { value.RelatedEntities = nil }},
		{name: "nil_evidence_ids", mutate: func(value *ContextPacketItem) { value.EvidenceRefIDs = nil }},
		{name: "non_finite_confidence", mutate: func(value *ContextPacketItem) { value.Confidence = math.Inf(1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := validItem()
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted schema-invalid item")
			}
		})
	}
}

func validRequest() ContextPacketRequest {
	return ContextPacketRequest{SchemaVersion: ContextPacketRequestSchema, RequestID: "request-1", Goal: "goal", Repository: RepositoryRef{Slug: "owner/repository"}, Options: PacketOptions{MaxItems: 1, MaxOutputTokens: 500, MaxSerializedBytes: 8192}, Client: ClientInfo{Name: "client", Version: "1"}}
}

func validItem() ContextPacketItem {
	return ContextPacketItem{SchemaVersion: ContextPacketItemSchema, PacketItemID: "item-0001", Category: CategoryEvidence, ClaimKind: ClaimObserved, Title: "title", Summary: "summary", WhyIncluded: "why", RuleID: "rule", Severity: SeverityInfo, RelatedEntities: []RelatedEntity{{Type: "type", ID: "id", Label: "label"}}, EvidenceRefIDs: []string{"evidence-1"}}
}

func assertSchemaParity(t *testing.T, schema string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	if err := contractcheck.ValidateSerialized("", schema, encoded); err != nil {
		t.Fatalf("schema rejected Go-valid value: %v", err)
	}
}
