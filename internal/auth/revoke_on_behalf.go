package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The actor types of RevokeOnBehalf: the user who approved the credential, or
// an organization admin. The Auth Control Plane decides which one the caller
// is; acr records it as the audit actor type.
const (
	OnBehalfActorUser  = oauthvocab.OnBehalfActorUser
	OnBehalfActorAdmin = oauthvocab.OnBehalfActorAdmin
)

// The reasons a RevokeOnBehalf refuses a credential. A caller answers every
// one of them alike, so a refusal tells nothing about the credential.
const (
	OnBehalfRefusedUnknownCredential = oauthvocab.OnBehalfReasonUnknownCredential
	OnBehalfRefusedNotMCPBound       = oauthvocab.OnBehalfReasonNotMCPBound
	OnBehalfRefusedNotApprover       = oauthvocab.OnBehalfReasonNotApprover
)

// ErrRevokeOnBehalfRefused is the refusal of a credential that is not revocable
// on behalf of the named actor; the error is an *OnBehalfRefusal.
var ErrRevokeOnBehalfRefused = errors.New("credential is not revocable on behalf of this actor")

// OnBehalfRefusal names why RevokeOnBehalf refused a credential.
type OnBehalfRefusal struct{ Reason string }

func (r *OnBehalfRefusal) Error() string { return ErrRevokeOnBehalfRefused.Error() + ": " + r.Reason }

func (r *OnBehalfRefusal) Unwrap() error { return ErrRevokeOnBehalfRefused }

// RevokeOnBehalf revokes an MCP-bound (oauth_user class) credential of orgID for
// an actor the Auth Control Plane authorized: an admin of the organization, or
// the user who approved the credential. It takes the same storage path as
// RevokeAsOperator, so it writes the same credential_revoked audit row, with
// actorType as the audit actor type. An unknown id, a credential that is not
// MCP-bound (any other class, the browser class bound to the issuer included),
// and a user who did not approve the credential are each an
// *OnBehalfRefusal; an already-revoked credential is storage.ErrConflict, and
// only an actor the credential admits gets it.
func (s *Service) RevokeOnBehalf(ctx context.Context, orgID, credentialID, actorType, actorID string) (contractsv1.ClientCredential, error) {
	orgID, credentialID, actorID = strings.TrimSpace(orgID), strings.TrimSpace(credentialID), strings.TrimSpace(actorID)
	if orgID == "" || actorID == "" || (actorType != OnBehalfActorUser && actorType != OnBehalfActorAdmin) {
		return contractsv1.ClientCredential{}, fmt.Errorf("%w: org_id, a known actor type and actor are required", ErrInvalidCredential)
	}
	// resource and approving_user_id are written once at issuance and a
	// trigger refuses any change, so this read cannot go stale before the
	// revoke below.
	credential, err := s.store.GetByID(ctx, orgID, credentialID)
	if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalidCredentialInput) {
		return contractsv1.ClientCredential{}, &OnBehalfRefusal{Reason: OnBehalfRefusedUnknownCredential}
	}
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("load credential for revoke: %w", err)
	}
	// MCP-bound is the oauth_user class: an OAuth or RFC 8628 credential the
	// lifecycle bound to a resource other than the issuer. A resource alone is
	// not enough: a browser credential is bound to the issuer (the acr-api
	// resource), and an org bearer or a legacy unclassified row names no MCP
	// client.
	if credential.Provenance.ClientClass != string(storage.CredentialClientClassOAuthUser) || credential.Resource == "" {
		return contractsv1.ClientCredential{}, &OnBehalfRefusal{Reason: OnBehalfRefusedNotMCPBound}
	}
	if actorType == OnBehalfActorUser && credential.Provenance.ApprovingUserID != actorID {
		return contractsv1.ClientCredential{}, &OnBehalfRefusal{Reason: OnBehalfRefusedNotApprover}
	}
	return s.revokeWithActor(ctx, storage.CredentialRevocationInput{
		OrgID: orgID, CredentialID: credential.CredentialID, ActorID: actorID, ActorType: actorType,
	})
}
