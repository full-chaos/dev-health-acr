package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type outboxRow struct {
	Aggregate string
	Revision  int
	EventType string
	ActorType string
	ActorRef  string
}

type outboxFixture struct {
	ctx     context.Context
	db      *sql.DB
	store   *storage.CredentialLifecycle
	service *auth.Service
}

func newOutboxFixture(t *testing.T) outboxFixture {
	t.Helper()
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	return outboxFixture{ctx: ctx, db: db, store: store, service: service}
}

func (f outboxFixture) rows(t *testing.T, aggregate string) []outboxRow {
	t.Helper()
	result, err := f.db.QueryContext(f.ctx, `
SELECT aggregate_id, revision, event_type, payload->'payload'->>'actor_type', coalesce(payload->'payload'->>'actor_ref', '')
FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1 ORDER BY revision`, aggregate)
	require.NoError(t, err)
	defer result.Close()
	var out []outboxRow
	for result.Next() {
		var row outboxRow
		require.NoError(t, result.Scan(&row.Aggregate, &row.Revision, &row.EventType, &row.ActorType, &row.ActorRef))
		out = append(out, row)
	}
	require.NoError(t, result.Err())
	return out
}

func (f outboxFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.QueryRowContext(f.ctx, query, args...).Scan(&n))
	return n
}

