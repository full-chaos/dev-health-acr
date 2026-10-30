package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestReproTokenRotationKeepsTheSubmissionOwner reproduces the gap the
// approving-user owner key closes. A user-approved credential submits an
// investigation, then rotates (as every browser token refresh will). The
// successor is the same user with the same grant: it must read its own
// submission status, and a same-key retry must not run the investigation
// again.
func TestReproTokenRotationKeepsTheSubmissionOwner(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	source := harness.issueDeviceCredential(t, "user_1")
	servedResultID(t, harness.post(0, source.Token, "submission-rotate-001", submissionQuestion))

	successor := harness.rotateSelf(t, source)

	if status := harness.status(1, successor, "submission-rotate-001"); status.Code != http.StatusOK {
		t.Errorf("status read by the rotated credential = %d %s, want 200: the same user lost its own submission", status.Code, status.Body.String())
	}
	harness.post(1, successor, "submission-rotate-001", submissionQuestion)
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Errorf("executions after a same-key retry by the rotated credential = %d, want 1", got)
	}
}

// issueDeviceCredential approves and redeems one device authorization for
// approver, granted the harness repository.
func (h *submissionHarness) issueDeviceCredential(t *testing.T, approver string) auth.IssuedCredential {
	t.Helper()
	ctx := context.Background()
	pending, err := h.devices.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("owner-repro-" + approver), UserCodeHash: storage.HashUserCode("OWNREPRO"),
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := storage.DeviceAuthorizationGrant{
		OrgID: "org_1", RepositoryScopes: []string{hostedTestRepository}, Scopes: []string{auth.ScopeContextRead},
		ApprovingSubject: approver, ApprovingAuthenticationMethod: storage.AuthenticationMethodWebAssertion,
	}
	if _, err := h.devices.Approve(ctx, pending.UserCodeHash, grant); err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(h.credentials, auth.ServiceOptions{Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	expires := h.clock.Now().Add(30 * 24 * time.Hour)
	prepared, err := service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "device login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: grant.Scopes, CreatedBy: approver, ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := h.devices.Redeem(ctx, pending.DeviceCodeHash, prepared.StorageInput())
	if err != nil {
		t.Fatal(err)
	}
	issued, err := prepared.Complete(credential)
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

// rotateSelf self-rotates source and returns the successor's token.
func (h *submissionHarness) rotateSelf(t *testing.T, source auth.IssuedCredential) string {
	t.Helper()
	service, err := auth.NewService(h.credentials, auth.ServiceOptions{Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	credential := source.Credential
	rotation, err := service.RotateSelf(context.Background(), storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: credential.CredentialID,
		OrgID: credential.OrgID, CredentialID: credential.CredentialID,
		RepositoryScopes: credential.RepositoryScopes, Permissions: credential.Scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rotation.Issued.Token
}
