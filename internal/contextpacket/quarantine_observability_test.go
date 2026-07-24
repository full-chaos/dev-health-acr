package contextpacket_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAssembler_observes_quarantined_rows_without_leaking_hostile_content(t *testing.T) {
	// Given
	const hostile = "Bearer hostile-evidence-" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	request := fixtureRequest("req-quarantine-observability", "main", "")
	first := testEvidence("ev-first", "ci", time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
	first.SourceVersion, first.Provenance = "git_commits.v1", "unmapped"
	second := testEvidence("ev-second", "ci", time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC))
	second.SourceVersion, second.Provenance = "git_commits.v1", "unmapped"
	unsafe := testEvidence("ev-unsafe", hostile, time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC))
	unsafe.SourceVersion, unsafe.Provenance = hostile, hostile
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{first, second, unsafe},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}
	var output bytes.Buffer
	hooks := observability.NewHooks(observability.NewSlogSink(slog.New(slog.NewTextHandler(&output, nil))), nil)
	options := fixedOptions()
	options.Observer = observability.NewAssemblyObserver(hooks)

	// When
	_, err := contextpacket.NewAssembler(store, options).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble quarantine observability packet: %v", err)
	}
	logged := output.String()
	if !strings.Contains(logged, "evidence rows quarantined") || !strings.Contains(logged, "source=git_commits.v1") || !strings.Contains(logged, "rule_code=invalid_provenance") || !strings.Contains(logged, "dropped_rows=2") {
		t.Fatalf("quarantine log = %q, want safe git commits count", logged)
	}
	if strings.Contains(logged, hostile) || strings.Contains(logged, "ev-unsafe") {
		t.Fatalf("quarantine log leaked hostile evidence content: %q", logged)
	}
}

func TestAssembler_isolates_quarantine_observer_panics(t *testing.T) {
	// Given
	request := fixtureRequest("req-quarantine-observer-panic", "main", "")
	bad := testEvidence("ev-bad", "ci", time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
	bad.SourceVersion, bad.Provenance = "git_commits.v1", "unmapped"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{bad},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}
	options := fixedOptions()
	options.Observer = panicQuarantineObserver{}

	// When
	packet, err := contextpacket.NewAssembler(store, options).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble with panicking quarantine observer: %v", err)
	}
	if packet.Status != contractsv1.PacketDegraded {
		t.Fatalf("packet status = %q, want %q", packet.Status, contractsv1.PacketDegraded)
	}
}

type panicQuarantineObserver struct{}

func (panicQuarantineObserver) ObserveStoreQuery(context.Context, contextpacket.StoreQueryObservation) {
}

func (panicQuarantineObserver) ObserveRanking(context.Context, contextpacket.RankingObservation) {}

func (panicQuarantineObserver) ObservePacket(context.Context, contextpacket.PacketObservation) {}

func (panicQuarantineObserver) ObserveEvidenceQuarantine(context.Context, contextpacket.EvidenceQuarantineObservation) {
	panic("quarantine observer")
}
