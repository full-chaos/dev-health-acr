package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

const (
	orgRevokeOtherOrg = "22222222-2222-4222-8222-222222222222"
	orgRevokeBinding  = "wlb_go_api_revoke"
	orgRevokeActor    = "usr_platform_admin_1"
)

// orgRevokeRig seeds one credential of every class into deviceFlowTestOrgID on
// the memory store.
type orgRevokeRig struct {
	onBehalfRig
	now time.Time
}

// newOrgRevokeRig's store writes at 18:00 and its service reads at 18:02, so a
// credential seeded to expire at 18:01 is expired when the service lists it.
func newOrgRevokeRig(t *testing.T) orgRevokeRig {
	t.Helper()
	seeded := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	now := seeded.Add(2 * time.Minute)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, seeded, audit)
	return orgRevokeRig{onBehalfRig: onBehalfRig{service: newTestService(t, store, audit, now), store: store, audit: audit}, now: now}
}

func (r orgRevokeRig) create(t *testing.T, input storage.CredentialCreateInput) string {
	t.Helper()
	if input.OrgID == "" {
		input.OrgID = deviceFlowTestOrgID
	}
	input.Name, input.TokenPrefix, input.TokenHash = "seed", "fcacr_"+input.CredentialID[5:13], HashToken("fcacr_"+input.CredentialID+"_secret_value_for_hash")
	if input.RepositoryScopes == nil {
		input.RepositoryScopes = []string{"*"}
	}
	if input.Scopes == nil {
		input.Scopes = []string{ScopeContextRead}
	}
	if input.ActorID == "" {
		input.ActorID = "seed_actor"
	}
	created, err := r.store.CreateCredential(context.Background(), input)
	require.NoError(t, err)
	return created.CredentialID
}

func (r orgRevokeRig) workload(t *testing.T, id, binding string) string {
	t.Helper()
	expires := r.now.Add(10 * time.Minute)
	return r.create(t, storage.CredentialCreateInput{CredentialID: id, ExpiresAt: &expires,
		Scopes:             RoleScopes("revoke"),
		IssuanceProvenance: storage.CredentialIssuanceProvenanceWorkloadExchange, WorkloadBindingID: binding})
}

// seedEveryClass stores, in the organization: a browser family of two, an
// oauth_user, a native_device, an org_bearer, a workload of another binding,
// an already expired org_bearer; and an org_bearer of another organization.
func (r orgRevokeRig) seedEveryClass(t *testing.T) (live []string, expired, otherOrg string) {
	t.Helper()
	live = append(live, r.browserCredential(t, "cred_browserfam001"))
	expires := time.Date(2026, 11, 4, 19, 0, 0, 0, time.UTC)
	family := expires.Add(11 * time.Hour)
	live = append(live, r.create(t, storage.CredentialCreateInput{CredentialID: "cred_browserfam002", ExpiresAt: &expires,
		ActorID: onBehalfApprover, RepositoryScopes: []string{"owner/repo"},
		IssuanceProvenance: storage.CredentialIssuanceProvenanceDeviceAuthorization, ApprovingUserID: onBehalfApprover,
		Resource: "https://acr.example.test", ClientID: "acrc_browser0000000000000000000000000",
		ClientClass: storage.CredentialClientClassBrowser, SessionID: "ses_web_1", FamilyID: "fam_cred_browserfam001", FamilyExpiresAt: &family}))
	live = append(live, r.mcpCredential(t, "cred_oauthuser0001", "https://acr.example.test/mcp"))
	live = append(live, r.mcpCredential(t, "cred_nativedev0001", ""))
	live = append(live, r.create(t, storage.CredentialCreateInput{CredentialID: "cred_orgbearer0001"}))
	live = append(live, r.workload(t, "cred_otherwork0001", "wlb_projector_read"))
	soon := r.now.Add(-time.Minute)
	expired = r.create(t, storage.CredentialCreateInput{CredentialID: "cred_expiredbr0001", ExpiresAt: &soon})
	otherOrg = r.create(t, storage.CredentialCreateInput{CredentialID: "cred_otherorg00001", OrgID: orgRevokeOtherOrg})
	return live, expired, otherOrg
}

