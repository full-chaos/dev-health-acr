package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// OrganizationRevokeBatch is the most credentials one RevokeOrganization call
// revokes before it answers More.
const OrganizationRevokeBatch = 500

// The reasons the Auth Control Plane gives for revoking every credential of an
// organization; each credential_revoked audit row records the reason.
const (
	OrganizationRevokeReasonDisabled = contractsv1.CredentialRevokeOrganizationReasonDisabled
	OrganizationRevokeReasonDeleted  = contractsv1.CredentialRevokeOrganizationReasonDeleted
)

// OrganizationRevocation is the outcome of one RevokeOrganization call.
// Revoked counts the credentials it revoked that the caller's own workload
// binding did not mint; CallerRevoked counts those it did (the calling
// credential among them). More is true while live credentials the caller's
// binding did not mint remain, and the caller then calls again.
type OrganizationRevocation struct {
	Revoked       int
	CallerRevoked int
	More          bool
}

// RevokeOrganization revokes the live credentials of orgID (not revoked, not
// expired) of every class: browser, oauth_user, native_device, org_bearer,
// operator, workload and unclassified rows. Each credential goes through the
// one storage revoke path, so each writes its credential_revoked audit row
// (actor type admin, reason) and its revoked lifecycle event; a browser
// credential revokes its whole family with the reason org_deprovisioned.
//
// One call revokes at most limit credentials that the caller's binding did not
// mint, oldest id first. While any such credential remains it answers More and
// leaves the caller's own credentials live, so the caller can call again. Once
// none remains, the same call revokes every live credential the caller's
// binding minted, the calling one included, and answers More false. Each call
// adds at most one credential (the caller's exchange) and removes at least one
// the binding did not mint, so the caller's loop ends. A credential another
// path revoked or purged meanwhile is skipped.
func (s *Service) RevokeOrganization(ctx context.Context, orgID, callerBindingID, callerCredentialID, actorID, reason string, limit int) (OrganizationRevocation, error) {
	orgID, callerBindingID, callerCredentialID, actorID = strings.TrimSpace(orgID), strings.TrimSpace(callerBindingID), strings.TrimSpace(callerCredentialID), strings.TrimSpace(actorID)
	if orgID == "" || callerCredentialID == "" || actorID == "" || limit < 1 ||
		(reason != OrganizationRevokeReasonDisabled && reason != OrganizationRevokeReasonDeleted) {
		return OrganizationRevocation{}, fmt.Errorf("%w: org_id, caller, actor, a known reason and a positive limit are required", ErrInvalidCredential)
	}
	ownedByCaller := func(credential contractsv1.ClientCredential) bool {
		if credential.CredentialID == callerCredentialID {
			return true
		}
		return callerBindingID != "" && credential.WorkloadBindingID != nil && *credential.WorkloadBindingID == callerBindingID
	}
	others, _, err := s.liveOrganizationCredentials(ctx, orgID, ownedByCaller)
	if err != nil {
		return OrganizationRevocation{}, err
	}
	var result OrganizationRevocation
	for _, credential := range others {
		if result.Revoked == limit {
			break
		}
		revoked, err := s.revokeForOrganization(ctx, credential, actorID, reason)
		if err != nil {
			return result, err
		}
		if revoked {
			result.Revoked++
		}
	}
	others, own, err := s.liveOrganizationCredentials(ctx, orgID, ownedByCaller)
	if err != nil {
		return result, err
	}
	if len(others) > 0 {
		result.More = true
		return result, nil
	}
	for _, credential := range own {
		revoked, err := s.revokeForOrganization(ctx, credential, actorID, reason)
		if err != nil {
			return result, err
		}
		if revoked {
			result.CallerRevoked++
		}
	}
	return result, nil
}

// liveOrganizationCredentials splits orgID's live credentials into those the
// caller's binding did not mint and those it did, each sorted by id.
func (s *Service) liveOrganizationCredentials(ctx context.Context, orgID string, ownedByCaller func(contractsv1.ClientCredential) bool) (others, own []contractsv1.ClientCredential, err error) {
	listed, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("list organization credentials: %w", err)
	}
	now := s.now().UTC()
	for _, credential := range listed {
		if credential.RevokedAt != nil || (credential.ExpiresAt != nil && !credential.ExpiresAt.After(now)) {
			continue
		}
		if ownedByCaller(credential) {
			own = append(own, credential)
		} else {
			others = append(others, credential)
		}
	}
	byID := func(a, b contractsv1.ClientCredential) int { return strings.Compare(a.CredentialID, b.CredentialID) }
	slices.SortFunc(others, byID)
	slices.SortFunc(own, byID)
	return others, own, nil
}

// revokeForOrganization revokes one credential for RevokeOrganization; false
// means another path revoked or removed it first.
func (s *Service) revokeForOrganization(ctx context.Context, credential contractsv1.ClientCredential, actorID, reason string) (bool, error) {
	input := storage.CredentialRevocationInput{
		OrgID: credential.OrgID, CredentialID: credential.CredentialID, ActorID: actorID,
		ActorType: oauthvocab.OnBehalfActorAdmin, Reason: reason,
	}
	if credential.Provenance.ClientClass == string(storage.CredentialClientClassBrowser) && credential.Provenance.FamilyID != "" {
		input.FamilyReason = string(storage.BrowserFamilyRevokeOrgDeprovisioned)
	}
	_, err := s.revokeWithActor(ctx, input)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, storage.ErrConflict), errors.Is(err, storage.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}
