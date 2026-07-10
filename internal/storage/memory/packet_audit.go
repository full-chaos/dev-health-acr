package memory

import (
	"encoding/json"
	"errors"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func snapshotPurgeEvent(id string, snapshot packetSnapshot, cutoff, createdAt time.Time) (storage.AuditEvent, error) {
	var packet contractsv1.ContextPacket
	if err := json.Unmarshal(snapshot.payload, &packet); err != nil {
		return storage.AuditEvent{}, errors.New("decode packet snapshot")
	}
	return storage.AuditEvent{OrgID: snapshot.orgID, RepoID: packet.Repository.RepoID, ActorType: "system", ActorID: "retention_worker", Action: "context_packet_snapshot_purged", ResourceType: "context_packet_snapshot", ResourceID: id, Status: "success", Metadata: map[string]any{"expires_at": snapshot.expiresAt.UTC().Format(time.RFC3339Nano), "cutoff": cutoff.UTC().Format(time.RFC3339Nano)}, CreatedAt: createdAt.UTC()}, nil
}
