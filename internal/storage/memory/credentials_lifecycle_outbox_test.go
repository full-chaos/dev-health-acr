package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

const outboxOrg = "9f1c2b3a-0000-4000-8000-000000000001"

type outboxEvent struct {
	EventType string `json:"event_type"`
	Payload   struct {
		Revision  int    `json:"revision"`
		ActorType string `json:"actor_type"`
		ActorRef  string `json:"actor_ref"`
		Change    struct {
			Status string `json:"status"`
			Class  string `json:"acr_class"`
			Parent string `json:"rotation_parent_id"`
			Owner  string `json:"owner_subject"`
		} `json:"change"`
	} `json:"payload"`
}

func outboxSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "auth", "v1", "ops-vendored", "jsonschema", "acr-credential-lifecycle-event.v1.schema.json"))
	require.NoError(t, err)
	var schema jsonschema.Schema
	require.NoError(t, json.Unmarshal(raw, &schema))
	resolved, err := schema.Resolve(nil)
	require.NoError(t, err)
	return resolved
}

// outboxRows returns the decoded events of one aggregate in revision order and
// proves every row in the store is valid against the vendored contract.
func outboxRows(t *testing.T, audit *AuditStore, aggregate string) []outboxEvent {
	t.Helper()
	schema := outboxSchema(t)
	var out []outboxEvent
	for _, row := range audit.LifecycleOutbox() {
		var document any
		require.NoError(t, json.Unmarshal(row.Payload, &document))
		require.NoError(t, schema.Validate(document), string(row.Payload))
		if row.AggregateID != aggregate {
			continue
		}
		var event outboxEvent
		require.NoError(t, json.Unmarshal(row.Payload, &event))
		require.Equal(t, row.Revision, event.Payload.Revision)
		out = append(out, event)
	}
	return out
}

func outboxInput(suffix string) storage.CredentialCreateInput {
	input := validCredentialCreateInput(suffix)
	input.OrgID = outboxOrg
	sum := sha256.Sum256([]byte(suffix))
	input.TokenHash = hex.EncodeToString(sum[:])
	return input
}

func newOutboxStore(t *testing.T) (*AuditStore, *storage.CredentialLifecycle, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	audit := NewAuditStore()
	_, lifecycle, err := newCredentialStore(audit, func() time.Time { return now })
	require.NoError(t, err)
	return audit, lifecycle, now
}

func TestOutbox_CreateWritesOneIssuedRowWithRevisionOne(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	input := outboxInput("create1")
	credential, err := lifecycle.CreateCredential(context.Background(), input)
	require.NoError(t, err)
	rows := outboxRows(t, audit, credential.CredentialID)
	require.Len(t, rows, 1)
	require.Equal(t, "acr_credential.issued", rows[0].EventType)
	require.Equal(t, 1, rows[0].Payload.Revision)
	require.Equal(t, "user", rows[0].Payload.ActorType)
	require.Equal(t, "actor_1", rows[0].Payload.ActorRef)
	require.Equal(t, "org_bearer", rows[0].Payload.Change.Class)
	require.Empty(t, rows[0].Payload.Change.Owner)
	require.Len(t, audit.LifecycleOutbox(), 1)
}

func TestOutbox_WorkloadExchangeIsIssuedBySystemWithNoRef(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	input := outboxInput("workload1")
	input.IssuanceProvenance = storage.CredentialIssuanceProvenanceWorkloadExchange
	input.WorkloadBindingID = "binding-1"
	expires := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	input.ExpiresAt = &expires
	credential, err := lifecycle.CreateCredential(context.Background(), input)
	require.NoError(t, err)
	rows := outboxRows(t, audit, credential.CredentialID)
	require.Len(t, rows, 1)
	require.Equal(t, "system", rows[0].Payload.ActorType)
	require.Empty(t, rows[0].Payload.ActorRef)
	require.Equal(t, "workload", rows[0].Payload.Change.Class)
}