func TestCredentialOutbox_EveryCommittedMutationHasItsRowAndNoSecret(t *testing.T) {
	f := newOutboxFixture(t)
	source, err := f.service.Create(f.ctx, credentialCreateRequest("outbox-source"))
	require.NoError(t, err)
	id := source.Credential.CredentialID
	require.Equal(t, []outboxRow{{id, 1, "acr_credential.issued", "user", credentialTestActorID}}, f.rows(t, id))

	rotated, err := f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: id, CreatedBy: credentialTestActorID})
	require.NoError(t, err)
	child := rotated.Credential.CredentialID
	require.Equal(t, []outboxRow{{child, 1, "acr_credential.rotated", "user", credentialTestActorID}}, f.rows(t, child))
	require.Len(t, f.rows(t, id), 2, "overlap 0: the parent's revoked row is written with the rotated row")
	require.Equal(t, 2, f.rows(t, id)[1].Revision)
	require.Equal(t, "acr_credential.revoked", f.rows(t, id)[1].EventType)

	overlap, err := f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: child, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)
	require.Len(t, f.rows(t, child), 1, "overlap above 0: the parent stays live and gets no revoked row")
	require.Len(t, f.rows(t, overlap.Credential.CredentialID), 1)

	_, err = f.service.Revoke(f.ctx, credentialTestOrgID, overlap.Credential.CredentialID, credentialTestActorID)
	require.NoError(t, err)
	require.Equal(t, 2, f.rows(t, overlap.Credential.CredentialID)[1].Revision)
	require.Equal(t, 5, f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox`))

	rows, err := f.db.QueryContext(f.ctx, `SELECT payload::text FROM acr.credential_lifecycle_outbox`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var payload string
		require.NoError(t, rows.Scan(&payload))
		for _, secret := range []string{source.Token, rotated.Token, overlap.Token, source.Credential.TokenPrefix, rotated.Credential.TokenPrefix, "token_hash", "token_prefix", "fingerprint"} {
			require.NotContains(t, payload, secret)
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &event))
		require.Equal(t, "acr-credential-lifecycle-event.v1", event["schema_version"])
	}
	require.NoError(t, rows.Err())
}

func TestCredentialOutbox_AFailedOutboxWriteRollsBackEveryMutation(t *testing.T) {
	f := newOutboxFixture(t)
	source, err := f.service.Create(f.ctx, credentialCreateRequest("rollback-source"))
	require.NoError(t, err)
	baseCredentials := f.count(t, `SELECT count(*) FROM acr.client_credentials`)
	baseAudit := f.count(t, `SELECT count(*) FROM acr.audit_events`)
	baseOutbox := f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox`)
	_, err = f.db.ExecContext(f.ctx, `
CREATE FUNCTION acr.refuse_outbox_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'outbox refused'; END $$;
CREATE TRIGGER trg_refuse_outbox_insert BEFORE INSERT ON acr.credential_lifecycle_outbox FOR EACH ROW EXECUTE FUNCTION acr.refuse_outbox_insert();`)
	require.NoError(t, err)

	_, err = f.service.Create(f.ctx, credentialCreateRequest("rollback-new"))
	require.Error(t, err, "issue")
	_, err = f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: source.Credential.CredentialID, CreatedBy: credentialTestActorID})
	require.Error(t, err, "rotate")
	_, err = f.service.Revoke(f.ctx, credentialTestOrgID, source.Credential.CredentialID, credentialTestActorID)
	require.Error(t, err, "revoke")

	require.Equal(t, baseCredentials, f.count(t, `SELECT count(*) FROM acr.client_credentials`), "no credential was added")
	require.Equal(t, baseAudit, f.count(t, `SELECT count(*) FROM acr.audit_events`), "no audit row was written")
	require.Equal(t, baseOutbox, f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox`))
	require.Zero(t, f.count(t, `SELECT count(*) FROM acr.client_credentials WHERE credential_id = $1 AND (revoked_at IS NOT NULL OR rotated_at IS NOT NULL)`, source.Credential.CredentialID),
		"the source credential was neither revoked nor rotated")
}

func TestCredentialOutbox_ALegacyCredentialGetsRevisionOneThenTheEventInOneTransaction(t *testing.T) {
	f := newOutboxFixture(t)
	legacy, err := f.service.Create(f.ctx, credentialCreateRequest("legacy"))
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `DELETE FROM acr.credential_lifecycle_outbox`)
	require.NoError(t, err)

	_, err = f.service.RevokeSelf(f.ctx, storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: legacy.Credential.CredentialID,
		OrgID: credentialTestOrgID, CredentialID: legacy.Credential.CredentialID,
		RepositoryScopes: legacy.Credential.RepositoryScopes, Permissions: legacy.Credential.Scopes,
	})
	require.NoError(t, err)
	got := f.rows(t, legacy.Credential.CredentialID)
	require.Equal(t, []outboxRow{
		{legacy.Credential.CredentialID, 1, "acr_credential.issued", "system", ""},
		{legacy.Credential.CredentialID, 2, "acr_credential.revoked", "credential", legacy.Credential.CredentialID},
	}, got)

	_, err = f.service.Revoke(f.ctx, credentialTestOrgID, legacy.Credential.CredentialID, credentialTestActorID)
	require.ErrorIs(t, err, storage.ErrConflict)
	require.Len(t, f.rows(t, legacy.Credential.CredentialID), 2, "a refused second revoke writes nothing")
}

func TestCredentialOutbox_ARevisionIsNeverWrittenTwice(t *testing.T) {
	f := newOutboxFixture(t)
	issued, err := f.service.Create(f.ctx, credentialCreateRequest("unique"))
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `
INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
SELECT gen_random_uuid(), org_id, aggregate_id, revision, event_type, payload, created_at FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1`, issued.Credential.CredentialID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "uq_acr_credential_lifecycle_outbox_revision")
}

func TestCredentialOutbox_BrowserFamilyRevokeWritesOneRowPerMemberInOneTransaction(t *testing.T) {
	f := newOutboxFixture(t)
	family := "fam_outbox"
	end := time.Now().UTC().Add(24 * time.Hour)
	var ids []string
	for i, suffix := range []string{"fam-a", "fam-b", "fam-c"} {
		expires := time.Now().UTC().Add(time.Hour)
		credential, err := f.store.CreateCredential(f.ctx, storage.CredentialCreateInput{
			CredentialID: "cred_" + suffix, OrgID: credentialTestOrgID, Name: suffix, TokenPrefix: "fcacr_abcdefghij",
			TokenHash:        strings.Repeat(string(rune('a'+i)), 64),
			RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{"context:read"}, ActorID: "web-user-1",
			ExpiresAt: &expires, IssuanceProvenance: storage.CredentialIssuanceProvenanceDeviceAuthorization,
			ApprovingUserID: "web-user-1", Resource: "https://acr.example/mcp", ClientID: "client-1",
			ClientClass: storage.CredentialClientClassBrowser, SessionID: "sess-1", FamilyID: family, FamilyExpiresAt: &end,
		})
		require.NoError(t, err)
		ids = append(ids, credential.CredentialID)
	}
	_, err := f.service.Revoke(f.ctx, credentialTestOrgID, ids[0], "web-user-1")
	require.NoError(t, err)
	for _, id := range ids {
		rows := f.rows(t, id)
		require.Len(t, rows, 2, id)
		require.Equal(t, 2, rows[1].Revision)
		require.Equal(t, "acr_credential.revoked", rows[1].EventType)
	}
	require.Equal(t, 1, f.count(t, `SELECT count(DISTINCT created_at) FROM acr.credential_lifecycle_outbox WHERE revision = 2`), "one transaction time")
}
