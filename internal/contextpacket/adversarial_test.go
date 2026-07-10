package contextpacket_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAssembler_rejects_bundle_outside_resolved_repository_scope(t *testing.T) {
	request := fixtureRequest("req-cross-repo", "main", "")
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "foreign", RepoSlug: "other-org/other-repo", Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{testEvidence("ev-foreign", "ci", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}
	store.scope = contractsv1.ResolvedScope{RepoID: "authorized", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered}

	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	if err != nil {
		t.Fatalf("assemble mismatched bundle: %v", err)
	}
	if packet.Status != contractsv1.PacketDegraded || packet.ResolvedScope.RepoID != "authorized" || len(packet.Items) != 0 || !contains(packet.Warnings, "evidence_scope_mismatch") {
		t.Fatalf("unexpected scope-mismatch packet: %#v", packet)
	}
}

func TestAssembler_excludes_undisplayable_evidence_content(t *testing.T) {
	request := fixtureRequest("req-redacted", "main", "")
	secret := testEvidence("ev-redacted", "ci", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	secret.Citation = "must-not-be-exposed"
	secret.Availability = contractsv1.EvidenceUnauthorized
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{secret},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	if err != nil {
		t.Fatalf("assemble undisplayable evidence: %v", err)
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	if packet.Status != contractsv1.PacketDegraded || len(packet.Items) != 0 || strings.Contains(string(encoded), secret.Citation) {
		t.Fatalf("undisplayable evidence leaked into packet: %s", encoded)
	}
}

func TestAssembler_filters_to_requested_categories(t *testing.T) {
	request := fixtureRequest("req-category", "main", "")
	request.Options.RequestedCategories = []contractsv1.PacketCategory{contractsv1.CategoryPressure}
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence: []contractsv1.EvidenceRef{
			testEvidence("ev-pressure", "ci", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			testEvidence("ev-cause", "git", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
		QueryVersion: contextpacket.QueryVersionV1,
	}}

	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	if err != nil {
		t.Fatalf("assemble category-filtered packet: %v", err)
	}
	if got := itemEvidenceIDs(packet); !equalStrings(got, []string{"ev-pressure"}) {
		t.Fatalf("category filter returned %v", got)
	}
}

func TestAssembler_degrades_schema_invalid_evidence_without_exposing_it(t *testing.T) {
	request := fixtureRequest("req-bad-evidence", "main", "")
	bad := testEvidence("ev-bad-evidence", "ci", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	bad.Confidence = math.Inf(1)
	bad.SourceVersion = strings.Repeat("s", 513)
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered, FallbackReasons: []string{}},
		Evidence:      []contractsv1.EvidenceRef{bad},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	if err != nil {
		t.Fatalf("assemble invalid evidence: %v", err)
	}
	if packet.Status != contractsv1.PacketDegraded || len(packet.Items) != 0 || !contains(packet.Warnings, "evidence_retrieval_unavailable") {
		t.Fatalf("invalid evidence was not degraded safely: %#v", packet)
	}
}

func TestAssembler_rejects_schema_invalid_resolved_scope(t *testing.T) {
	request := fixtureRequest("req-bad-scope", "main", "")
	scope := contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: "invalid slug", Resolution: contractsv1.ScopeBranchFiltered, FallbackReasons: []string{}}
	store := testStore{scope: scope, bundle: storage.EvidenceBundle{ResolvedScope: scope, QueryVersion: contextpacket.QueryVersionV1}}

	_, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	if err == nil {
		t.Fatal("assembler accepted schema-invalid resolved scope")
	}
}

func TestAssembler_uses_total_tie_breaker_and_serializes_empty_collections(t *testing.T) {
	request := fixtureRequest("req-tie", "main", "")
	first := testEvidence("ev-tie-item", "ci", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	first.Source.DisplayLabel, first.Citation = "z-label", "z-citation"
	second := first
	second.Source.DisplayLabel, second.Citation = "a-label", "a-citation"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{first, second},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	if err != nil {
		t.Fatalf("assemble tied packet: %v", err)
	}
	if packet.Items[0].Title != "a-label" {
		t.Fatalf("tie breaker retained %q", packet.Items[0].Title)
	}
	empty, err := contextpacket.NewAssembler(testStore{scope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered}, bundle: storage.EvidenceBundle{ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered}, QueryVersion: contextpacket.QueryVersionV1}}, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble empty packet: %v", err)
	}
	encoded, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty packet: %v", err)
	}
	for _, field := range []string{"\"items\":[]", "\"required_checks\":[]", "\"recommended_next_steps\":[]"} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("empty packet omitted array %s: %s", field, encoded)
		}
	}
}
