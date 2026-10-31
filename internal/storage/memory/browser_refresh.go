package memory

import (
	"context"
	"errors"
	"sync"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// BrowserRefreshStore is the in-memory browser token family store
// (CHAOS-12124). One mutex serializes every refresh and revocation, which is
// the memory form of the Postgres family row lock.
type BrowserRefreshStore struct {
	mu          sync.Mutex
	credentials *storage.CredentialLifecycle
	families    map[string]*browserFamily
	tokens      map[string]*browserRefreshToken
}

type browserFamily struct {
	orgID, clientID string
	expiresAt       time.Time
	revokedAt       *time.Time
	revokedReason   string
}

type browserRefreshToken struct {
	familyID, credentialID string
	consumedAt             *time.Time
	successorHash          string
	seed                   []byte
	accessHash             string
}

var _ storage.BrowserRefreshStore = (*BrowserRefreshStore)(nil)

// NewBrowserRefreshStore builds the store over a credential store.
func NewBrowserRefreshStore(credentials *storage.CredentialLifecycle) (*BrowserRefreshStore, error) {
	if credentials == nil {
		return nil, storage.ErrInvalidBrowserRefresh
	}
	return &BrowserRefreshStore{credentials: credentials, families: map[string]*browserFamily{}, tokens: map[string]*browserRefreshToken{}}, nil
}

// StartBrowserFamily records a family and its first refresh token.
func (s *BrowserRefreshStore) StartBrowserFamily(ctx context.Context, start storage.BrowserFamilyStart) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := storage.ValidateBrowserFamilyStart(start); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.families[start.FamilyID]; exists {
		return storage.ErrConflict
	}
	if _, exists := s.tokens[start.RefreshTokenHash]; exists {
		return storage.ErrConflict
	}
	s.families[start.FamilyID] = &browserFamily{orgID: start.OrgID, clientID: start.ClientID, expiresAt: start.ExpiresAt.UTC()}
	s.tokens[start.RefreshTokenHash] = &browserRefreshToken{familyID: start.FamilyID, credentialID: start.CredentialID}
	return nil
}

// RotateBrowserRefresh decides one refresh.
func (s *BrowserRefreshStore) RotateBrowserRefresh(ctx context.Context, rotation storage.BrowserRefreshRotation) (storage.BrowserRefreshOutcome, error) {
	if err := ctx.Err(); err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	if err := storage.ValidateBrowserRefreshRotation(rotation); err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	now := rotation.Now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[rotation.PresentedHash]
	if !ok {
		return storage.BrowserRefreshOutcome{Decision: storage.BrowserRefreshUnknownToken}, nil
	}
	family := s.families[token.familyID]
	outcome := storage.BrowserRefreshOutcome{OrgID: family.orgID, FamilyID: token.familyID}
	switch {
	case family.clientID != rotation.ClientID:
		outcome.Decision = storage.BrowserRefreshClientMismatch
		return outcome, nil
	case family.revokedAt != nil:
		outcome.Decision = storage.BrowserRefreshFamilyRevoked
		return outcome, nil
	case !now.Before(family.expiresAt):
		outcome.Decision = storage.BrowserRefreshFamilyExpired
		return outcome, nil
	}
	source, err := s.credentials.GetByID(ctx, family.orgID, token.credentialID)
	if err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	if source.RevokedAt != nil {
		revoked, err := s.revokeFamilyLocked(ctx, token.familyID, "member_revoked", rotation.ActorID, now)
		if err != nil {
			return storage.BrowserRefreshOutcome{}, err
		}
		outcome.Decision, outcome.RevokedCredentials = storage.BrowserRefreshFamilyRevoked, revoked
		return outcome, nil
	}
	if token.consumedAt == nil {
		if _, exists := s.tokens[rotation.SuccessorRefresh]; exists {
			return storage.BrowserRefreshOutcome{}, storage.ErrConflict
		}
		successor, err := s.credentials.CreateCredential(ctx, storage.BrowserRefreshSuccessorInput(rotation, source, family.expiresAt, now))
		if err != nil {
			return storage.BrowserRefreshOutcome{}, err
		}
		consumed := now
		token.consumedAt, token.successorHash, token.seed = &consumed, rotation.SuccessorRefresh, append([]byte(nil), rotation.Seed...)
		s.tokens[rotation.SuccessorRefresh] = &browserRefreshToken{familyID: token.familyID, credentialID: successor.CredentialID, accessHash: rotation.SuccessorTokenHash}
		outcome.Decision, outcome.Credential, outcome.Seed = storage.BrowserRefreshRotated, successor, append([]byte(nil), rotation.Seed...)
		outcome.SuccessorTokenHash, outcome.SuccessorRefreshHash = rotation.SuccessorTokenHash, rotation.SuccessorRefresh
		return outcome, nil
	}
	if replay, ok := s.graceReplayLocked(ctx, family, token, rotation.Grace, now); ok {
		replay.OrgID, replay.FamilyID = outcome.OrgID, outcome.FamilyID
		return replay, nil
	}
	revoked, err := s.revokeFamilyLocked(ctx, token.familyID, "reuse", rotation.ActorID, now)
	if err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	outcome.Decision, outcome.RevokedCredentials = storage.BrowserRefreshReuseRevoked, revoked
	return outcome, nil
}

