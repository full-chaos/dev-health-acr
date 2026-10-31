package storage

import (
	"context"
	"errors"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Browser token families (CHAOS-12124). A browser-class credential's code
// exchange starts a token family: its first access credential and an opaque,
// single-use refresh token. Each refresh rotates the presented token into a
// new access credential (provenance copied) and a new refresh token, inside
// the family's absolute life, which a refresh never extends. The family row is
// the lock every refresh of one family takes, on every replica.

// BrowserRefreshDecision names what one refresh, or one family revocation,
// did. The values are the closed telemetry vocabulary of the refresh grant.
type BrowserRefreshDecision string

const (
	// BrowserRefreshRotated: the presented token was unused; it is now
	// consumed and a successor pair was issued.
	BrowserRefreshRotated BrowserRefreshDecision = "rotated"
	// BrowserRefreshGraceReplay: the presented token was consumed less than
	// the grace window ago and its successor is still unused; the same
	// successor is answered again (no plaintext is kept: the caller derives it
	// again from the presented token and the stored rotation seed).
	BrowserRefreshGraceReplay BrowserRefreshDecision = "grace_replay"
	// BrowserRefreshReuseRevoked: a consumed token was presented outside the
	// grace rule; the whole family is now revoked.
	BrowserRefreshReuseRevoked BrowserRefreshDecision = "reuse_revoked"
	// BrowserRefreshFamilyExpired: the family's absolute life has ended.
	BrowserRefreshFamilyExpired BrowserRefreshDecision = "family_expired"
	// BrowserRefreshFamilyRevoked: the family was already revoked (sign-out,
	// reuse, or a revoked member credential).
	BrowserRefreshFamilyRevoked BrowserRefreshDecision = "family_revoked"
	// BrowserRefreshUnknownToken: no refresh token has this hash.
	BrowserRefreshUnknownToken BrowserRefreshDecision = "unknown_token"
	// BrowserRefreshClientMismatch: the token belongs to another client.
	BrowserRefreshClientMismatch BrowserRefreshDecision = "client_mismatch"
	// BrowserFamilyRevokedNow: a revocation request revoked a live family.
	BrowserFamilyRevokedNow BrowserRefreshDecision = "revoked"
)

// BrowserRefreshDecisionVocabulary lists every decision.
func BrowserRefreshDecisionVocabulary() []BrowserRefreshDecision {
	return []BrowserRefreshDecision{
		BrowserRefreshRotated, BrowserRefreshGraceReplay, BrowserRefreshReuseRevoked, BrowserRefreshFamilyExpired,
		BrowserRefreshFamilyRevoked, BrowserRefreshUnknownToken, BrowserRefreshClientMismatch, BrowserFamilyRevokedNow,
	}
}

// BrowserFamilyStart records a new family at a browser code exchange: the
// family of the credential just issued and its first refresh token.
type BrowserFamilyStart struct {
	OrgID            string
	FamilyID         string
	ClientID         string
	CredentialID     string
	RefreshTokenHash string
	CreatedAt        time.Time
	// ExpiresAt is the family's absolute end: the credential's
	// family_expires_at.
	ExpiresAt time.Time
}

// BrowserRefreshRotation is one refresh request. The successor fields are
// used only when the decision is BrowserRefreshRotated.
type BrowserRefreshRotation struct {
	PresentedHash string
	ClientID      string
	Now           time.Time
	Grace         time.Duration
	// AccessLifetime bounds the successor credential's life; the family end
	// bounds it too.
	AccessLifetime time.Duration
	// Seed is the rotation seed the successor tokens were derived from; it is
	// stored on the presented token's row so a grace replay derives them
	// again.
	Seed                []byte
	SuccessorCredential string
	SuccessorTokenHash  string
	SuccessorPrefix     string
	SuccessorRefresh    string
	ActorID             string
}

// BrowserRefreshOutcome is a refresh's decision and, for a rotation or a
// grace replay, the successor credential with the seed and hashes its tokens
// must derive to.
type BrowserRefreshOutcome struct {
	Decision             BrowserRefreshDecision
	OrgID                string
	FamilyID             string
	Credential           contractsv1.ClientCredential
	Seed                 []byte
	SuccessorTokenHash   string
	SuccessorRefreshHash string
	// RevokedCredentials counts the credentials a reuse revocation revoked.
	RevokedCredentials int
}

// BrowserFamilyRevocationOutcome is a family revocation's decision.
type BrowserFamilyRevocationOutcome struct {
	Decision           BrowserRefreshDecision
	OrgID              string
	FamilyID           string
	RevokedCredentials int
}

// BrowserRefreshStore keeps browser token families and refresh tokens.
type BrowserRefreshStore interface {
	// StartBrowserFamily records a family and its first refresh token.
	StartBrowserFamily(ctx context.Context, start BrowserFamilyStart) error
	// RotateBrowserRefresh decides one refresh under the family's row lock.
	RotateBrowserRefresh(ctx context.Context, rotation BrowserRefreshRotation) (BrowserRefreshOutcome, error)
	// RevokeBrowserFamilyByToken revokes the family a refresh token belongs
	// to (consumed or not), for the client that holds it.
	RevokeBrowserFamilyByToken(ctx context.Context, presentedHash, clientID, actorID string, now time.Time) (BrowserFamilyRevocationOutcome, error)
}

// ErrInvalidBrowserRefresh reports a malformed refresh store request.
var ErrInvalidBrowserRefresh = errors.New("invalid browser refresh request")

// ValidateBrowserRefreshRotation checks a rotation request's shape.
func ValidateBrowserRefreshRotation(rotation BrowserRefreshRotation) error {
	if rotation.PresentedHash == "" || rotation.ClientID == "" || rotation.Now.IsZero() || rotation.Grace < 0 ||
		rotation.AccessLifetime <= 0 || len(rotation.Seed) != 32 || rotation.SuccessorCredential == "" ||
		rotation.SuccessorTokenHash == "" || rotation.SuccessorPrefix == "" || rotation.SuccessorRefresh == "" || rotation.ActorID == "" {
		return ErrInvalidBrowserRefresh
	}
	return nil
}

// ValidateBrowserFamilyStart checks a family start's shape.
func ValidateBrowserFamilyStart(start BrowserFamilyStart) error {
	if start.OrgID == "" || start.FamilyID == "" || start.ClientID == "" || start.CredentialID == "" ||
		start.RefreshTokenHash == "" || start.CreatedAt.IsZero() || !start.ExpiresAt.After(start.CreatedAt) {
		return ErrInvalidBrowserRefresh
	}
	return nil
}

// BrowserRefreshSuccessorExpiry is the successor credential's end: the access
// lifetime from now, never past the family's end.
func BrowserRefreshSuccessorExpiry(now time.Time, lifetime time.Duration, familyEnd time.Time) time.Time {
	end := now.Add(lifetime)
	if familyEnd.Before(end) {
		return familyEnd
	}
	return end
}

// BrowserRefreshInGrace reports whether a token consumed at consumedAt is
// still inside the grace window at now.
func BrowserRefreshInGrace(consumedAt, now time.Time, grace time.Duration) bool {
	return !now.Before(consumedAt) && now.Sub(consumedAt) <= grace
}

// BrowserRefreshSuccessorInput is the create input of a rotation's successor
// credential: the source credential's org, name, scopes, resource and
// provenance (class browser, approving user, client, session, family and the
// family end, all unchanged), the rotation's new id and token, and a life
// bounded by the family end. Both stores normalize it like any issuance.
func BrowserRefreshSuccessorInput(rotation BrowserRefreshRotation, source contractsv1.ClientCredential, familyEnd, now time.Time) CredentialCreateInput {
	expires := BrowserRefreshSuccessorExpiry(now, rotation.AccessLifetime, familyEnd)
	end := familyEnd.UTC()
	return CredentialCreateInput{
		CredentialID: rotation.SuccessorCredential, OrgID: source.OrgID, Name: source.Name,
		TokenPrefix: rotation.SuccessorPrefix, TokenHash: rotation.SuccessorTokenHash,
		RepositoryScopes: append([]string(nil), source.RepositoryScopes...), Scopes: append([]string(nil), source.Scopes...),
		ActorID: rotation.ActorID, ExpiresAt: &expires,
		IssuanceProvenance: CredentialIssuanceProvenanceDeviceAuthorization,
		ApprovingUserID:    source.Provenance.ApprovingUserID, Resource: source.Resource, ClientID: source.Provenance.ClientID,
		ClientClass: CredentialClientClassBrowser, SessionID: source.Provenance.SessionID,
		FamilyID: source.Provenance.FamilyID, FamilyExpiresAt: &end,
	}
}