func (r orgRevokeRig) revokedAt(t *testing.T, org, id string) *time.Time {
	t.Helper()
	credential, err := r.store.GetByID(context.Background(), org, id)
	require.NoError(t, err)
	return credential.RevokedAt
}

func (r orgRevokeRig) liveCount(t *testing.T, org string) int {
	t.Helper()
	listed, err := r.store.List(context.Background(), org)
	require.NoError(t, err)
	n := 0
	for _, credential := range listed {
		if credential.RevokedAt == nil && (credential.ExpiresAt == nil || credential.ExpiresAt.After(r.now)) {
			n++
		}
	}
	return n
}

// One call revokes every live credential of every class in the organization,
// writes one credential_revoked audit row (actor admin, the reason; the
// browser family's reason org_deprovisioned) and one revoked outbox event
// each, revokes the caller last, and touches neither the expired credential
// nor another organization's.
func TestRevokeOrganization_RevokesEveryLiveClassWithAuditAndOutbox(t *testing.T) {
	r := newOrgRevokeRig(t)
	live, expired, otherOrg := r.seedEveryClass(t)
	caller := r.workload(t, "cred_callerwork001", orgRevokeBinding)

	result, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, caller, orgRevokeActor, OrganizationRevokeReasonDisabled, OrganizationRevokeBatch)

	require.NoError(t, err)
	require.Equal(t, OrganizationRevocation{Revoked: len(live) - 1, CallerRevoked: 1, More: false}, result,
		"the second browser member is revoked by its family, not counted again")
	require.Zero(t, r.liveCount(t, deviceFlowTestOrgID))
	require.Nil(t, r.revokedAt(t, deviceFlowTestOrgID, expired), "an expired credential is left to the lifecycle tick")
	require.Nil(t, r.revokedAt(t, orgRevokeOtherOrg, otherOrg))

	revokedRows := map[string]map[string]any{}
	for _, event := range r.audit.Events() {
		if event.Action != "credential_revoked" {
			continue
		}
		require.Equal(t, deviceFlowTestOrgID, event.OrgID, "no other organization is touched")
		require.Equal(t, orgRevokeActor, event.ActorID)
		revokedRows[event.ResourceID] = event.Metadata
	}
	require.Len(t, revokedRows, len(live)+1)
	for _, id := range append(append([]string(nil), live...), caller) {
		metadata, ok := revokedRows[id]
		require.True(t, ok, "no credential_revoked row for %s", id)
		if id == "cred_browserfam001" || id == "cred_browserfam002" {
			require.Equal(t, "org_deprovisioned", metadata["family_revoke_reason"], id)
			continue
		}
		require.Equal(t, OrganizationRevokeReasonDisabled, metadata["reason"], id)
	}
	outbox := map[string]int{}
	for _, row := range r.audit.LifecycleOutbox() {
		if row.EventType == "acr_credential.revoked" {
			outbox[row.AggregateID]++
		}
	}
	require.Len(t, outbox, len(live)+1)
	for id, n := range outbox {
		require.Equal(t, 1, n, "outbox rows for %s", id)
	}
}

// With a limit of one and a fresh caller credential on every call (go-api
// exchanges per call), the loop still ends: each call revokes one credential
// the caller's binding did not mint, and the call that finds none left
// revokes every credential the binding minted and answers More false.
func TestRevokeOrganization_TheBatchLoopEndsWithLimitOne(t *testing.T) {
	r := newOrgRevokeRig(t)
	live, _, _ := r.seedEveryClass(t)
	calls, revoked := 0, 0
	for {
		calls++
		require.LessOrEqual(t, calls, len(live)+1, "the loop did not end")
		caller := r.workload(t, "cred_caller"+string(rune('a'+calls))+"zzzzzz", orgRevokeBinding)
		result, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, caller, orgRevokeActor, OrganizationRevokeReasonDeleted, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, result.Revoked, 1)
		revoked += result.Revoked
		if !result.More {
			require.Equal(t, calls, result.CallerRevoked, "the last call revokes every caller credential of the binding")
			break
		}
		require.Zero(t, result.CallerRevoked, "a call that answers More leaves the caller live")
	}
	require.Equal(t, len(live)-1, revoked, "the second browser member goes with its family")
	require.Zero(t, r.liveCount(t, deviceFlowTestOrgID))
}

