package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestAuditStore_RecordRedactsInvalidUUIDCasts(t *testing.T) {
	for _, test := range []struct {
		name   string
		orgID  string
		repoID string
	}{
		{name: "organization", orgID: "raw-invalid-org-uuid", repoID: ""},
		{name: "repository", orgID: credentialTestOrgID, repoID: "raw-invalid-repo-uuid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			ctx := context.Background()
			db := newCredentialStoreDatabase(t, ctx)
			audit, err := NewAuditStore(db)
			require.NoError(t, err)
			event := storage.AuditEvent{
				OrgID: test.orgID, RepoID: test.repoID, ActorType: "user", ActorID: credentialTestActorID,
				Action: "context_read", ResourceType: "context_packet", ResourceID: "packet_1", Status: "success",
				CreatedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
			}

			// When
			err = audit.Record(ctx, event)

			// Then
			require.ErrorIs(t, err, storage.ErrUnavailable)
			require.NotContains(t, err.Error(), test.orgID)
			if test.repoID != "" {
				require.NotContains(t, err.Error(), test.repoID)
			}
			require.False(t, strings.Contains(err.Error(), "invalid input syntax"))
			var count int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events").Scan(&count))
			require.Zero(t, count)
		})
	}
}