func TestOutbox_DeviceRedeemAndRefreshSuccessorActors(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	input := outboxInput("device1")
	input.IssuanceProvenance = storage.CredentialIssuanceProvenanceDeviceAuthorization
	input.ApprovingUserID = "web:user@example"
	input.ActorID = "web:user@example"
	credential, err := lifecycle.CreateCredential(context.Background(), input)
	require.NoError(t, err)
	rows := outboxRows(t, audit, credential.CredentialID)
	require.Len(t, rows, 1)
	require.Equal(t, "acr_credential.device_redeemed", rows[0].EventType)
	require.Equal(t, "native_device", rows[0].Payload.Change.Class)
	require.Equal(t, "user", rows[0].Payload.ActorType)
	require.True(t, strings.HasPrefix(rows[0].Payload.ActorRef, "sha256_"), "a subject with ':' and '@' is encoded")
	require.Equal(t, rows[0].Payload.ActorRef, rows[0].Payload.Change.Owner)

	refresh := browserOutboxInput("refresh1", "fam_r")
	refresh.ActorID = "oauth_refresh_grant"
	refreshed, err := lifecycle.CreateCredential(context.Background(), refresh)
	require.NoError(t, err)
	rows = outboxRows(t, audit, refreshed.CredentialID)
	require.Len(t, rows, 1)
	require.Equal(t, "acr_credential.issued", rows[0].EventType, "the browser class is issued, never device_redeemed")
	require.Equal(t, "browser", rows[0].Payload.Change.Class)
	require.Equal(t, "system", rows[0].Payload.ActorType)
	require.Empty(t, rows[0].Payload.ActorRef)
}

func browserOutboxInput(suffix, family string) storage.CredentialCreateInput {
	input := outboxInput(suffix)
	expires := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)
	input.IssuanceProvenance = storage.CredentialIssuanceProvenanceDeviceAuthorization
	input.ApprovingUserID = "web-user-1"
	input.ActorID = "web-user-1"
	input.Resource = "https://acr.example/mcp"
	input.ClientID = "client-1"
	input.ClientClass = storage.CredentialClientClassBrowser
	input.SessionID, input.FamilyID, input.FamilyExpiresAt, input.ExpiresAt = "sess-1", family, &end, &expires
	return input
}

func TestOutbox_RotateOverlapWritesRotatedAndLeavesTheParentLive(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	parent, err := lifecycle.CreateCredential(context.Background(), outboxInput("parent1"))
	require.NoError(t, err)
	child, err := lifecycle.RotateCredential(context.Background(), validRotation(parent, "child1", "b", false))
	require.NoError(t, err)
	rows := outboxRows(t, audit, child.CredentialID)
	require.Len(t, rows, 1)
	require.Equal(t, "acr_credential.rotated", rows[0].EventType)
	require.Equal(t, 1, rows[0].Payload.Revision)
	require.Equal(t, parent.CredentialID, rows[0].Payload.Change.Parent)
	require.Len(t, outboxRows(t, audit, parent.CredentialID), 1, "overlap above 0: the parent gets no revoked row")
}

func validRotation(source contractsv1.ClientCredential, suffix, hashByte string, immediate bool) storage.CredentialRotationInput {
	input := validCredentialRotationInput(source, suffix, strings.Repeat(hashByte, 64), immediate)
	return input
}

func TestOutbox_RotateOverlapZeroWritesRotatedAndTheParentsRevokedInOneStep(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	parent, err := lifecycle.CreateCredential(context.Background(), outboxInput("parent2"))
	require.NoError(t, err)
	child, err := lifecycle.RotateCredential(context.Background(), validRotation(parent, "child2", "c", true))
	require.NoError(t, err)
	require.Len(t, outboxRows(t, audit, child.CredentialID), 1)
	parentRows := outboxRows(t, audit, parent.CredentialID)
	require.Len(t, parentRows, 2)
	require.Equal(t, "acr_credential.revoked", parentRows[1].EventType)
	require.Equal(t, 2, parentRows[1].Payload.Revision)
	require.Equal(t, "revoked", parentRows[1].Payload.Change.Status)
}

func TestOutbox_RotateOfALegacyParentWritesTheParentsRevisionOneFirst(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	parent, err := lifecycle.CreateCredential(context.Background(), outboxInput("legacy1"))
	require.NoError(t, err)
	audit.ledger.TruncateTo(0)
	child, err := lifecycle.RotateCredential(context.Background(), validRotation(parent, "child3", "d", true))
	require.NoError(t, err)
	rows := audit.LifecycleOutbox()
	require.Len(t, rows, 3)
	require.Equal(t, parent.CredentialID, rows[0].AggregateID)
	require.Equal(t, 1, rows[0].Revision)
	require.Equal(t, child.CredentialID, rows[1].AggregateID)
	require.Equal(t, parent.CredentialID, rows[2].AggregateID)
	require.Equal(t, 2, rows[2].Revision)
	parentRows := outboxRows(t, audit, parent.CredentialID)
	require.Equal(t, "acr_credential.issued", parentRows[0].EventType)
	require.Equal(t, "system", parentRows[0].Payload.ActorType)
}