func (s *BrowserRefreshStore) graceReplayLocked(ctx context.Context, family *browserFamily, token *browserRefreshToken, grace time.Duration, now time.Time) (storage.BrowserRefreshOutcome, bool) {
	if token.successorHash == "" || len(token.seed) != 32 || !storage.BrowserRefreshInGrace(*token.consumedAt, now, grace) {
		return storage.BrowserRefreshOutcome{}, false
	}
	successorToken, ok := s.tokens[token.successorHash]
	if !ok || successorToken.consumedAt != nil {
		return storage.BrowserRefreshOutcome{}, false
	}
	successor, err := s.credentials.GetByID(ctx, family.orgID, successorToken.credentialID)
	if err != nil || successor.RevokedAt != nil {
		return storage.BrowserRefreshOutcome{}, false
	}
	return storage.BrowserRefreshOutcome{
		Decision: storage.BrowserRefreshGraceReplay, Credential: successor, Seed: append([]byte(nil), token.seed...),
		SuccessorTokenHash: successorToken.accessHash, SuccessorRefreshHash: token.successorHash,
	}, true
}

// revokeFamilyLocked revokes the family and every live member credential.
func (s *BrowserRefreshStore) revokeFamilyLocked(ctx context.Context, familyID, reason, actorID string, now time.Time) (int, error) {
	family := s.families[familyID]
	if family.revokedAt == nil {
		revokedAt := now
		family.revokedAt, family.revokedReason = &revokedAt, reason
	}
	return revokeFamilyCredentials(ctx, s.credentials, family.orgID, familyID, actorID, reason)
}

// revokeFamilyCredentials revokes every live credential of a family.
func revokeFamilyCredentials(ctx context.Context, credentials *storage.CredentialLifecycle, orgID, familyID, actorID, reason string) (int, error) {
	members, err := credentials.List(ctx, orgID)
	if err != nil {
		return 0, err
	}
	// Revoking one browser member revokes the rest of its family (the
	// credential store's cascade); a member already revoked by it answers
	// ErrConflict. Every member live before is revoked after.
	live := 0
	for _, member := range members {
		if member.Provenance.FamilyID != familyID || member.RevokedAt != nil {
			continue
		}
		live++
		if _, err := credentials.RevokeCredential(ctx, storage.CredentialRevocationInput{
			OrgID: orgID, CredentialID: member.CredentialID, ActorID: actorID, Reason: "browser family " + reason,
		}); err != nil && !errors.Is(err, storage.ErrConflict) {
			return 0, err
		}
	}
	return live, nil
}

// RevokeBrowserFamilyByToken revokes the family a refresh token belongs to.
func (s *BrowserRefreshStore) RevokeBrowserFamilyByToken(ctx context.Context, presentedHash, clientID, actorID string, now time.Time) (storage.BrowserFamilyRevocationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return storage.BrowserFamilyRevocationOutcome{}, err
	}
	if presentedHash == "" || clientID == "" || actorID == "" || now.IsZero() {
		return storage.BrowserFamilyRevocationOutcome{}, storage.ErrInvalidBrowserRefresh
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[presentedHash]
	if !ok {
		return storage.BrowserFamilyRevocationOutcome{Decision: storage.BrowserRefreshUnknownToken}, nil
	}
	family := s.families[token.familyID]
	outcome := storage.BrowserFamilyRevocationOutcome{OrgID: family.orgID, FamilyID: token.familyID}
	switch {
	case family.clientID != clientID:
		outcome.Decision = storage.BrowserRefreshClientMismatch
		return outcome, nil
	case family.revokedAt != nil:
		outcome.Decision = storage.BrowserRefreshFamilyRevoked
		return outcome, nil
	}
	revoked, err := s.revokeFamilyLocked(ctx, token.familyID, "sign_out", actorID, now.UTC())
	if err != nil {
		return storage.BrowserFamilyRevocationOutcome{}, err
	}
	outcome.Decision, outcome.RevokedCredentials = storage.BrowserFamilyRevokedNow, revoked
	return outcome, nil
}

// browserFamilyOf names the token family of a browser credential; the
// credential store's revoke of one revokes the rest of its family.
func browserFamilyOf(credential contractsv1.ClientCredential) (string, bool) {
	if credential.Provenance.ClientClass != string(storage.CredentialClientClassBrowser) || credential.Provenance.FamilyID == "" {
		return "", false
	}
	return credential.Provenance.FamilyID, true
}
