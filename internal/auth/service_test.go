package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCredentialServiceCreateDefaultsReadOnlyAndStoresHashOnly(t *testing.T) {
	clock := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	credentialStore := memory.NewCredentialStore()
	auditStore := memory.NewAuditStore()
	service := newTestService(t, credentialStore, auditStore, clock)

	issued, err := service.Create(context.Background(), CreateCredentialRequest{
		OrgID: "org_1", Name: "local sidecar", RepositoryScopes: []string{"Full-Chaos/Dev-Health-ACR"}, CreatedBy: "user_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !HasScope(issued.Credential.Scopes, ScopeContextRead) || !HasScope(issued.Credential.Scopes, ScopeEvidenceRead) {
		t.Fatalf("default scopes are not read-only: %#v", issued.Credential.Scopes)
	}
	if HasScope(issued.Credential.Scopes, ScopeEpisodeWrite) {
		t.Fatal("episode write must not be granted by default")
	}
	if !IsTokenShapeValid(issued.Token) {
		t.Fatal("plaintext token was not returned at issuance")
	}
	record, ok := credentialStore.RecordForTest(issued.Credential.CredentialID)
	if !ok {
		t.Fatal("credential was not persisted")
	}
	if record.TokenHash != HashToken(issued.Token) || record.TokenHash == issued.Token {
		t.Fatal("store must contain only the token hash")
	}
	if record.Metadata.TokenPrefix == issued.Token {
		t.Fatal("metadata exposed the full token")
	}
	if record.Metadata.RepositoryScopes[0] != "full-chaos/dev-health-acr" {
		t.Fatalf("repository scope was not normalized: %#v", record.Metadata.RepositoryScopes)
	}
	events := auditStore.Events()
	if len(events) != 1 || events[0].Action != "credential_created" {
		t.Fatalf("unexpected audit events: %#v", events)
	}
	if containsValue(events[0].Metadata, issued.Token) || containsValue(events[0].Metadata, record.TokenHash) {
		t.Fatal("audit metadata leaked credential material")
	}
}

func TestCredentialServiceRotateSupportsImmediateCutoverAndBoundedOverlap(t *testing.T) {
	clock := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	credentialStore := memory.NewCredentialStore()
	service := newTestService(t, credentialStore, memory.NewAuditStore(), clock)
	first, err := service.Create(context.Background(), CreateCredentialRequest{OrgID: "org_1", Name: "first", RepositoryScopes: []string{"owner/repo"}})
	if err != nil {
		t.Fatal(err)
	}

	second, err := service.Rotate(context.Background(), RotateCredentialRequest{OrgID: "org_1", CredentialID: first.Credential.CredentialID})
	if err != nil {
		t.Fatal(err)
	}
	old, err := credentialStore.GetByID(context.Background(), "org_1", first.Credential.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if old.RevokedAt == nil || !old.RevokedAt.Equal(clock) {
		t.Fatalf("immediate rotation did not revoke previous credential: %#v", old.RevokedAt)
	}
	if second.Token == first.Token {
		t.Fatal("rotation returned the original token")
	}

	third, err := service.Rotate(context.Background(), RotateCredentialRequest{
		OrgID: "org_1", CredentialID: second.Credential.CredentialID, Overlap: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	previous, err := credentialStore.GetByID(context.Background(), "org_1", second.Credential.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if previous.RevokedAt != nil || previous.ExpiresAt == nil || !previous.ExpiresAt.Equal(clock.Add(5*time.Minute)) {
		t.Fatalf("bounded overlap was not applied: %#v", previous)
	}
	if third.Credential.CredentialID == second.Credential.CredentialID {
		t.Fatal("replacement credential id was not changed")
	}

	if _, err := service.Rotate(context.Background(), RotateCredentialRequest{
		OrgID: "org_1", CredentialID: third.Credential.CredentialID, Overlap: 16 * time.Minute,
	}); err == nil {
		t.Fatal("overlap beyond the maximum was accepted")
	}
}

func TestCredentialServiceRevokeAndListAreOrgScoped(t *testing.T) {
	clock := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	credentialStore := memory.NewCredentialStore()
	service := newTestService(t, credentialStore, memory.NewAuditStore(), clock)
	issued, err := service.Create(context.Background(), CreateCredentialRequest{OrgID: "org_1", Name: "first", RepositoryScopes: []string{"owner/repo"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Revoke(context.Background(), "org_2", issued.Credential.CredentialID, "admin"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-org revoke must look not found: %v", err)
	}
	revoked, err := service.Revoke(context.Background(), "org_1", issued.Credential.CredentialID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("credential was not revoked")
	}
	items, err := service.List(context.Background(), "org_1")
	if err != nil || len(items) != 1 {
		t.Fatalf("unexpected list result: %#v %v", items, err)
	}
	other, err := service.List(context.Background(), "org_2")
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-org list leaked records: %#v %v", other, err)
	}
}

func newTestService(t *testing.T, store storage.CredentialStore, audit storage.AuditStore, now time.Time) *Service {
	t.Helper()
	counter := byte(1)
	service, err := NewService(store, audit, ServiceOptions{
		Now: func() time.Time { return now },
		GenerateToken: func() (string, error) {
			bytes := make([]byte, tokenSecretBytes)
			for index := range bytes {
				bytes[index] = counter
			}
			counter++
			return TokenPrefix + base64.RawURLEncoding.EncodeToString(bytes), nil
		},
		GenerateCredentialID: func() (string, error) {
			value := "cred_test_" + string(rune('a'+counter))
			counter++
			return value, nil
		},
		MaximumOverlap: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func containsValue(metadata map[string]any, target string) bool {
	for _, value := range metadata {
		if text, ok := value.(string); ok && text == target {
			return true
		}
	}
	return false
}
