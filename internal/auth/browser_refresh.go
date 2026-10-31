package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Browser refresh grant (CHAOS-12124). A browser-class code exchange also
// returns an opaque refresh token; grant_type=refresh_token rotates it into a
// new 10-minute access credential and a new refresh token, inside the
// family's absolute life. A consumed token presented again within
// BrowserRefreshGrace, while its successor is unused, gets the same
// successor: both successor tokens are an HMAC of the presented token over a
// random rotation seed, so only the seed is stored, never a token.

const (
	// RefreshTokenPrefix starts every browser refresh token. It is not the
	// access token prefix: a refresh token is never a bearer.
	RefreshTokenPrefix = "acrrt_"
	// BrowserRefreshGrace is how long a consumed refresh token still answers
	// its unused successor (a lost response retried, two tabs).
	BrowserRefreshGrace = 30 * time.Second
	refreshTokenBytes   = 32
	refreshSeedBytes    = 32
)

var (
	// ErrBrowserRefreshUnavailable reports a refresh grant that is not
	// configured (no browser client).
	ErrBrowserRefreshUnavailable = errors.New("browser refresh grant unavailable")
	// errRefreshDerivation reports a grace replay whose stored hashes do not
	// match the tokens the presented token derives: a store defect, never
	// answered with tokens.
	errRefreshDerivation = errors.New("browser refresh successor does not match its derivation")
)

// ValidRefreshTokenShape reports whether a value has the refresh token shape.
func ValidRefreshTokenShape(token string) bool {
	secret, ok := strings.CutPrefix(token, RefreshTokenPrefix)
	if !ok || len(secret) != base64.RawURLEncoding.EncodedLen(refreshTokenBytes) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(secret)
	return err == nil
}

// HashRefreshToken is the stored form of a refresh token.
func HashRefreshToken(token string) string { return HashToken(token) }

