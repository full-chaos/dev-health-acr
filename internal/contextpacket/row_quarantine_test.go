package contextpacket_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAssembler_quarantines_only_invalid_row_within_source(t *testing.T) {
	// Given
	request := fixtureRequest("req-row-quarantine", "main", "")
	good := testEvidence("ev-good-row", "ci", time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
	good.SourceVersion = "ai_workflow_artifacts.v1"
	bad := testEvidence("ev-bad-row", "ci", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC))
	bad.SourceVersion = "ai_workflow_artifacts.v1"
	bad.Provenance = "unmapped"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{good, bad},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble row-local quarantine packet: %v", err)
	}
	if got := itemEvidenceIDs(packet); !equalStrings(got, []string{"ev-good-row"}) {
		t.Fatalf("quarantine retained evidence %v, want only the valid row", got)
	}
	const reason = "evidence_data_invalid:ai_workflow_artifacts.v1:invalid_provenance"
	if packet.Status != contractsv1.PacketPartial || !contains(packet.Warnings, reason) || !contains(packet.Coverage.DegradedReasons, reason) {
		t.Fatalf("quarantine was not disclosed as partial coverage: %#v", packet)
	}
}

func TestAssembler_preserves_unaffected_sources_when_quarantining(t *testing.T) {
	// Given
	request := fixtureRequest("req-source-quarantine", "main", "")
	affected := testEvidence("ev-affected-good", "ci", time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC))
	affected.SourceVersion = "ai_workflow_artifacts.v1"
	unaffected := testEvidence("ev-unaffected", "git", time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC))
	unaffected.SourceVersion = "git_commits.v1"
	bad := testEvidence("ev-affected-bad", "ci", time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
	bad.SourceVersion = "ai_workflow_artifacts.v1"
	bad.Provenance = "unmapped"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{affected, unaffected, bad},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble multi-source quarantine packet: %v", err)
	}
	ids := itemEvidenceIDs(packet)
	if len(ids) != 2 || !contains(ids, "ev-affected-good") || !contains(ids, "ev-unaffected") || contains(ids, "ev-affected-bad") {
		t.Fatalf("quarantine changed unaffected evidence: %v", ids)
	}
}

func TestAssembler_scope_mismatch_remains_fatal_before_row_quarantine(t *testing.T) {
	// Given
	request := fixtureRequest("req-scope-before-quarantine", "main", "")
	bad := testEvidence("ev-foreign-bad", "ci", time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
	bad.SourceVersion = "ai_workflow_artifacts.v1"
	bad.Provenance = "unmapped"
	store := testStore{
		scope: contractsv1.ResolvedScope{RepoID: "authorized", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		bundle: storage.EvidenceBundle{
			ResolvedScope: contractsv1.ResolvedScope{RepoID: "foreign", RepoSlug: "other-org/other-repo", Resolution: contractsv1.ScopeBranchFiltered},
			Evidence:      []contractsv1.EvidenceRef{bad},
			QueryVersion:  contextpacket.QueryVersionV1,
		},
	}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble scope-mismatched packet: %v", err)
	}
	const quarantineReason = "evidence_data_invalid:ai_workflow_artifacts.v1:invalid_provenance"
	if packet.Status != contractsv1.PacketDegraded || len(packet.Items) != 0 || !contains(packet.Warnings, "evidence_scope_mismatch") || contains(packet.Warnings, quarantineReason) {
		t.Fatalf("scope mismatch did not remain fatal: %#v", packet)
	}
}

func TestAssembler_excludes_quarantined_rows_from_watermarks(t *testing.T) {
	// Given
	request := fixtureRequest("req-quarantine-watermark", "main", "")
	validObserved := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	valid := testEvidence("ev-watermark-good", "ci", validObserved)
	valid.SourceVersion = "ai_workflow_artifacts.v1"
	badLatest := testEvidence("ev-watermark-bad", "ci", time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC))
	badLatest.SourceVersion = "ai_workflow_artifacts.v1"
	badLatest.Provenance = "unmapped"
	badOnly := testEvidence("ev-only-bad", "ci", time.Date(2026, 1, 15, 11, 30, 0, 0, time.UTC))
	badOnly.SourceVersion = "ai_review_outcomes.v1"
	badOnly.Provenance = "unmapped"
	badLatestObserved, badOnlyObserved := badLatest.ObservedAt, badOnly.ObservedAt
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{valid, badLatest, badOnly},
		Watermarks: []contractsv1.SourceWatermark{
			{Source: "ai_workflow_artifacts.v1", LastIngestedAt: &badLatestObserved, Status: "fresh"},
			{Source: "ai_review_outcomes.v1", LastIngestedAt: &badOnlyObserved, Status: "fresh"},
		},
		QueryVersion: contextpacket.QueryVersionV1,
	}}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble watermark quarantine packet: %v", err)
	}
	foundValidSource, foundQuarantinedSource := false, false
	for _, watermark := range packet.Freshness.Watermarks {
		switch watermark.Source {
		case "ai_workflow_artifacts.v1":
			foundValidSource = true
			if watermark.LastIngestedAt == nil || !watermark.LastIngestedAt.Equal(validObserved) || watermark.Status != "fresh" {
				t.Fatalf("valid-row watermark = %#v, want %s fresh", watermark, validObserved)
			}
		case "ai_review_outcomes.v1":
			foundQuarantinedSource = true
			if watermark.LastIngestedAt != nil || watermark.Status != "missing" {
				t.Fatalf("all-quarantined watermark retained dropped data: %#v", watermark)
			}
		}
	}
	if !foundValidSource || !foundQuarantinedSource {
		t.Fatalf("quarantine removed expected watermarks: %#v", packet.Freshness.Watermarks)
	}
}

func TestAssembler_degrades_when_all_rows_are_quarantined(t *testing.T) {
	// Given
	request := fixtureRequest("req-all-quarantined", "main", "")
	bad := testEvidence("ev-all-bad", "ci", time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
	bad.SourceVersion = "ai_workflow_artifacts.v1"
	bad.Provenance = "unmapped"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{bad},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble all-quarantined packet: %v", err)
	}
	const reason = "evidence_data_invalid:ai_workflow_artifacts.v1:invalid_provenance"
	if packet.Status != contractsv1.PacketDegraded || !packet.Coverage.Partial || len(packet.Items) != 0 || !contains(packet.Coverage.SourcesConsidered, "ai_workflow_artifacts.v1") || !contains(packet.Coverage.DegradedReasons, reason) || packet.Summary != "Evidence was retrieved but failed validation for the requested goal." {
		t.Fatalf("all-quarantined packet was not honestly degraded: %#v", packet)
	}
}
