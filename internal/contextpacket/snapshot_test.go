package contextpacket_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestAssembler_savesExactSnapshotAfterEvidenceChanges(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	snapshots := memory.NewPacketStore(func() time.Time { return now })
	request := fixtureRequest("req-snapshot", "main", "")
	source := &testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{testEvidence("ev-snapshot-001", "ci", now)},
	}}
	options := fixedOptions()
	options.SnapshotStore = snapshots
	packet, err := contextpacket.NewAssembler(source, options).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble snapshot packet: %v", err)
	}
	want, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	source.bundle.Evidence[0].Citation = "changed after snapshot"
	replayed, err := snapshots.GetSnapshot(context.Background(), fixturePrincipal(), packet.ContextPacketID)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	got, err := json.Marshal(replayed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("replayed snapshot changed after evidence mutation\n got: %s\nwant: %s", got, want)
	}
}

func TestAssembler_failsWhenSnapshotSaveIsInterrupted(t *testing.T) {
	options := fixedOptions()
	options.SnapshotStore = failingPacketStore{}
	_, err := contextpacket.NewAssembler(testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: "example-org/widget-service", Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{testEvidence("ev-save-failure", "ci", time.Now().UTC())},
	}}, options).Assemble(context.Background(), fixturePrincipal(), fixtureRequest("req-save-failure", "main", ""))
	if err == nil {
		t.Fatal("assemble succeeded after snapshot save interruption")
	}
}

func TestAssembler_preservesDegradedTimeoutWhenSnapshotContextExpires(t *testing.T) {
	options := fixedOptions()
	options.SnapshotStore = failingPacketStore{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	packet, err := contextpacket.NewAssembler(timeoutEvidenceStore{}, options).Assemble(ctx, fixturePrincipal(), fixtureRequest("req-deadline-snapshot", "main", ""))
	if err != nil || packet.Status != contractsv1.PacketDegraded {
		t.Fatalf("timeout packet/error = (%#v, %v), want degraded packet and nil error", packet, err)
	}
}

type failingPacketStore struct{}

func (failingPacketStore) SaveSnapshot(context.Context, storage.Principal, contractsv1.ContextPacket, time.Time) error {
	return errors.New("interrupted")
}

func (failingPacketStore) GetSnapshot(context.Context, storage.Principal, string) (contractsv1.ContextPacket, error) {
	return contractsv1.ContextPacket{}, errors.New("interrupted")
}

func (failingPacketStore) PurgeExpired(context.Context, time.Time, int) (int, error) {
	return 0, errors.New("interrupted")
}

type timeoutEvidenceStore struct{}

func (timeoutEvidenceStore) ResolveScope(ctx context.Context, _ storage.Principal, _ contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	<-ctx.Done()
	return contractsv1.ResolvedScope{}, context.DeadlineExceeded
}

func (timeoutEvidenceStore) ContextForTask(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	return storage.EvidenceBundle{}, errors.New("not reached")
}

func (timeoutEvidenceStore) ResolveEvidence(context.Context, storage.Principal, string) (contractsv1.ExpandedEvidence, error) {
	return contractsv1.ExpandedEvidence{}, errors.New("not used")
}
