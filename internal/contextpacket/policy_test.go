package contextpacket_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestBuildReadPlanV1_uses_authenticated_organization_and_scoped_parameters(t *testing.T) {
	request := fixtureRequest("req-plan", "main", "a1b2")
	principal := fixturePrincipal()

	plan, err := contextpacket.BuildReadPlanV1(principal, request)

	if err != nil {
		t.Fatalf("build read plan: %v", err)
	}
	if plan.Version != contextpacket.QueryVersionV1 || plan.OrgID != principal.OrgID || plan.RepoSlug != request.Repository.Slug || plan.Branch != request.Scope.Branch || plan.CommitSHA != request.Scope.CommitSHA {
		t.Fatalf("unexpected scoped plan: %#v", plan)
	}
	for _, predicate := range []string{"repo_id = {repo_id:UUID}", "hash = {commit_sha:String}", "committer_when <= {as_of:Nullable(DateTime)}", "time_window_days:UInt16"} {
		if !strings.Contains(plan.Statement, predicate) {
			t.Fatalf("read query missing scoped predicate %q: %s", predicate, plan.Statement)
		}
	}
}

func TestAssembler_uses_stable_category_tie_order_and_deduplicates_evidence(t *testing.T) {
	request := fixtureRequest("req-ranked", "main", "")
	observedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence: []contractsv1.EvidenceRef{
			testEvidence("ev-duplicate", "git", observedAt),
			testEvidence("ev-item-b", "ci", observedAt),
			testEvidence("ev-duplicate", "ci", observedAt),
			testEvidence("ev-item-a", "ci", observedAt),
		},
		QueryVersion: contextpacket.QueryVersionV1,
	}}

	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	if err != nil {
		t.Fatalf("assemble ranked packet: %v", err)
	}
	if got, want := itemEvidenceIDs(packet), []string{"ev-duplicate", "ev-item-a", "ev-item-b"}; !equalStrings(got, want) {
		t.Fatalf("evidence order = %v, want %v", got, want)
	}
	if packet.Items[0].ClaimKind != contractsv1.ClaimObserved || packet.Items[0].Category != contractsv1.CategoryPressure {
		t.Fatalf("duplicate did not retain higher-provenance item: %#v", packet.Items[2])
	}
}

func TestAssembler_truncates_when_token_or_byte_budget_is_exhausted(t *testing.T) {
	request := fixtureRequest("req-budget", "main", "")
	large := testEvidence("ev-large", "ci", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	large.Citation = strings.Repeat("e", 1_999)
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{large},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	if err != nil {
		t.Fatalf("assemble budget packet: %v", err)
	}
	if !packet.Budget.Truncated || packet.Budget.ItemsUsed != 0 || packet.Budget.EstimatedTokens != 0 || !contains(packet.Warnings, "context_truncated") {
		t.Fatalf("unexpected exhausted budget: %#v", packet.Budget)
	}
	byteRequest := fixtureRequest("req-byte-budget", "main", "")
	byteRequest.Options.MaxOutputTokens = 16_000
	large.Citation = strings.Repeat("e", 1_999)
	byteEvidence := make([]contractsv1.EvidenceRef, 0, 10)
	for index := range 10 {
		copy := large
		copy.EvidenceRefID, copy.Source.EntityID, copy.Source.DisplayLabel = fmt.Sprintf("ev-byte-%03d", index), fmt.Sprintf("entity-byte-%03d", index), fmt.Sprintf("byte evidence %03d", index)
		byteEvidence = append(byteEvidence, copy)
	}
	byteStore := testStore{bundle: storage.EvidenceBundle{ResolvedScope: store.bundle.ResolvedScope, Evidence: byteEvidence, QueryVersion: contextpacket.QueryVersionV1}}
	bytePacket, byteErr := contextpacket.NewAssembler(byteStore, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), byteRequest)
	if byteErr != nil {
		t.Fatalf("assemble byte-budget packet: %v", byteErr)
	}
	if !bytePacket.Budget.Truncated || bytePacket.Budget.ItemsUsed == 0 || bytePacket.Budget.SerializedBytes > bytePacket.Budget.MaxSerializedBytes {
		t.Fatalf("unexpected byte budget: %#v", bytePacket.Budget)
	}
}

func TestAssembler_marks_only_fresh_or_stale_catalog_sources_available(t *testing.T) {
	// Given
	request := fixtureRequest("req-coverage", "main", "")
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Watermarks: []contractsv1.SourceWatermark{
			{Source: "fresh-source", Status: "fresh"},
			{Source: "stale-source", Status: "stale"},
			{Source: "missing-source", Status: "missing"},
			{Source: "unavailable-source", Status: "unavailable"},
		},
		Unavailable:  []contractsv1.UnavailableSource{{Source: "missing-source", Reason: "no_evidence"}, {Source: "unavailable-source", Reason: "source_unavailable"}},
		QueryVersion: contextpacket.QueryVersionV1,
	}}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble catalog coverage packet: %v", err)
	}
	if got, want := packet.Coverage.SourcesAvailable, []string{"fresh-source", "stale-source"}; !equalStrings(got, want) {
		t.Fatalf("available sources = %v, want %v", got, want)
	}
}