// deriveRefreshSuccessor derives a rotation's successor access token and
// refresh token from the presented refresh token and the rotation seed. The
// same inputs always give the same tokens; without the presented token the
// stored seed gives nothing.
func deriveRefreshSuccessor(presented string, seed []byte) (access, refresh string) {
	mac := func(label string) []byte {
		h := hmac.New(sha256.New, []byte(presented))
		h.Write([]byte("acr-browser-refresh/v1/" + label + "/"))
		h.Write(seed)
		return h.Sum(nil)
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(mac("access")),
		RefreshTokenPrefix + base64.RawURLEncoding.EncodeToString(mac("refresh"))
}

// BrowserRefreshService runs the refresh grant over a refresh store.
type BrowserRefreshService struct {
	store    storage.BrowserRefreshStore
	now      func() time.Time
	randomMu sync.Mutex
	random   io.Reader
}

// NewBrowserRefreshService builds the service; random defaults to
// crypto/rand.
func NewBrowserRefreshService(store storage.BrowserRefreshStore, now func() time.Time, random io.Reader) (*BrowserRefreshService, error) {
	if storage.IsNil(store) {
		return nil, ErrBrowserRefreshUnavailable
	}
	if now == nil {
		now = time.Now
	}
	if storage.IsNil(random) {
		random = rand.Reader
	}
	return &BrowserRefreshService{store: store, now: now, random: random}, nil
}

func (s *BrowserRefreshService) read(buf []byte) error {
	s.randomMu.Lock()
	defer s.randomMu.Unlock()
	_, err := io.ReadFull(s.random, buf)
	return err
}

// Start begins the family of a browser credential just issued by a code
// exchange and returns its first refresh token.
func (s *BrowserRefreshService) Start(ctx context.Context, issued IssuedCredential) (string, error) {
	provenance := issued.Credential.Provenance
	if provenance.ClientClass != string(storage.CredentialClientClassBrowser) || provenance.FamilyID == "" || provenance.FamilyExpiresAt == nil {
		return "", fmt.Errorf("%w: not a browser credential", ErrInvalidCredential)
	}
	secret := make([]byte, refreshTokenBytes)
	if err := s.read(secret); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	token := RefreshTokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
	err := s.store.StartBrowserFamily(ctx, storage.BrowserFamilyStart{
		OrgID: issued.Credential.OrgID, FamilyID: provenance.FamilyID, ClientID: provenance.ClientID,
		CredentialID: issued.Credential.CredentialID, RefreshTokenHash: HashRefreshToken(token),
		CreatedAt: issued.Credential.CreatedAt, ExpiresAt: *provenance.FamilyExpiresAt,
	})
	if err != nil {
		return "", fmt.Errorf("start browser family: %w", err)
	}
	return token, nil
}

// BrowserRefresh is one refresh's result: the decision, and for a rotation
// or a grace replay the successor credential and refresh token.
type BrowserRefresh struct {
	Decision     storage.BrowserRefreshDecision
	Issued       IssuedCredential
	RefreshToken string
}

const browserRefreshRedacted = "auth.BrowserRefresh{redacted}"

func (BrowserRefresh) String() string   { return browserRefreshRedacted }
func (BrowserRefresh) GoString() string { return browserRefreshRedacted }

// Refresh rotates a presented refresh token for its client. An unknown or
// malformed token is BrowserRefreshUnknownToken; every other decision comes
// from the store under the family's lock.
func (s *BrowserRefreshService) Refresh(ctx context.Context, presented, clientID string) (BrowserRefresh, error) {
	if !ValidRefreshTokenShape(presented) || clientID == "" {
		return BrowserRefresh{Decision: storage.BrowserRefreshUnknownToken}, nil
	}
	seed := make([]byte, refreshSeedBytes)
	if err := s.read(seed); err != nil {
		return BrowserRefresh{}, fmt.Errorf("generate rotation seed: %w", err)
	}
	credentialID, err := GenerateCredentialID()
	if err != nil {
		return BrowserRefresh{}, err
	}
	access, refresh := deriveRefreshSuccessor(presented, seed)
	outcome, err := s.store.RotateBrowserRefresh(ctx, storage.BrowserRefreshRotation{
		PresentedHash: HashRefreshToken(presented), ClientID: clientID, Now: s.now().UTC(), Grace: BrowserRefreshGrace,
		AccessLifetime: BrowserCredentialLifetime, Seed: seed,
		SuccessorCredential: credentialID, SuccessorTokenHash: HashToken(access), SuccessorPrefix: DisplayPrefix(access),
		SuccessorRefresh: HashRefreshToken(refresh), ActorID: browserRefreshActor,
	})
	if err != nil {
		return BrowserRefresh{}, err
	}
	result := BrowserRefresh{Decision: outcome.Decision}
	switch outcome.Decision {
	case storage.BrowserRefreshRotated:
	case storage.BrowserRefreshGraceReplay:
		access, refresh = deriveRefreshSuccessor(presented, outcome.Seed)
	default:
		return result, nil
	}
	if HashToken(access) != outcome.SuccessorTokenHash || HashRefreshToken(refresh) != outcome.SuccessorRefreshHash {
		return BrowserRefresh{}, errRefreshDerivation
	}
	result.Issued = IssuedCredential{Credential: outcome.Credential, Token: access}
	result.RefreshToken = refresh
	return result, nil
}

// RevokeFamily revokes the family of a presented refresh token (sign-out).
func (s *BrowserRefreshService) RevokeFamily(ctx context.Context, presented, clientID string) (storage.BrowserRefreshDecision, error) {
	if !ValidRefreshTokenShape(presented) || clientID == "" {
		return storage.BrowserRefreshUnknownToken, nil
	}
	outcome, err := s.store.RevokeBrowserFamilyByToken(ctx, HashRefreshToken(presented), clientID, browserRefreshActor, s.now().UTC())
	if err != nil {
		return "", err
	}
	return outcome.Decision, nil
}

// browserRefreshActor is the audit actor of a refresh-grant issuance or
// family revocation: the grant itself, not a user.
const browserRefreshActor = "oauth_refresh_grant"
