package contextpacket_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAssembler_uses_fixed_corpus_for_exact_commit_scope(t *testing.T) {
	// Given
	assembler := fixtureAssembler(t)
	request := fixtureRequest("req-exact", "main", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2")

	// When
	packet, err := assembler.Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble exact commit: %v", err)
	}
	if packet.Status != contractsv1.PacketComplete || packet.ResolvedScope.Resolution != contractsv1.ScopeExactCommit {
		t.Fatalf("unexpected packet scope/status: %#v", packet)
	}
	if got, want := itemEvidenceIDs(packet), []string{"ev-ci-checkout-001", "ev-commit-checkout-001"}; !equalStrings(got, want) {
		t.Fatalf("evidence order = %v, want %v", got, want)
	}
	if packet.Items[0].ClaimKind != contractsv1.ClaimObserved || packet.Items[1].ClaimKind != contractsv1.ClaimObserved {
		t.Fatalf("unexpected claim labels: %#v", packet.Items)
	}
}

func TestAssembler_applies_stable_branch_ranking_and_budgets(t *testing.T) {
	// Given
	assembler := fixtureAssembler(t)
	request := fixtureRequest("req-branch", "main", "")
	request.Options.MaxItems = 1

	// When
	packet, err := assembler.Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble branch: %v", err)
	}
	if packet.Status != contractsv1.PacketPartial || !packet.Budget.Truncated || packet.Budget.ItemsUsed != 1 {
		t.Fatalf("unexpected budget/status: %#v", packet)
	}
	if got := itemEvidenceIDs(packet); !equalStrings(got, []string{"ev-pr-auth-002"}) {
		t.Fatalf("evidence order = %v", got)
	}
	if packet.Items[0].Rank != 1 || packet.Budget.EstimatedTokens > packet.Budget.MaxOutputTokens || packet.Budget.SerializedBytes > packet.Budget.MaxSerializedBytes {
		t.Fatalf("invalid bounded packet: %#v", packet)
	}
}

func TestAssembler_marks_empty_corpus_scope_without_fabricating_items(t *testing.T) {
	// Given
	assembler := fixtureAssembler(t)
	request := fixtureRequest("req-empty", "release/1.4-unindexed", "")

	// When
	packet, err := assembler.Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble empty scope: %v", err)
	}
	if packet.Status != contractsv1.PacketEmpty || len(packet.Items) != 0 || !contains(packet.Warnings, "no_evidence_found") {
		t.Fatalf("unexpected empty packet: %#v", packet)
	}
}

func TestAssembler_marks_partial_and_timeout_retrieval(t *testing.T) {
	// Given
	request := fixtureRequest("req-degraded", "main", "")
	partial := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{testEvidence("ev-item-1", "ci", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
		Unavailable:   []contractsv1.UnavailableSource{{Source: "linear", Reason: "lagging"}},
		Watermarks:    []contractsv1.SourceWatermark{{Source: "ci", Status: "stale"}},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	// When
	partialPacket, err := contextpacket.NewAssembler(partial, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	timeoutPacket, timeoutErr := contextpacket.NewAssembler(testStore{err: context.DeadlineExceeded}, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil || partialPacket.Status != contractsv1.PacketPartial || !contains(partialPacket.Warnings, "source_stale:ci") || !contains(partialPacket.Warnings, "source_unavailable:linear:lagging") {
		t.Fatalf("unexpected partial packet/error: %#v %v", partialPacket, err)
	}
	if timeoutErr != nil || timeoutPacket.Status != contractsv1.PacketDegraded || !contains(timeoutPacket.Warnings, "evidence_retrieval_timed_out") {
		t.Fatalf("unexpected timeout packet/error: %#v %v", timeoutPacket, timeoutErr)
	}
}

func fixtureAssembler(t *testing.T) *contextpacket.Assembler {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	corpus, err := evalfixture.VerifyCorpus(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "evaluation", "v1"))
	if err != nil {
		t.Fatalf("verify corpus: %v", err)
	}
	store, err := contextpacket.NewEvaluationStore(corpus, "org-fixture")
	if err != nil {
		t.Fatalf("create fixture store: %v", err)
	}
	return contextpacket.NewAssembler(store, fixedOptions())
}

func fixedOptions() contextpacket.Options {
	return contextpacket.Options{Now: func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) }, ServiceVersion: "test", MinimumSidecarVersion: "0.1.0"}
}

func fixturePrincipal() storage.Principal {
	return storage.Principal{OrgID: "org-fixture", RepositoryScopes: []string{"example-org/widget-service"}}
}

func fixtureRequest(requestID, branch, commit string) contractsv1.ContextPacketRequest {
	if len(requestID) < 8 {
		requestID += "-valid"
	}
	return contractsv1.ContextPacketRequest{SchemaVersion: contractsv1.ContextPacketRequestSchema, RequestID: requestID, Goal: "Investigate fixture evidence", Repository: contractsv1.RepositoryRef{Slug: "example-org/widget-service"}, Scope: contractsv1.RequestedScope{Branch: branch, CommitSHA: commit}, Options: contractsv1.PacketOptions{MaxItems: 10, MaxOutputTokens: 500, MaxSerializedBytes: 8192}, Client: contractsv1.ClientInfo{Name: "test", Version: "1.0"}}
}

type testStore struct {
	bundle storage.EvidenceBundle
	err    error
	scope  contractsv1.ResolvedScope
}

func (s testStore) ResolveScope(_ context.Context, _ storage.Principal, _ contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	if s.scope.RepoID != "" {
		return s.scope, s.err
	}
	return s.bundle.ResolvedScope, s.err
}

func (s testStore) ContextForTask(_ context.Context, _ storage.Principal, _ contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	return s.bundle, s.err
}

func (s testStore) ResolveEvidence(_ context.Context, _ storage.Principal, _ string) (contractsv1.ExpandedEvidence, error) {
	return contractsv1.ExpandedEvidence{}, errors.New("not implemented")
}

func testEvidence(id, system string, observed time.Time) contractsv1.EvidenceRef {
	entityType := "check"
	if system == "git" {
		entityType = "commit"
	}
	if system == "github_pr" {
		entityType = "review_action"
	}
	return contractsv1.EvidenceRef{SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: id, Source: contractsv1.EvidenceSource{System: system, EntityType: entityType, EntityID: id, DisplayLabel: id}, Provenance: "native", Confidence: 0.9, Citation: "fixture", ObservedAt: observed, Availability: contractsv1.EvidenceAvailable}
}

func itemEvidenceIDs(packet contractsv1.ContextPacket) []string {
	ids := make([]string, 0, len(packet.Items))
	for _, item := range packet.Items {
		ids = append(ids, item.EvidenceRefIDs[0])
	}
	return ids
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}