// A repeat after everything is revoked revokes only the new caller.
func TestRevokeOrganization_ARepeatRevokesOnlyTheCaller(t *testing.T) {
	r := newOrgRevokeRig(t)
	r.seedEveryClass(t)
	first := r.workload(t, "cred_callerfirst01", orgRevokeBinding)
	_, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, first, orgRevokeActor, OrganizationRevokeReasonDisabled, OrganizationRevokeBatch)
	require.NoError(t, err)
	second := r.workload(t, "cred_callersecond1", orgRevokeBinding)

	result, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, second, orgRevokeActor, OrganizationRevokeReasonDisabled, OrganizationRevokeBatch)

	require.NoError(t, err)
	require.Equal(t, OrganizationRevocation{Revoked: 0, CallerRevoked: 1, More: false}, result)
	require.NotNil(t, r.revokedAt(t, deviceFlowTestOrgID, second))
	require.Zero(t, r.liveCount(t, deviceFlowTestOrgID), "nothing of the organization is live after the repeat")
}

// A credential minted while the loop runs (here an org bearer by the operator
// command, which has no organization check) is caught by a later call: the
// call that answers More false lists the organization again first.
func TestRevokeOrganization_ACredentialMintedDuringTheLoopIsCaught(t *testing.T) {
	r := newOrgRevokeRig(t)
	r.create(t, storage.CredentialCreateInput{CredentialID: "cred_orgbearer0001"})
	r.create(t, storage.CredentialCreateInput{CredentialID: "cred_orgbearer0002"})
	first, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, r.workload(t, "cred_callerloop001", orgRevokeBinding), orgRevokeActor, OrganizationRevokeReasonDisabled, 1)
	require.NoError(t, err)
	require.True(t, first.More)
	minted := r.create(t, storage.CredentialCreateInput{CredentialID: "cred_mintedmidloop"})

	calls := 1
	for {
		calls++
		require.LessOrEqual(t, calls, 5, "the loop did not end")
		caller := r.workload(t, "cred_callerloop00"+string(rune('0'+calls)), orgRevokeBinding)
		result, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, caller, orgRevokeActor, OrganizationRevokeReasonDisabled, 1)
		require.NoError(t, err)
		if !result.More {
			break
		}
	}
	require.NotNil(t, r.revokedAt(t, deviceFlowTestOrgID, minted), "the credential minted during the loop is revoked")
	require.Zero(t, r.liveCount(t, deviceFlowTestOrgID))
}

// A caller without a binding id (any workload credential) is still revoked last.
func TestRevokeOrganization_ACallerWithoutABindingIsRevokedLast(t *testing.T) {
	r := newOrgRevokeRig(t)
	r.create(t, storage.CredentialCreateInput{CredentialID: "cred_orgbearer0001"})
	caller := r.create(t, storage.CredentialCreateInput{CredentialID: "cred_callerbearer1"})

	result, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, "", caller, orgRevokeActor, OrganizationRevokeReasonDisabled, 1)

	require.NoError(t, err)
	require.Equal(t, OrganizationRevocation{Revoked: 1, CallerRevoked: 1, More: false}, result)
}

// Bad input is refused before any read or write.
func TestRevokeOrganization_RefusesBadInput(t *testing.T) {
	r := newOrgRevokeRig(t)
	id := r.create(t, storage.CredentialCreateInput{CredentialID: "cred_orgbearer0001"})
	for name, call := range map[string]func() error{
		"unknown reason": func() error {
			_, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, id, orgRevokeActor, "organization_suspended", 1)
			return err
		},
		"no actor": func() error {
			_, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, id, " ", OrganizationRevokeReasonDisabled, 1)
			return err
		},
		"zero limit": func() error {
			_, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, id, orgRevokeActor, OrganizationRevokeReasonDisabled, 0)
			return err
		},
		"no caller": func() error {
			_, err := r.service.RevokeOrganization(context.Background(), deviceFlowTestOrgID, orgRevokeBinding, "", orgRevokeActor, OrganizationRevokeReasonDisabled, 1)
			return err
		},
	} {
		require.True(t, errors.Is(call(), ErrInvalidCredential), name)
	}
	require.Nil(t, r.revokedAt(t, deviceFlowTestOrgID, id))
}