func TestOutbox_RevokeActorsAndLegacyFirstTouch(t *testing.T) {
	for name, tc := range map[string]struct {
		input     func(id string) storage.CredentialRevocationInput
		actorType string
		actorRef  string
	}{
		"user":     {func(id string) storage.CredentialRevocationInput { return revoke(id, "admin 1", "", false) }, "user", "sha256_"},
		"operator": {func(id string) storage.CredentialRevocationInput { return revoke(id, "op-1", "operator", false) }, "operator", "op-1"},
		"system": {func(id string) storage.CredentialRevocationInput {
			return revoke(id, "device-credential-ack", "system", false)
		}, "system", ""},
		"self": {func(id string) storage.CredentialRevocationInput { return revoke(id, id, "", true) }, "credential", "SELF"},
	} {
		t.Run(name, func(t *testing.T) {
			audit, lifecycle, _ := newOutboxStore(t)
			credential, err := lifecycle.CreateCredential(context.Background(), outboxInput("revoke_"+name))
			require.NoError(t, err)
			audit.ledger.TruncateTo(0)
			_, err = lifecycle.RevokeCredential(context.Background(), tc.input(credential.CredentialID))
			require.NoError(t, err)
			rows := outboxRows(t, audit, credential.CredentialID)
			require.Len(t, rows, 2, "a legacy credential gets rev 1 and rev 2 in the same step")
			require.Equal(t, 1, rows[0].Payload.Revision)
			require.Equal(t, 2, rows[1].Payload.Revision)
			require.Equal(t, tc.actorType, rows[1].Payload.ActorType)
			want := tc.actorRef
			if want == "SELF" {
				want = credential.CredentialID
			}
			if strings.HasSuffix(want, "_") {
				require.True(t, strings.HasPrefix(rows[1].Payload.ActorRef, want))
			} else {
				require.Equal(t, want, rows[1].Payload.ActorRef)
			}
			_, err = lifecycle.RevokeCredential(context.Background(), tc.input(credential.CredentialID))
			require.ErrorIs(t, err, storage.ErrConflict)
			require.Len(t, audit.LifecycleOutbox(), 2, "a refused second revoke writes nothing")
		})
	}
}

func revoke(id, actor, actorType string, self bool) storage.CredentialRevocationInput {
	return storage.CredentialRevocationInput{OrgID: outboxOrg, CredentialID: id, ActorID: actor, ActorType: actorType, SelfCredential: self}
}

func TestOutbox_SelfFlagWithAnotherActorIsRefused(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	credential, err := lifecycle.CreateCredential(context.Background(), outboxInput("selfbad1"))
	require.NoError(t, err)
	_, err = lifecycle.RevokeCredential(context.Background(), revoke(credential.CredentialID, "someone-else", "", true))
	require.Error(t, err)
	require.Len(t, audit.LifecycleOutbox(), 1)
	stored, err := lifecycle.GetByID(context.Background(), outboxOrg, credential.CredentialID)
	require.NoError(t, err)
	require.Nil(t, stored.RevokedAt)
}

func TestOutbox_SelfRevokeLeavesTheAuditRowAsAnOrdinaryRevoke(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	credential, err := lifecycle.CreateCredential(context.Background(), outboxInput("selfaudit1"))
	require.NoError(t, err)
	_, err = lifecycle.RevokeCredential(context.Background(), revoke(credential.CredentialID, credential.CredentialID, "", true))
	require.NoError(t, err)
	events := audit.events
	require.Equal(t, storage.AuditActionCredentialRevoked, events[len(events)-1].Action)
	require.Equal(t, "user", events[len(events)-1].ActorType, "the audit actor type is unchanged by the outbox-only self marker")
	require.Equal(t, credential.CredentialID, events[len(events)-1].ActorID)
}

func TestOutbox_BrowserFamilyRevokeWritesOneRevokedRowPerMember(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	var ids []string
	for _, suffix := range []string{"fam_a", "fam_b", "fam_c"} {
		input := browserOutboxInput(suffix, "fam_x")
		credential, err := lifecycle.CreateCredential(context.Background(), input)
		require.NoError(t, err)
		ids = append(ids, credential.CredentialID)
	}
	other := browserOutboxInput("fam_other", "fam_y")
	otherCredential, err := lifecycle.CreateCredential(context.Background(), other)
	require.NoError(t, err)
	audit.ledger.TruncateTo(0)
	_, err = lifecycle.RevokeCredential(context.Background(), revoke(ids[0], "web-user-1", "", false))
	require.NoError(t, err)
	revoked := map[string]int{}
	for _, row := range audit.LifecycleOutbox() {
		if row.Revision == 2 {
			revoked[row.AggregateID]++
		}
	}
	require.Equal(t, map[string]int{ids[0]: 1, ids[1]: 1, ids[2]: 1}, revoked)
	require.Empty(t, outboxRows(t, audit, otherCredential.CredentialID), "another family is untouched")
	require.Len(t, audit.LifecycleOutbox(), 6, "three members: each a first touch and a revoked row")
}

