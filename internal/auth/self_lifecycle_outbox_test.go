package auth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

type selfOutboxEvent struct {
	EventType string `json:"event_type"`
	Payload   struct {
		Revision  int    `json:"revision"`
		ActorType string `json:"actor_type"`
		ActorRef  string `json:"actor_ref"`
		Change    struct {
			Parent string `json:"rotation_parent_id"`
		} `json:"change"`
	} `json:"payload"`
}

func selfOutbox(t *testing.T, audit *memory.AuditStore, aggregate string) []selfOutboxEvent {
	t.Helper()
	var out []selfOutboxEvent
	for _, row := range audit.LifecycleOutbox() {
		if row.AggregateID != aggregate {
			continue
		}
		var event selfOutboxEvent
		require.NoError(t, json.Unmarshal(row.Payload, &event))
		out = append(out, event)
	}
	return out
}

func TestService_RotateSelfNamesTheCredentialAsTheOutboxActor(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	source := createSelfCredential(t, service, []string{"owner/repo"})
	rotation, err := service.RotateSelf(context.Background(), selfPrincipal(source.Credential))
	require.NoError(t, err)
	rows := selfOutbox(t, audit, rotation.Issued.Credential.CredentialID)
	require.Len(t, rows, 1)
	require.Equal(t, "acr_credential.rotated", rows[0].EventType)
	require.Equal(t, "credential", rows[0].Payload.ActorType)
	require.Equal(t, source.Credential.CredentialID, rows[0].Payload.ActorRef)
	require.Equal(t, source.Credential.CredentialID, rows[0].Payload.Change.Parent)
	require.Len(t, selfOutbox(t, audit, source.Credential.CredentialID), 1, "the 15 minute overlap leaves the parent without a revoked row")
}

func TestService_RevokeSelfNamesTheCredentialAsTheOutboxActorAndKeepsTheAuditRow(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	source := createSelfCredential(t, service, []string{"owner/repo"})
	_, err := service.RevokeSelf(context.Background(), selfPrincipal(source.Credential))
	require.NoError(t, err)
	rows := selfOutbox(t, audit, source.Credential.CredentialID)
	require.Len(t, rows, 2)
	require.Equal(t, "acr_credential.revoked", rows[1].EventType)
	require.Equal(t, "credential", rows[1].Payload.ActorType)
	require.Equal(t, source.Credential.CredentialID, rows[1].Payload.ActorRef)
}

func TestService_AdminRevokeAndOperatorRevokeAreNotSelf(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	first := createSelfCredential(t, service, []string{"owner/repo"})
	_, err := service.Revoke(context.Background(), first.Credential.OrgID, first.Credential.CredentialID, "admin-1")
	require.NoError(t, err)
	require.Equal(t, "user", selfOutbox(t, audit, first.Credential.CredentialID)[1].Payload.ActorType)
	second := createSelfCredential(t, service, []string{"owner/repo"})
	_, err = service.RevokeAsOperator(context.Background(), second.Credential.OrgID, second.Credential.CredentialID, "op-1", "")
	require.NoError(t, err)
	got := selfOutbox(t, audit, second.Credential.CredentialID)[1]
	require.Equal(t, "operator", got.Payload.ActorType)
	require.Equal(t, "op-1", got.Payload.ActorRef)
}
