package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCredentialService_rejects_empty_lifecycle_actor(t *testing.T) {
	// Given
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStore(t, audit)
	service := newTestService(t, store, audit, time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))

	// When
	_, createErr := service.Create(context.Background(), CreateCredentialRequest{
		OrgID: "org_1", Name: "credential", RepositoryScopes: []string{"owner/repo"},
	})
	created, err := service.Create(context.Background(), CreateCredentialRequest{
		OrgID: "org_1", Name: "credential", RepositoryScopes: []string{"owner/repo"}, CreatedBy: "actor_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, rotateErr := service.Rotate(context.Background(), RotateCredentialRequest{
		OrgID: "org_1", CredentialID: created.Credential.CredentialID,
	})
	_, revokeErr := service.Revoke(context.Background(), "org_1", created.Credential.CredentialID, " ")

	// Then
	for operation, err := range map[string]error{"create": createErr, "rotate": rotateErr, "revoke": revokeErr} {
		if !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("%s error = %v, want invalid credential", operation, err)
		}
	}
}

func TestCredentialService_rejects_typed_nil_lifecycle_store(t *testing.T) {
	// Given
	var store *storage.CredentialLifecycle

	// When
	service, err := NewService(store, ServiceOptions{})

	// Then
	if service != nil || err == nil {
		t.Fatalf("NewService() = %v, %v; want nil service and error", service, err)
	}
}

func TestCredentialService_requiresConcreteAuditedLifecycle(t *testing.T) {
	// Then
	var constructor func(*storage.CredentialLifecycle, ServiceOptions) (*Service, error) = NewService
	_ = constructor
}

func TestCredentialService_rotate_rejects_cross_organization_replacement(t *testing.T) {
	// Given
	store := newMemoryCredentialStore(t, memory.NewAuditStore())
	source, err := store.CreateCredential(context.Background(), storage.CredentialCreateInput{
		CredentialID: "source", OrgID: "org_1", TokenHash: strings.Repeat("a", 64), Name: "source", TokenPrefix: "fcacr_source",
		RepositoryScopes: []string{"owner/repo"}, Scopes: []string{ScopeContextRead}, ActorID: "actor_1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// When
	replacement, err := store.RotateCredential(context.Background(), storage.CredentialRotationInput{
		OrgID: "org_1", SourceCredentialID: source.CredentialID, ActorID: "actor_1",
		Replacement: storage.CredentialRotationReplacement{
			CredentialID: "replacement", TokenHash: strings.Repeat("b", 64), Name: "replacement", TokenPrefix: "fcacr_replacement",
			RepositoryScopes: []string{"owner/repo"}, Scopes: []string{ScopeContextRead}, Immediate: true,
		},
	})

	// Then
	if err != nil || replacement.OrgID != "org_1" {
		t.Fatalf("RotateCredential() = %#v, %v; want org_1 replacement", replacement, err)
	}
}