func TestOutbox_RollbackBySuccessorNamesTheCredentialAsActor(t *testing.T) {
	audit, lifecycle, now := newOutboxStore(t)
	source, err := lifecycle.CreateCredential(context.Background(), outboxInput("rb_source"))
	require.NoError(t, err)
	successor, err := lifecycle.RotateCredential(context.Background(), validRotation(source, "rb_succ", "f", false))
	require.NoError(t, err)
	_, err = lifecycle.RollbackCredentialRotation(context.Background(), storage.CredentialRotationRollbackInput{
		OrgID: outboxOrg, SourceCredentialID: source.CredentialID, SuccessorCredentialID: successor.CredentialID,
		ActorID: successor.CredentialID, RollbackUntil: now.Add(time.Minute),
	})
	require.NoError(t, err)
	rows := outboxRows(t, audit, successor.CredentialID)
	require.Len(t, rows, 2)
	require.Equal(t, "credential", rows[1].Payload.ActorType)
	require.Equal(t, successor.CredentialID, rows[1].Payload.ActorRef)
}

func TestOutbox_AMappingFailureRollsTheMutationBack(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	browser, err := lifecycle.CreateCredential(context.Background(), browserOutboxInput("rbk_browser", "fam_z"))
	require.NoError(t, err)
	audit.ledger.TruncateTo(0)
	auditBefore := len(audit.events)
	_, err = lifecycle.RotateCredential(context.Background(), validRotation(browser, "rbk_child", "e", false))
	require.ErrorContains(t, err, "rotated browser credential", "refused by the contract mapper, not by an earlier check")
	stored, getErr := lifecycle.GetByID(context.Background(), outboxOrg, browser.CredentialID)
	require.NoError(t, getErr)
	require.Nil(t, stored.RevokedAt)
	require.Equal(t, browser.ExpiresAt, stored.ExpiresAt, "the overlap cut was not applied")
	require.Len(t, audit.events, auditBefore+0, "no rotation audit row")
	require.Empty(t, audit.LifecycleOutbox(), "no partial rows")
	_, err = lifecycle.GetByID(context.Background(), outboxOrg, "cred_rbk_child")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestOutbox_AFreeFormOrganizationIdThatFitsGetsARowAndOneThatDoesNotFailsTheMutation(t *testing.T) {
	audit, lifecycle, _ := newOutboxStore(t)
	input := validCredentialCreateInput("freeform1")
	credential, err := lifecycle.CreateCredential(context.Background(), input)
	require.NoError(t, err)
	rows := audit.LifecycleOutbox()
	require.Len(t, rows, 1)
	require.Equal(t, credential.CredentialID, rows[0].AggregateID)
	require.Contains(t, string(rows[0].Payload), `"organization_id":"org_org_1"`)

	unfit := validCredentialCreateInput("freeform2")
	unfit.OrgID = "org:1"
	unfit.TokenHash = strings.Repeat("c", 64)
	_, err = lifecycle.CreateCredential(context.Background(), unfit)
	require.ErrorContains(t, err, "organization id", "the mapper refuses an organization id that does not fit the wire form")
	_, err = lifecycle.GetByID(context.Background(), "org:1", unfit.CredentialID)
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.Len(t, audit.LifecycleOutbox(), 1, "a failed mutation leaves no row")
}

// flakyContext reports no error for its first failAfter Err calls and a
// cancellation from then on, so a failure lands between two steps of one
// mutation.
type flakyContext struct {
	context.Context
	calls     *int
	failAfter int
}

func (c flakyContext) Err() error {
	*c.calls++
	if *c.calls > c.failAfter {
		return context.Canceled
	}
	return nil
}

func TestOutbox_AnAuditFailureAfterTheOutboxRowsLeavesNoRows(t *testing.T) {
	for failAfter := 0; failAfter < 6; failAfter++ {
		audit, lifecycle, _ := newOutboxStore(t)
		credential, err := lifecycle.CreateCredential(context.Background(), outboxInput("flaky"))
		require.NoError(t, err)
		audit.ledger.TruncateTo(0)
		calls := 0
		_, err = lifecycle.RevokeCredential(flakyContext{Context: context.Background(), calls: &calls, failAfter: failAfter}, revoke(credential.CredentialID, "admin", "", false))
		stored, getErr := lifecycle.GetByID(context.Background(), outboxOrg, credential.CredentialID)
		require.NoError(t, getErr)
		if err != nil {
			require.Nil(t, stored.RevokedAt, "failAfter=%d: a failed revoke changes nothing", failAfter)
			require.Empty(t, audit.LifecycleOutbox(), "failAfter=%d: a failed revoke leaves no outbox row", failAfter)
		} else {
			require.NotNil(t, stored.RevokedAt)
			require.Len(t, audit.LifecycleOutbox(), 2)
		}
	}
}
