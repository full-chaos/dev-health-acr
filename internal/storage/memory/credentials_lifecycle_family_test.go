package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func familyMembers(t *testing.T, lifecycle *storage.CredentialLifecycle, family string, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		suffix := family + string(rune('a'+i))
		input := browserOutboxInput(suffix, family)
		sum := sha256.Sum256([]byte(suffix))
		input.TokenHash = hex.EncodeToString(sum[:])
		credential, err := lifecycle.CreateCredential(context.Background(), input)
		require.NoError(t, err)
		ids = append(ids, credential.CredentialID)
	}
	return ids
}

// A family revoke names the same actor on every member's row: the actor mapped
// once from the revocation input.
func TestOutbox_FamilyRevokeKeepsTheMappedActorOnEveryMember(t *testing.T) {
	for name, tc := range map[string]struct {
		revoke   func(target string) storage.CredentialRevocationInput
		wantType string
		wantRef  func(id, target string) string
	}{
		"user":     {func(id string) storage.CredentialRevocationInput { return revoke(id, "web-user-1", "", false) }, "user", func(_, _ string) string { return "web-user-1" }},
		"operator": {func(id string) storage.CredentialRevocationInput { return revoke(id, "operator_1", "operator", false) }, "operator", func(_, _ string) string { return "operator_1" }},
		// A credential actor names the credential the row is about: the
		// contract fixes that reference to the row's own aggregate.
		"self": {func(id string) storage.CredentialRevocationInput { return revoke(id, id, "", true) }, "credential", func(id, _ string) string { return id }},
	} {
		t.Run(name, func(t *testing.T) {
			audit, lifecycle, _ := newOutboxStore(t)
			ids := familyMembers(t, lifecycle, "fam_"+name, 3)
			audit.ledger.TruncateTo(0)
			_, err := lifecycle.RevokeCredential(context.Background(), tc.revoke(ids[0]))
			require.NoError(t, err)
			for _, id := range ids {
				rows := outboxRows(t, audit, id)
				require.Len(t, rows, 2, id)
				require.Equal(t, "acr_credential.revoked", rows[1].EventType, id)
				require.Equal(t, tc.wantType, rows[1].Payload.ActorType, "%s: every member keeps the mapped actor type", id)
				require.Equal(t, tc.wantRef(id, ids[0]), rows[1].Payload.ActorRef, id)
			}
		})
	}
}

// A family revoke is one operation: a failure at any step leaves every member
// live, no outbox row and no audit row.
func TestOutbox_FamilyRevokeIsAllOrNothing(t *testing.T) {
	failed, succeeded := 0, 0
	for failAfter := 0; failAfter < 40; failAfter++ {
		audit, lifecycle, _ := newOutboxStore(t)
		ids := familyMembers(t, lifecycle, "fam_atomic", 3)
		audit.ledger.TruncateTo(0)
		auditBefore := len(audit.events)
		calls := 0
		_, err := lifecycle.RevokeCredential(flakyContext{Context: context.Background(), calls: &calls, failAfter: failAfter}, revoke(ids[0], "web-user-1", "", false))
		revoked := 0
		for _, id := range ids {
			stored, getErr := lifecycle.GetByID(context.Background(), outboxOrg, id)
			require.NoError(t, getErr)
			if stored.RevokedAt != nil {
				revoked++
			}
		}
		if err != nil {
			failed++
			require.Zero(t, revoked, "failAfter=%d: a failed family revoke changes no member", failAfter)
			require.Empty(t, audit.LifecycleOutbox(), "failAfter=%d: no outbox row", failAfter)
			require.Len(t, audit.events, auditBefore, "failAfter=%d: no audit row", failAfter)
			continue
		}
		succeeded++
		require.Equal(t, len(ids), revoked, "failAfter=%d", failAfter)
		require.Len(t, audit.LifecycleOutbox(), 6, "failAfter=%d: first touch and revoked for every member", failAfter)
	}
	require.Positive(t, failed, "the sweep must reach a failing step")
	require.Positive(t, succeeded, "and a succeeding run")
}
