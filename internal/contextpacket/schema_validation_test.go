package contextpacket_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAssembler_serializes_every_fixture_packet_against_canonical_v1_schema(t *testing.T) {
	// Given
	assembler := fixtureAssembler(t)
	requests := []contractsv1.ContextPacketRequest{
		fixtureRequest("schema-exact", "main", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"),
		fixtureRequest("schema-branch", "main", ""),
		fixtureRequest("schema-empty", "release/1.4-unindexed", ""),
	}

	for _, request := range requests {
		t.Run(request.RequestID, func(t *testing.T) {
			// When
			packet, err := assembler.Assemble(context.Background(), fixturePrincipal(), request)
			if err != nil {
				t.Fatalf("assemble packet: %v", err)
			}
			encoded, err := json.Marshal(packet)
			if err != nil {
				t.Fatalf("marshal packet: %v", err)
			}

			// Then
			if err := contractcheck.ValidateSerialized("", "context_packet.v1.schema.json", encoded); err != nil {
				t.Fatalf("context packet violates canonical schema: %v\n%s", err, encoded)
			}
			if packet.Budget.SerializedBytes != len(encoded) {
				t.Fatalf("serialized bytes = %d, want %d", packet.Budget.SerializedBytes, len(encoded))
			}
		})
	}
}

func TestAssembler_serializes_repo_fallback_and_unresolved_packets_against_canonical_v1_schema(t *testing.T) {
	// Given
	request := fixtureRequest("schema-fallback", "", "")
	cases := []struct {
		name  string
		scope contractsv1.ResolvedScope
	}{
		{name: "repo_fallback", scope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeRepoFallback, FallbackReasons: []string{"branch_not_requested"}}},
		{name: "unresolved", scope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeUnresolved, FallbackReasons: []string{"scope_resolution_unavailable"}}},
		{name: "unresolved_without_repository_id", scope: contractsv1.ResolvedScope{RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeUnresolved, FallbackReasons: []string{"authorized_repository_not_found"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When
			store := testStore{scope: tc.scope, bundle: storage.EvidenceBundle{ResolvedScope: tc.scope, QueryVersion: "context-query.v1"}}
			packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
			if err != nil {
				t.Fatalf("assemble %s packet: %v", tc.name, err)
			}
			encoded, err := json.Marshal(packet)
			if err != nil {
				t.Fatalf("marshal packet: %v", err)
			}

			// Then
			if err := contractcheck.ValidateSerialized("", "context_packet.v1.schema.json", encoded); err != nil {
				t.Fatalf("%s packet violates canonical schema: %v\n%s", tc.name, err, encoded)
			}
		})
	}
}

func TestAssembler_validates_request_evidence_items_and_packet_with_canonical_schemas(t *testing.T) {
	// Given
	request := fixtureRequest("schema-components", "main", "")
	evidence := testEvidence("ev-schema-components", "ci", fixedOptions().Now())
	assembler := contextpacket.NewAssembler(testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered, FallbackReasons: []string{}},
		Evidence:      []contractsv1.EvidenceRef{evidence},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}, fixedOptions())

	// When
	packet, err := assembler.Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble packet: %v", err)
	}

	// Then
	assertCanonicalSchema(t, "context_packet_request.v1.schema.json", request)
	assertCanonicalSchema(t, "evidence_ref.v1.schema.json", evidence)
	assertCanonicalSchema(t, "context_packet_item.v1.schema.json", packet.Items[0])
	assertCanonicalSchema(t, "context_packet.v1.schema.json", packet)
}

func assertCanonicalSchema(t *testing.T, schema string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", schema, err)
	}
	if err := contractcheck.ValidateSerialized("", schema, encoded); err != nil {
		t.Fatalf("%s validation: %v\n%s", schema, err, encoded)
	}
}
