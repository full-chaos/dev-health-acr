package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
)

// An aggregate whose latest event is terminal takes no second terminal row from
// any path: the audit row and the state change stay, the outbox row does not.

func expiringInput(suffix string) storage.CredentialCreateInput {
	input := outboxInput(suffix)
	expires := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
	input.ExpiresAt = &expires
	return input
}

// expireInTheStream writes the credential's expired event the way the tick does.
func expireInTheStream(t *testing.T, audit *AuditStore, credential contractsv1.ClientCredential) {
	t.Helper()
	require.NoError(t, credentiallifecycle.Append(context.Background(), &audit.ledger, credentiallifecycle.Facts{
		Kind: credentiallifecycle.KindExpired, Credential: credential, At: time.Date(2026, 11, 1, 6, 31, 0, 0, time.UTC),
		Actor: credentiallifecycle.Actor{Type: credentiallifecycle.ActorSystem},
	}))
}

func eventTypes(t *testing.T, audit *AuditStore, id string) []string {
	t.Helper()
	var out []string
	for _, row := range audit.LifecycleOutbox() {
		if row.AggregateID == id {
			out = append(out, row.EventType)
		}
	}
	return out
}

func TestOutbox_ARevokeOfAnExpiredCredentialWritesNoSecondTerminalRow(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	credential, err := lifecycle.CreateCredential(context.Background(), expiringInput("termrevoke"))
	require.NoError(t, err)
	expireInTheStream(t, audit, credential)
	auditBefore := len(audit.events)
	_, err = lifecycle.RevokeCredential(context.Background(), revoke(credential.CredentialID, "admin", "", false))
	require.NoError(t, err)
	require.Equal(t, []string{"acr_credential.issued", "acr_credential.expired"}, eventTypes(t, audit, credential.CredentialID))
	stored, err := lifecycle.GetByID(context.Background(), outboxOrg, credential.CredentialID)
	require.NoError(t, err)
	require.NotNil(t, stored.RevokedAt, "the credential is still revoked")
	require.Len(t, audit.events, auditBefore+1, "and the audit row is still written")
}

func TestOutbox_AFamilyRevokeSkipsTheTerminalMemberOnly(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	ids := familyMembers(t, lifecycle, "fam_term", 3)
	member, err := lifecycle.GetByID(context.Background(), outboxOrg, ids[1])
	require.NoError(t, err)
	audit.ledger.TruncateTo(0)
	expireInTheStream(t, audit, withExpiry(member))
	_, err = lifecycle.RevokeCredential(context.Background(), revoke(ids[0], "web-user-1", "", false))
	require.NoError(t, err)
	require.Equal(t, []string{"acr_credential.issued", "acr_credential.expired"}, eventTypes(t, audit, ids[1]), "the expired member takes no second terminal")
	require.Equal(t, []string{"acr_credential.issued", "acr_credential.revoked"}, eventTypes(t, audit, ids[0]))
	require.Equal(t, []string{"acr_credential.issued", "acr_credential.revoked"}, eventTypes(t, audit, ids[2]))
}

func withExpiry(c contractsv1.ClientCredential) contractsv1.ClientCredential {
	if c.ExpiresAt == nil {
		expires := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
		c.ExpiresAt = &expires
	}
	return c
}

func TestOutbox_ARotationOfAnExpiredParentStillWritesTheChildAndNoSecondTerminalForTheParent(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	parent, err := lifecycle.CreateCredential(context.Background(), expiringInput("termrotate"))
	require.NoError(t, err)
	expireInTheStream(t, audit, parent)
	child, err := lifecycle.RotateCredential(context.Background(), validRotation(parent, "termchild", "a", true))
	require.NoError(t, err)
	require.Equal(t, []string{"acr_credential.rotated"}, eventTypes(t, audit, child.CredentialID), "the child's rows are written")
	require.Equal(t, []string{"acr_credential.issued", "acr_credential.expired"}, eventTypes(t, audit, parent.CredentialID), "the parent's second terminal is not")
	stored, err := lifecycle.GetByID(context.Background(), outboxOrg, parent.CredentialID)
	require.NoError(t, err)
	require.NotNil(t, stored.RevokedAt)
}

func TestOutbox_ARollbackThatRevokesAnExpiredSuccessorWritesNoSecondTerminal(t *testing.T) {
	audit, lifecycle, now := newOutboxStore(t)
	source, err := lifecycle.CreateCredential(context.Background(), outboxInput("termrbsrc"))
	require.NoError(t, err)
	successor, err := lifecycle.RotateCredential(context.Background(), validRotation(source, "termrbsucc", "b", false))
	require.NoError(t, err)
	expireInTheStream(t, audit, withExpiry(successor))
	_, err = lifecycle.RollbackCredentialRotation(context.Background(), storage.CredentialRotationRollbackInput{
		OrgID: outboxOrg, SourceCredentialID: source.CredentialID, SuccessorCredentialID: successor.CredentialID,
		ActorID: successor.CredentialID, RollbackUntil: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"acr_credential.rotated", "acr_credential.expired"}, eventTypes(t, audit, successor.CredentialID))
}

func TestOutbox_ANonTerminalCredentialStillGetsItsRevokedRow(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	credential, err := lifecycle.CreateCredential(context.Background(), expiringInput("termcontrol"))
	require.NoError(t, err)
	_, err = lifecycle.RevokeCredential(context.Background(), revoke(credential.CredentialID, "admin", "", false))
	require.NoError(t, err)
	require.Equal(t, []string{"acr_credential.issued", "acr_credential.revoked"}, eventTypes(t, audit, credential.CredentialID))
}
